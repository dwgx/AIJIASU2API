package ajs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestSelectNodePollsToSucceeded(t *testing.T) {
	var polls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/accounts/acc-1/actions/select-node":
			var body SelectNodeRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode: %v", err)
			}
			if body.Target == nil || body.Target.ServerID != "srv-7" || body.Wait {
				t.Errorf("body = %+v", body)
			}
			w.Header().Set("Location", "/v1/operations/op-1")
			w.Header().Set("Retry-After", "0")
			writeJSON(w, http.StatusAccepted, `{"id":"op-1","type":"account.switch",
				"state":"queued","accountId":"acc-1","target":{"serverId":"srv-7"},
				"requestedBy":"admin","createdAt":"","startedAt":"","finishedAt":"",
				"steps":[],"result":null,"error":null}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-1":
			if polls.Add(1) == 1 {
				writeJSON(w, 200, `{"id":"op-1","type":"account.switch","state":"running",
					"accountId":"acc-1","target":{"serverId":"srv-7"},"requestedBy":"admin",
					"createdAt":"","startedAt":"","finishedAt":"","steps":[],
					"result":null,"error":null}`)
				return
			}
			writeJSON(w, 200, `{"id":"op-1","type":"account.switch","state":"succeeded",
				"accountId":"acc-1","target":{"serverId":"srv-7"},"requestedBy":"admin",
				"createdAt":"","startedAt":"","finishedAt":"","steps":[],
				"result":{"changed":true,"accountId":"acc-1","nodeId":"acc-1:srv-7",
				"serverId":"srv-7","gatePort":19001,"egressIp":"203.0.113.7",
				"sessionHealthy":true},"error":null}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	result, err := client.SelectNode(context.Background(), "acc-1", SelectNodeRequest{
		Target: &OperationTarget{ServerID: "srv-7"},
	}, WaitOptions{Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("SelectNode: %v", err)
	}
	if !result.Changed || result.NodeID != "acc-1:srv-7" || result.EgressIP != "203.0.113.7" ||
		result.GatePort != 19001 {
		t.Fatalf("result = %+v", result)
	}
	if got := polls.Load(); got != 2 {
		t.Fatalf("polls = %d, want 2", got)
	}
}

func TestSelectNodeAlreadyCurrent(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, 200, `{"changed":false,"accountId":"acc-1","nodeId":"acc-1:srv-7",
			"serverId":"srv-7","gatePort":19001,"egressIp":"203.0.113.7","sessionHealthy":true}`)
	})
	result, err := client.SelectNode(context.Background(), "acc-1", SelectNodeRequest{
		Target: &OperationTarget{ServerID: "srv-7"},
	})
	if err != nil {
		t.Fatalf("SelectNode: %v", err)
	}
	if result.Changed || result.ServerID != "srv-7" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSelectNodeFailedOperation(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Location", "/v1/operations/op-2")
			writeJSON(w, http.StatusAccepted, `{"id":"op-2","type":"account.connect",
				"state":"queued","accountId":"acc-1","target":{"serverId":"srv-7"},
				"requestedBy":"admin","createdAt":"","startedAt":"","finishedAt":"",
				"steps":[],"result":null,"error":null}`)
			return
		}
		writeJSON(w, 200, `{"id":"op-2","type":"account.connect","state":"failed",
			"accountId":"acc-1","target":{"serverId":"srv-7"},"requestedBy":"admin",
			"createdAt":"","startedAt":"","finishedAt":"","steps":[],
			"result":null,"error":{"code":"LOGIN_REQUIRED","message":"login first",
			"details":{"accountId":"acc-1"}}}`)
	})
	_, err := client.SelectNode(context.Background(), "acc-1", SelectNodeRequest{
		Target: &OperationTarget{ServerID: "srv-7"},
	}, WaitOptions{Interval: time.Millisecond})
	var failure *OperationFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %T %v, want *OperationFailure", err, err)
	}
	if failure.OperationID != "op-2" || failure.Code != "LOGIN_REQUIRED" ||
		failure.State != OperationFailed || failure.Details["accountId"] != "acc-1" {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestSelectNodeGatewayTimeoutRecovers(t *testing.T) {
	var gotOperation atomic.Bool
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeJSON(w, http.StatusGatewayTimeout, `{"error":{"code":"OPERATION_TIMEOUT",
				"message":"operation did not finish within timeoutSeconds",
				"reason":"OPERATION_TIMEOUT","details":{"operationId":"op-9"}}}`)
			return
		}
		if r.URL.Path == "/v1/operations/op-9" {
			gotOperation.Store(true)
			writeJSON(w, 200, `{"id":"op-9","type":"account.switch","state":"succeeded",
				"accountId":"acc-1","target":{"serverId":"srv-7"},"requestedBy":"admin",
				"createdAt":"","startedAt":"","finishedAt":"","steps":[],
				"result":{"changed":true,"accountId":"acc-1","serverId":"srv-7",
				"sessionHealthy":true},"error":null}`)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	})
	result, err := client.SelectNode(context.Background(), "acc-1", SelectNodeRequest{
		Target: &OperationTarget{ServerID: "srv-7"}, Wait: true,
	}, WaitOptions{Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("SelectNode: %v", err)
	}
	if !gotOperation.Load() || !result.Changed {
		t.Fatalf("result = %+v, polled = %v", result, gotOperation.Load())
	}
}

func TestWaitOperationTimeout(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, `{"id":"op-3","type":"account.switch","state":"running",
			"accountId":"acc-1","target":{},"requestedBy":"admin","createdAt":"",
			"startedAt":"","finishedAt":"","steps":[],"result":null,"error":null}`)
	})
	_, err := client.WaitOperation(context.Background(), "op-3", WaitOptions{
		Interval: time.Millisecond, Timeout: 40 * time.Millisecond,
	})
	var timeout *OperationTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("error = %T %v, want *OperationTimeoutError", err, err)
	}
	if timeout.OperationID != "op-3" {
		t.Fatalf("timeout = %+v", timeout)
	}
}

func TestCancelOperation(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/operations/op-4" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, 200, `{"id":"op-4","type":"account.switch","state":"canceled",
			"accountId":"acc-1","target":{},"requestedBy":"admin","createdAt":"",
			"startedAt":"","finishedAt":"","steps":[],"result":null,"error":null}`)
	})
	op, err := client.CancelOperation(context.Background(), "op-4")
	if err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	if op.State != OperationCanceled {
		t.Fatalf("state = %s", op.State)
	}
}

func TestListOperationsQuery(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/operations" || q.Get("account") != "acc-1" ||
			q.Get("state") != "failed" || q.Get("type") != "account.switch" || q.Get("limit") != "7" {
			t.Errorf("path/query = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		writeJSON(w, 200, `[]`)
	})
	if _, err := client.ListOperations(context.Background(), ListOperationsOptions{
		Account: "acc-1", State: OperationFailed, Type: "account.switch", Limit: 7,
	}); err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
}
