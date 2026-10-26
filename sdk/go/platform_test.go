package ajs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const leaseDetailJSON = `{
	"lease": {
		"id":"lease-1","userId":"usr-1","userName":"u","accountId":"acc-1",
		"accountLabel":"primary","platform":"linux","nodeId":"acc-1:srv-1",
		"nodeLabel":"Tokyo","region":"jp","egressIp":"203.0.113.9",
		"protocol":"socks5","gatePort":19001,"ttlSeconds":3600,
		"expiresAt":"2026-10-26T01:00:00Z","requests":3,"bytes":1024,
		"sticky":true,"status":"active","idempotencyKey":"k",
		"createdAt":"2026-10-26T00:00:00Z"
	},
	"connection": {
		"host":"127.0.0.1","port":19001,"protocol":"socks5",
		"username":"usr-1","credentialKind":"ajs_sk"
	}
}`

func TestGetLeaseDecodesConnection(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/leases/lease-1" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-AJS-CSRF"); got != "" {
			t.Errorf("GET sent X-AJS-CSRF = %q, want none", got)
		}
		writeJSON(w, 200, leaseDetailJSON)
	})
	detail, err := client.GetLease(context.Background(), "lease-1")
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if detail.Lease.ID != "lease-1" || detail.Lease.GatePort != 19001 {
		t.Fatalf("lease = %+v", detail.Lease)
	}
	want := LeaseConnection{
		Host: "127.0.0.1", Port: 19001, Protocol: ProxySOCKS5,
		Username: "usr-1", CredentialKind: CredentialKindScopedKey,
	}
	if detail.Connection != want {
		t.Fatalf("connection = %+v, want %+v", detail.Connection, want)
	}
}

func TestGetLeaseEscapesID(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/v1/leases/lease%2F1" {
			t.Errorf("escaped path = %q", r.URL.EscapedPath())
		}
		writeJSON(w, 200, leaseDetailJSON)
	})
	if _, err := client.GetLease(context.Background(), "lease/1"); err != nil {
		t.Fatalf("GetLease: %v", err)
	}
}

func TestListNodesQueryAndDecode(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/nodes" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("account_id") != "acc-1" || q.Get("state") != "LIVE" ||
			q.Get("region") != "jp" || q.Get("limit") != "20" || q.Get("offset") != "40" {
			t.Errorf("query = %v", q)
		}
		writeJSON(w, 200, `[
			{"id":"acc-1:srv-1","accountId":"acc-1","accountLabel":"primary","label":"Tokyo",
			 "region":"jp","country":"JP","city":"Tokyo","state":"LIVE","latencyMs":42,
			 "successRate":0.98,"inUse":1,"capacity":4,"egressIp":"203.0.113.9",
			 "protocol":"socks5","gatePort":19001,"lastProbeAt":"2026-10-26T00:00:00Z",
			 "serverId":"srv-1"},
			{"id":"acc-2:srv-2","accountId":"acc-2","accountLabel":"backup","label":"Osaka",
			 "region":"jp","country":"JP","city":"Osaka","state":"STALE","latencyMs":0,
			 "successRate":0,"inUse":0,"capacity":2,"egressIp":"","protocol":"http",
			 "gatePort":0,"lastProbeAt":""}
		]`)
	})
	nodes, err := client.ListNodes(context.Background(), ListNodesOptions{
		AccountID: "acc-1", State: NodeLive, Region: "jp", Limit: 20, Offset: 40,
	})
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %+v", nodes)
	}
	if nodes[0].ServerID != "srv-1" || nodes[0].GatePort != 19001 || nodes[0].SuccessRate != 0.98 {
		t.Fatalf("node[0] = %+v", nodes[0])
	}
	if nodes[1].State != NodeStale || nodes[1].ServerID != "" || nodes[1].GatePort != 0 {
		t.Fatalf("node[1] = %+v", nodes[1])
	}
}

func TestListRequestsQueryAndDecode(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/requests" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("user_id") != "usr-1" || q.Get("offset") != "5" {
			t.Errorf("query = %v", q)
		}
		if got, ok := q["limit"]; !ok || len(got) != 1 || got[0] != "0" {
			t.Errorf("limit = %v, want an explicit 0", got)
		}
		writeJSON(w, 200, `[{
			"id":"req-1","ts":"2026-10-26T00:00:00Z","userId":"usr-1","userName":"u",
			"accountLabel":"primary","nodeLabel":"Tokyo","region":"jp","method":"CONNECT",
			"target":"example.com:443","status":200,"bytes":2048,"latencyMs":12,
			"source":"gate"
		}]`)
	})
	limit := 0
	requests, err := client.ListRequests(context.Background(), ListRequestsOptions{
		UserID: "usr-1", Limit: &limit, Offset: 5,
	})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %+v", requests)
	}
	if requests[0].Source != RequestSourceGate || requests[0].Bytes != 2048 ||
		requests[0].Status != 200 {
		t.Fatalf("request = %+v", requests[0])
	}
}

func TestListRequestsOmitsUnsetLimit(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["limit"]; ok {
			t.Errorf("query = %v, want no limit parameter", r.URL.Query())
		}
		writeJSON(w, 200, `[]`)
	})
	if _, err := client.ListRequests(context.Background(), ListRequestsOptions{}); err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
}

func TestScopedKeyReasonMapping(t *testing.T) {
	denial := func(reason string) *APIError {
		return &APIError{
			StatusCode: http.StatusForbidden, Code: "FORBIDDEN",
			Message: "denied", Reason: reason,
			Method: http.MethodGet, Path: "/v1/nodes",
		}
	}
	cases := []struct {
		name          string
		err           error
		scopeRequired bool
		keyRestricted bool
	}{
		{"scope required", denial(ReasonScopeRequired), true, false},
		{"account restricted", denial(ReasonScopedKeyRestricted), false, true},
		{"other reason", denial("origin_forbidden"), false, false},
		{"no reason", &APIError{StatusCode: http.StatusForbidden, Code: "FORBIDDEN"}, false, false},
		{"other code", &APIError{StatusCode: http.StatusBadRequest, Code: "INVALID_ARGUMENT"}, false, false},
		{"non api", errors.New("boom"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsScopeRequired(tc.err); got != tc.scopeRequired {
				t.Errorf("IsScopeRequired = %v, want %v", got, tc.scopeRequired)
			}
			if got := IsScopedKeyRestricted(tc.err); got != tc.keyRestricted {
				t.Errorf("IsScopedKeyRestricted = %v, want %v", got, tc.keyRestricted)
			}
		})
	}
	if !HasReason(fmt.Errorf("wrapped: %w", denial(ReasonScopedKeyRestricted)), ReasonScopedKeyRestricted) {
		t.Error("HasReason did not unwrap")
	}
}

func TestScopedKeyDenialLeaksNoToken(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusForbidden, `{"error":{"code":"FORBIDDEN",
			"message":"scoped key is restricted to named accounts",
			"reason":"scoped_key_restricted"}}`)
	})
	_, err := client.ListNodes(context.Background(), ListNodesOptions{})
	if !IsScopedKeyRestricted(err) || IsScopeRequired(err) {
		t.Fatalf("err = %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatal("scoped-key error leaked the token")
	}
}
