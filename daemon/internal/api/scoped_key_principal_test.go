package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// TestScopedKeyLeaseOwnership pins the read half of AJS-448: key.UserID is the
// lease owner. A customer key sees its own subject's leases only, even when
// another user holds a lease on the same granted account, and a query naming
// another user is refused without revealing whether that user holds one.
func TestScopedKeyLeaseOwnership(t *testing.T) {
	ts, _ := newTestServer(t)

	mine := mustIssueScopedKey(t, ts, []string{"leases:read"}, []string{"acc-01"}, nil)
	resp, body := scopedCall(t, ts, mine.Secret, http.MethodGet, "/v1/leases", nil)
	requireStatus(t, resp, http.StatusOK)
	leases := decodeBody[[]model.Lease](t, body)
	if len(leases) != 1 || leases[0].ID != "lease-01" || leases[0].UserID != "u-01" {
		t.Fatalf("own leases = %+v, want only lease-01 for u-01", leases)
	}
	// lease-06 is u-03's lease on the same account; the account grant alone
	// would have leaked it.
	if bytes.Contains(body, []byte("lease-06")) || bytes.Contains(body, []byte("qa.pipeline")) {
		t.Fatalf("lease list leaked another user on the same account: %s", body)
	}

	resp, body = scopedCall(t, ts, mine.Secret, http.MethodGet, "/v1/leases?user_id=u-03", nil)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorReason(t, body, ReasonScopedKeyRestricted)
	if bytes.Contains(body, []byte("lease-06")) || bytes.Contains(body, []byte("u-03")) {
		t.Fatalf("refused user filter leaked the foreign user: %s", body)
	}
	resp, body = scopedCall(t, ts, mine.Secret, http.MethodGet, "/v1/leases?userId=u-03", nil)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorReason(t, body, ReasonScopedKeyRestricted)

	// Naming the key's own user is the same as omitting the filter.
	resp, body = scopedCall(t, ts, mine.Secret, http.MethodGet, "/v1/leases?user_id=u-01", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.Lease](t, body); len(got) != 1 || got[0].ID != "lease-01" {
		t.Fatalf("own-user filter = %+v, want lease-01", got)
	}

	// The other side of the same account: a u-03 key sees lease-06 only.
	theirs, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-03", "label": "qa leases", "scopes": []string{"leases:read"},
		"accountIds": []string{"acc-01"},
	})
	resp, body = scopedCall(t, ts, theirs.Secret, http.MethodGet, "/v1/leases", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.Lease](t, body); len(got) != 1 || got[0].ID != "lease-06" || got[0].UserID != "u-03" {
		t.Fatalf("u-03 leases = %+v, want only lease-06", got)
	}

	// Operator callers keep the unfiltered view: admin token and admin scope.
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/leases?user_id=u-03", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.Lease](t, body); len(got) != 2 {
		t.Fatalf("admin user filter = %+v, want lease-05 and lease-06", got)
	}
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/leases", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.Lease](t, body); len(got) != 8 {
		t.Fatalf("admin lease list = %d rows, want 8", len(got))
	}
	adminKey := mustIssueScopedKey(t, ts, []string{"admin"}, nil, nil)
	resp, body = scopedCall(t, ts, adminKey.Secret, http.MethodGet, "/v1/leases", nil)
	requireStatus(t, resp, http.StatusOK)
	if got := decodeBody[[]model.Lease](t, body); len(got) != 8 {
		t.Fatalf("admin-scoped lease list = %d rows, want 8", len(got))
	}
}

