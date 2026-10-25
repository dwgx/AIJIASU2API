package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

// The tests in this file are the AJS-364 reopen falsifiers of
// REVIEW-MULTISLOT-RESIDUALS finding 10's API half and of the bulk-delete
// mutation claim:
//
//   - bulk delete called the plain pre-mutation hook, so it did not hold the
//     account-wide stop claim across its per-account store cascade;
//   - connect/reconnect discarded the failed SwitchResult, so a reconcile loss
//     carried in its warnings never reached the error envelope.

// bulkClaimCatalog implements CatalogConnector plus the optional stop claim.
// The release records whether the account was already gone from the store: a
// release that sees the row proves the claim did not cover the cascade.
type bulkClaimCatalog struct {
	mu                  sync.Mutex
	st                  *store.Store
	begins              []string
	releases            []string
	releasedAfterDelete []bool
}

func (c *bulkClaimCatalog) SnapshotID() string { return "" }

func (c *bulkClaimCatalog) LookupServer(string, string) runtime.ServerLookup {
	return runtime.ServerLookup{}
}

func (c *bulkClaimCatalog) ConnectServer(context.Context, string, string, string, string) (runtime.SwitchResult, error) {
	return runtime.SwitchResult{}, errors.New("connect is not used by this test")
}

func (c *bulkClaimCatalog) ReconnectAccount(context.Context, string, string, string) (runtime.SwitchResult, error) {
	return runtime.SwitchResult{}, errors.New("reconnect is not used by this test")
}

func (c *bulkClaimCatalog) DisconnectAccount(context.Context, string) (runtime.DisconnectResult, error) {
	return runtime.DisconnectResult{}, errors.New("disconnect is not used by this test")
}

func (c *bulkClaimCatalog) BeginAccountStop(_ context.Context, accountID string) (func(), error) {
	c.mu.Lock()
	c.begins = append(c.begins, accountID)
	c.mu.Unlock()
	return func() {
		_, stillThere := c.st.Account(accountID)
		c.mu.Lock()
		c.releases = append(c.releases, accountID)
		c.releasedAfterDelete = append(c.releasedAfterDelete, !stillThere)
		c.mu.Unlock()
	}, nil
}

// TestBulkDeleteHoldsAccountStopClaim proves the bulk path uses the same
// account-wide claim as DELETE /v1/accounts/{id}: the plain hook is not called,
// every account begins a claim, and each release observes the deleted row.
func TestBulkDeleteHoldsAccountStopClaim(t *testing.T) {
	claimer := &bulkClaimCatalog{}
	hookCalls := 0
	ts, _, st, _, _ := newAccountIOServer(t, func(cfg *Config) {
		cfg.Catalog = claimer
		cfg.AccountDeleteHook = func(context.Context, string) error {
			hookCalls++
			return errors.New("the plain hook must not run when a claimer is wired")
		}
	})
	claimer.st = st
	first := createAccountAPI(t, ts, "claim-one", "claim-one", "password-one")
	second := createAccountAPI(t, ts, "claim-two", "claim-two", "password-two")

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/bulk-delete", map[string]any{
		"ids": []string{first.ID, second.ID},
	})
	requireStatus(t, resp, http.StatusOK)
	var result store.BulkDeleteResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode bulk result: %v", err)
	}
	if result.Counts.Deleted != 2 || result.Counts.Failed != 0 {
		t.Fatalf("counts = %+v, want both deleted", result.Counts)
	}
	if hookCalls != 0 {
		t.Fatalf("plain delete hook calls = %d, want 0: the claim must be used", hookCalls)
	}
	claimer.mu.Lock()
	defer claimer.mu.Unlock()
	if len(claimer.begins) != 2 || claimer.begins[0] != first.ID || claimer.begins[1] != second.ID {
		t.Fatalf("claim begins = %v, want [%s %s]", claimer.begins, first.ID, second.ID)
	}
	if len(claimer.releasedAfterDelete) != 2 || !claimer.releasedAfterDelete[0] || !claimer.releasedAfterDelete[1] {
		t.Fatalf("release saw the account row still present: %v, want both released after delete", claimer.releasedAfterDelete)
	}
	for _, id := range []string{first.ID, second.ID} {
		if _, ok := st.Account(id); ok {
			t.Fatalf("account %s survived the bulk delete", id)
		}
	}
}

// TestCatalogSwitchErrorCarriesReconcileAttribution proves the API no longer
// discards the failed switch result: a reconcile loss in Warnings plus the
// slot/generation attribution reaches the error envelope on both connect and
// reconnect.
func TestCatalogSwitchErrorCarriesReconcileAttribution(t *testing.T) {
	result := runtime.SwitchResult{
		AccountID: "acc-01", ServerID: "srv-b", NodeID: "acc-01#1:srv-b",
		Slot: 1, Generation: 3, PrevNodeID: "acc-01#1:srv-a",
		Warnings: []string{runtime.ReasonReconcileFailed},
	}
	err := &runtime.SwitchError{
		Reason: runtime.ReasonSessionOpen, Message: "could not open the new session",
		Details: map[string]string{"rollback": runtime.ReasonMetadataOnly},
	}
	connector := &fakeCatalog{connect: result, connectErr: err, reconnect: result, reconnectErr: err}
	ts, _ := newCatalogTestServer(t, connector)

	cases := []struct {
		name string
		path string
		body map[string]any
	}{
		{name: "connect", path: "/v1/accounts/acc-01/actions/connect", body: map[string]any{"serverId": "srv-b", "confirm": true}},
		{name: "reconnect", path: "/v1/accounts/acc-01/actions/reconnect", body: map[string]any{"confirm": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := apiCall(t, ts, http.MethodPost, tc.path, tc.body)
			requireStatus(t, resp, http.StatusBadGateway)
			payload := decodeCatalogError(t, body)
			if payload.Error.Reason != runtime.ReasonSessionOpen {
				t.Fatalf("reason = %q, want %q", payload.Error.Reason, runtime.ReasonSessionOpen)
			}
			details := payload.Error.Details
			if details["warnings"] != runtime.ReasonReconcileFailed {
				t.Fatalf("details warnings = %q, want %q", details["warnings"], runtime.ReasonReconcileFailed)
			}
			if details["slot"] != "1" || details["generation"] != "3" {
				t.Fatalf("details attribution = %v, want slot 1 generation 3", details)
			}
			if details["prevNodeId"] != "acc-01#1:srv-a" {
				t.Fatalf("details prevNodeId = %q", details["prevNodeId"])
			}
			if details["rollback"] != runtime.ReasonMetadataOnly {
				t.Fatalf("details rollback = %q, want %q", details["rollback"], runtime.ReasonMetadataOnly)
			}
		})
	}
}