// TestScopedKeyLeaseCreateSubject pins the create half of AJS-448: an empty
// userId becomes the key's user, another userId is refused before allocation,
// the account grant still requires an explicit allowed accountId, and a replay
// never hands the key a lease for someone else.
func TestScopedKeyLeaseCreateSubject(t *testing.T) {
	ts, st := newTestServer(t)
	key := mustIssueScopedKey(t, ts, []string{"leases:read", "leases:write"},
		[]string{"acc-01", "acc-03"}, nil)

	// The lease guard admits one active lease per account. lease-01 is the
	// key's own row; lease-06 belongs to u-03, so the operator frees it.
	resp, _ := scopedCall(t, ts, key.Secret, http.MethodDelete, "/v1/leases/lease-01",
		map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)
	if _, err := st.RevokeLease("lease-06"); err != nil {
		t.Fatalf("RevokeLease(lease-06): %v", err)
	}

	// An empty userId becomes the key's user.
	resp, body := scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/leases", map[string]any{
		"platform": "android", "accountId": "acc-01", "idempotencyKey": "subject-replay",
	})
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[leaseCreateResult](t, body)
	if created.Lease.UserID != "u-01" || created.Lease.UserName != "ada.ops" {
		t.Fatalf("created lease subject = %+v, want u-01", created.Lease)
	}

	// A different subject is refused before the store can allocate.
	resp, body = scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/leases", map[string]any{
		"userId": "u-02", "platform": "android", "accountId": "acc-01",
	})
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorReason(t, body, ReasonScopedKeyRestricted)
	for _, lease := range st.Leases() {
		if lease.UserID == "u-02" && lease.AccountID == "acc-01" {
			t.Fatalf("a refused create allocated for another user: %+v", lease)
		}
	}

	// The account grant still applies: accountId is required and must be one
	// the key names.
	resp, body = scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/leases", map[string]any{
		"platform": "android",
	})
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorReason(t, body, ReasonScopedKeyRestricted)
	resp, body = scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/leases", map[string]any{
		"platform": "ios", "accountId": "acc-02",
	})
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorReason(t, body, ReasonScopedKeyRestricted)

	// The same Idempotency-Key replays the key's own lease, not a new one.
	resp, body = scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/leases", map[string]any{
		"platform": "android", "accountId": "acc-01", "idempotencyKey": "subject-replay",
	})
	requireStatus(t, resp, http.StatusCreated)
	replayed := decodeBody[leaseCreateResult](t, body)
	if !replayed.Replayed || replayed.Lease.ID != created.Lease.ID || replayed.Lease.UserID != "u-01" {
		t.Fatalf("replay = %+v, want the original %s", replayed, created.Lease.ID)
	}

	// Another user's Idempotency-Key must not hand that lease to this key. The
	// store refuses the subject mismatch first and the handler re-checks the
	// replayed owner as defence in depth.
	for _, id := range []string{"lease-02", "lease-05"} {
		if _, err := st.RevokeLease(id); err != nil {
			t.Fatalf("RevokeLease(%s): %v", id, err)
		}
	}
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/leases", map[string]any{
		"userId": "u-02", "platform": "linux", "accountId": "acc-03", "idempotencyKey": "foreign-replay",
	})
	requireStatus(t, resp, http.StatusCreated)
	foreign := decodeBody[leaseCreateResult](t, body)
	resp, body = scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/leases", map[string]any{
		"platform": "linux", "accountId": "acc-03", "idempotencyKey": "foreign-replay",
	})
	requireStatus(t, resp, http.StatusConflict)
	requireLeaseReason(t, body, "idempotency_conflict")
	if bytes.Contains(body, []byte(foreign.Lease.ID)) {
		t.Fatalf("refused replay leaked the foreign lease: %s", body)
	}
}

// TestScopedKeyLeaseMutationOwnership pins AJS-448's mutation half: revoke and
// rotate resolve the owner before any side effect. A missing id, another
// user's lease on a foreign account and another user's lease on the key's own
// granted account all answer the same 404; the key's own lease on an account
// outside its grant answers 403; its own lease on its own account still
// mutates.
func TestScopedKeyLeaseMutationOwnership(t *testing.T) {
	ts, st := newTestServer(t)
	key := restrictedWriteKey(t, ts, []string{"acc-01"}, nil)

	// lease-03 is u-02's on acc-02 (foreign account and user); lease-06 is
	// u-03's on acc-01, the account the key grants. Both are refused exactly
	// like a missing id, so the account grant alone never lets a key touch
	// another user's lease and the response cannot reveal which case it hit.
	before03, ok := st.Lease("lease-03")
	if !ok {
		t.Fatal("seed lease-03 is missing")
	}
	before06, ok := st.Lease("lease-06")
	if !ok {
		t.Fatal("seed lease-06 is missing")
	}
	for _, tc := range []struct {
		method  string
		unknown string
		other   string
		body    any
	}{
		{http.MethodDelete, "/v1/leases/lease-99", "/v1/leases/lease-03", map[string]any{"confirm": true}},
		{http.MethodPost, "/v1/leases/lease-99/actions/rotate", "/v1/leases/lease-03/actions/rotate", nil},
		{http.MethodDelete, "/v1/leases/lease-99", "/v1/leases/lease-06", map[string]any{"confirm": true}},
		{http.MethodPost, "/v1/leases/lease-99/actions/rotate", "/v1/leases/lease-06/actions/rotate", nil},
	} {
		respUnknown, unknownBody := scopedCall(t, ts, key.Secret, tc.method, tc.unknown, tc.body)
		requireStatus(t, respUnknown, http.StatusNotFound)
		requireErrorCode(t, unknownBody, CodeNotFound)
		respOther, otherBody := scopedCall(t, ts, key.Secret, tc.method, tc.other, tc.body)
		requireStatus(t, respOther, http.StatusNotFound)
		requireErrorCode(t, otherBody, CodeNotFound)
		if !bytes.Equal(unknownBody, otherBody) {
			t.Fatalf("%s %s and %s answer differently: %s vs %s",
				tc.method, tc.unknown, tc.other, unknownBody, otherBody)
		}
	}
	for id, before := range map[string]model.Lease{"lease-03": before03, "lease-06": before06} {
		after, ok := st.Lease(id)
		if !ok || after.EgressIP != before.EgressIP || after.Requests != before.Requests ||
			after.Status != before.Status || after.ExpiresAt != before.ExpiresAt ||
			after.UserID != before.UserID || after.AccountID != before.AccountID {
			t.Fatalf("%s changed under a refused mutation: %+v (exists=%v)", id, after, ok)
		}
	}

	// The key's own lease on an account outside the grant is refused with the
	// account reason and left in place.
	resp, body := scopedCall(t, ts, key.Secret, http.MethodDelete, "/v1/leases/lease-02",
		map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorReason(t, body, ReasonScopedKeyRestricted)
	if _, ok := st.Lease("lease-02"); !ok {
		t.Fatal("an account-refused revoke removed the key's own lease")
	}

	// The key's own lease on its own account rotates and then revokes.
	resp, body = scopedCall(t, ts, key.Secret, http.MethodPost, "/v1/leases/lease-01/actions/rotate", nil)
	requireStatus(t, resp, http.StatusOK)
	if rotated := decodeBody[model.Lease](t, body); rotated.ID != "lease-01" || rotated.UserID != "u-01" {
		t.Fatalf("own-lease rotate = %+v", rotated)
	}
	resp, _ = scopedCall(t, ts, key.Secret, http.MethodDelete, "/v1/leases/lease-01",
		map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)
	if _, ok := st.Lease("lease-01"); ok {
		t.Fatal("lease-01 survived its own key's revoke")
	}

	// An operator may still mutate a customer's lease.
	resp, _ = apiCall(t, ts, http.MethodDelete, "/v1/leases/lease-03", map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)
}

// TestScopedKeyRevokedKeyLeaseAccess pins the credential half: a revoked key is
// no longer a credential, so every lease route answers 401 before a handler
// runs and nothing is mutated. It is deliberately not a 403/404: those statuses
// describe an authenticated principal that may not act, not a dead key.
func TestScopedKeyRevokedKeyLeaseAccess(t *testing.T) {
	ts, st := newTestServer(t)
	key := restrictedWriteKey(t, ts, []string{"acc-01"}, nil)
	before, ok := st.Lease("lease-01")
	if !ok {
		t.Fatal("seed lease-01 is missing")
	}

	resp, _ := apiCall(t, ts, http.MethodDelete, "/v1/scoped-keys/"+key.Key.ID, nil)
	requireStatus(t, resp, http.StatusNoContent)

	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/v1/leases", nil},
		{http.MethodPost, "/v1/leases", map[string]any{"platform": "android", "accountId": "acc-01"}},
		{http.MethodPost, "/v1/leases/lease-01/actions/rotate", nil},
		{http.MethodDelete, "/v1/leases/lease-01", map[string]any{"confirm": true}},
	} {
		resp, body := scopedCall(t, ts, key.Secret, tc.method, tc.path, tc.body)
		requireStatus(t, resp, http.StatusUnauthorized)
		requireErrorCode(t, body, CodeUnauthorized)
		if bytes.Contains(body, []byte("lease-01")) {
			t.Fatalf("%s %s refusal leaked the lease: %s", tc.method, tc.path, body)
		}
	}

	after, ok := st.Lease("lease-01")
	if !ok || after.EgressIP != before.EgressIP || after.Requests != before.Requests ||
		after.Status != before.Status || after.ExpiresAt != before.ExpiresAt {
		t.Fatalf("lease-01 changed under a revoked key: %+v (exists=%v)", after, ok)
	}
	for _, lease := range st.Leases() {
		if lease.UserID == "u-01" && lease.AccountID == "acc-01" && lease.ID != "lease-01" {
			t.Fatalf("a revoked key allocated a lease: %+v", lease)
		}
	}
}

// TestNonAdminScopedKeyRequiresAccountGrant pins the credential shape: a new
// non-admin key carrying an account, catalog or lease scope must name at least
// one account or region, the admin scope and global-only scopes keep the empty
// meaning, and a key minted before the rule fails closed until it is reissued.
func TestNonAdminScopedKeyRequiresAccountGrant(t *testing.T) {
	ts, st := newTestServer(t)

	for _, scopes := range [][]string{
		{"accounts:read"}, {"accounts:write"},
		{"catalog:read"}, {"catalog:write"},
		{"leases:read"}, {"leases:write"},
		{"requests:read", "leases:read"},
	} {
		resp, body := apiCall(t, ts, http.MethodPost, "/v1/scoped-keys", map[string]any{
			"userId": "u-01", "label": "unbounded " + strings.Join(scopes, "+"), "scopes": scopes,
		})
		requireStatus(t, resp, http.StatusBadRequest)
		requireErrorCode(t, body, CodeInvalidArgument)
		if !bytes.Contains(body, []byte("accountIds or regions")) {
			t.Fatalf("%v refusal does not name the missing grant: %s", scopes, body)
		}
	}

	// Either grant dimension satisfies the rule.
	for _, grants := range []map[string]any{
		{"accountIds": []string{"acc-01"}},
		{"regions": []string{"cn-zj"}},
	} {
		body := map[string]any{
			"userId": "u-01", "label": "granted", "scopes": []string{"leases:read"},
		}
		for name, value := range grants {
			body[name] = value
		}
		mustCreateScopedKey(t, ts, body)
	}

	// The admin scope and global-only scopes are not account-bound.
	adminKey := mustIssueScopedKey(t, ts, []string{"admin"}, nil, nil)
	resp, _ := scopedCall(t, ts, adminKey.Secret, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	open := mustIssueScopedKey(t, ts, []string{"requests:read"}, nil, nil)
	resp, _ = scopedCall(t, ts, open.Secret, http.MethodGet, "/v1/requests?limit=1", nil)
	requireStatus(t, resp, http.StatusOK)

	// A key minted before the rule, restored from a snapshot, fails closed on
	// every non-admin route until it is reissued.
	const legacySecret = "ajs_sk_legacy_unbounded_key"
	raw, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap store.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	sum := sha256.Sum256([]byte(legacySecret))
	snap.ScopedKeys = append(snap.ScopedKeys, model.ScopedKey{
		ID: "sk-legacy", UserID: "u-01", UserName: "ada.ops", Label: "legacy unbounded",
		Prefix: legacySecret[:16], RateLimitPerMin: 0,
		Scopes: []model.Scope{
			model.ScopeAccountsRead, model.ScopeLeasesRead, model.ScopeRequestsRead,
		},
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	snap.ScopedKeyHashes["sk-legacy"] = hex.EncodeToString(sum[:])
	restored, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	if err := st.Restore(restored); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/v1/accounts", nil},
		{http.MethodGet, "/v1/leases", nil},
		{http.MethodGet, "/v1/requests", nil},
		{http.MethodPost, "/v1/leases", map[string]any{"platform": "android", "accountId": "acc-01"}},
	} {
		resp, body := scopedCall(t, ts, legacySecret, tc.method, tc.path, tc.body)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorReason(t, body, ReasonScopedKeyRestricted)
		if !bytes.Contains(body, []byte("reissue")) {
			t.Fatalf("%s %s refusal does not name the reissue: %s", tc.method, tc.path, body)
		}
	}
}
