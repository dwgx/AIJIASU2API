package store_test

import (
	"encoding/json"
	"errors"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// TestNewEmpty locks the daemon's startup state: no mock rows, default
// settings, and sequences that start at 1 so the first created rows are
// acc-01/u-01/key-01. The seeded constructors stay available for tests and
// mock-mode comparisons.
func TestNewEmpty(t *testing.T) {
	sealer, _ := newSealer(t)
	st := store.NewEmptyWithSealer(sealer)

	if accounts := st.Accounts(); len(accounts) != 0 {
		t.Fatalf("accounts = %d, want 0", len(accounts))
	}
	if nodes := st.Nodes(); len(nodes) != 0 {
		t.Fatalf("nodes = %d, want 0", len(nodes))
	}
	if leases := st.Leases(); len(leases) != 0 {
		t.Fatalf("leases = %d, want 0", len(leases))
	}
	if users := st.Users(); len(users) != 0 {
		t.Fatalf("users = %d, want 0", len(users))
	}
	if keys := st.Keys(); len(keys) != 0 {
		t.Fatalf("keys = %d, want 0", len(keys))
	}
	if requests := st.Requests(); len(requests) != 0 {
		t.Fatalf("requests = %d, want 0", len(requests))
	}
	if audit := st.Audit(100); len(audit) != 0 {
		t.Fatalf("audit = %d, want 0", len(audit))
	}
	wantSettings := model.PanelSettings{
		Transport:              model.TransportHTTPUpgrade,
		HealthIntervalSec:      60,
		LeaseTTL:               "8h",
		SubscriptionRefreshSec: 30,
		Theme:                  model.ThemeDark,
	}
	if got := st.Settings(); got != wantSettings {
		t.Fatalf("settings = %+v, want %+v", got, wantSettings)
	}

	account, err := st.CreateAccount(accountInput("fresh", testPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if account.ID != "acc-01" {
		t.Fatalf("first account id = %q, want acc-01", account.ID)
	}
	secret, err := st.AccountSecret(account.ID)
	if err != nil {
		t.Fatalf("AccountSecret: %v", err)
	}
	if string(secret) != testPassword {
		t.Fatalf("AccountSecret = %q, want the created password", secret)
	}

	user, err := st.CreateUser(model.CreateUserInput{Name: "fresh-user"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID != "u-01" {
		t.Fatalf("first user id = %q, want u-01", user.ID)
	}
	key, _, err := st.CreateKey(user.ID, "fresh key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if key.ID != "key-01" {
		t.Fatalf("first key id = %q, want key-01", key.ID)
	}
	audit := st.Audit(3)
	if len(audit) != 3 || audit[0].ID != "audit-003" || audit[2].ID != "audit-001" {
		t.Fatalf("audit ids = %+v, want audit-003..audit-001", audit)
	}

	seeded := store.New()
	if accounts := seeded.Accounts(); len(accounts) != 6 {
		t.Fatalf("seeded store accounts = %d, want 6", len(accounts))
	}
	if nodes := seeded.Nodes(); len(nodes) != 12 {
		t.Fatalf("seeded store nodes = %d, want 12", len(nodes))
	}
}

// TestPurgeDemo locks the AJS_DROP_DEMO semantics: only seed rows go, a live
// account/node/user/key/request/audit row survives, and a second call is a
// no-op.
func TestPurgeDemo(t *testing.T) {
	sealer, _ := newSealer(t)
	st := store.NewWithSealer(sealer)

	live, err := st.CreateAccount(accountInput("live-ajs-01", testPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if live.ID != "acc-07" {
		t.Fatalf("live account id = %q, want acc-07", live.ID)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "acc-07:vvn-2076-264", AccountID: live.ID, Label: "vvn-2076-264",
		Region: "cn-zj", State: model.NodeLive, Protocol: model.ProtocolSOCKS5, GatePort: 13013,
	}); err != nil {
		t.Fatalf("PutNode live: %v", err)
	}
	// A runtime node opened for a demo account must go with that account even
	// though its id is not one of node-01..node-12.
	if _, err := st.PutNode(model.ProxyNode{
		ID: "acc-01:vvn-0000-001", AccountID: "acc-01", Label: "acc01-runtime",
		Region: "cn-zj", State: model.NodeLive, Protocol: model.ProtocolSOCKS5, GatePort: 13014,
	}); err != nil {
		t.Fatalf("PutNode demo: %v", err)
	}
	liveUser, err := st.CreateUser(model.CreateUserInput{Name: "live-user"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if liveUser.ID != "u-05" {
		t.Fatalf("live user id = %q, want u-05", liveUser.ID)
	}
	liveKey, _, err := st.CreateKey(liveUser.ID, "live key")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if liveKey.ID != "key-07" {
		t.Fatalf("live key id = %q, want key-07", liveKey.ID)
	}
	liveRequest, err := st.CreateMutation(model.MutationLabInput{
		Method: "GET", Target: "example.com:443", NodeID: "acc-07:vvn-2076-264",
	})
	if err != nil {
		t.Fatalf("CreateMutation: %v", err)
	}
	if liveRequest.ID != "req-mut-1" {
		t.Fatalf("live request id = %q, want req-mut-1", liveRequest.ID)
	}
	liveAudit := st.Audit(4)
	if len(liveAudit) != 4 || liveAudit[0].ID != "audit-024" || liveAudit[3].ID != "audit-021" {
		t.Fatalf("live audit ids = %+v, want audit-024..audit-021", liveAudit)
	}

	result, err := st.PurgeDemo()
	if err != nil {
		t.Fatalf("PurgeDemo: %v", err)
	}
	want := store.PurgeResult{Accounts: 6, Nodes: 13, Leases: 8, Users: 4, Keys: 6, Requests: 40, Audit: 20}
	if result != want {
		t.Fatalf("PurgeDemo result = %+v, want %+v", result, want)
	}
	if got := result.Total(); got != 97 {
		t.Fatalf("PurgeDemo total = %d, want 97", got)
	}

	accounts := st.Accounts()
	if len(accounts) != 1 || accounts[0].ID != "acc-07" {
		t.Fatalf("accounts after purge = %+v, want only acc-07", accounts)
	}
	if accounts[0].NodeCount != 1 || accounts[0].LiveNodeCount != 1 {
		t.Fatalf("acc-07 node counts = %d/%d, want 1/1", accounts[0].NodeCount, accounts[0].LiveNodeCount)
	}
	nodes := st.Nodes()
	if len(nodes) != 1 || nodes[0].ID != "acc-07:vvn-2076-264" {
		t.Fatalf("nodes after purge = %+v, want only acc-07:vvn-2076-264", nodes)
	}
	if leases := st.Leases(); len(leases) != 0 {
		t.Fatalf("leases after purge = %d, want 0", len(leases))
	}
	users := st.Users()
	if len(users) != 1 || users[0].ID != "u-05" {
		t.Fatalf("users after purge = %+v, want only u-05", users)
	}
	keys := st.Keys()
	if len(keys) != 1 || keys[0].ID != "key-07" {
		t.Fatalf("keys after purge = %+v, want only key-07", keys)
	}
	if _, ok := st.SecretHash("key-07"); !ok {
		t.Fatal("key-07 hash was removed")
	}
	if _, ok := st.SecretHash("key-01"); ok {
		t.Fatal("seed key-01 hash survived the purge")
	}
	requests := st.Requests()
	if len(requests) != 1 || requests[0].ID != "req-mut-1" {
		t.Fatalf("requests after purge = %+v, want only req-mut-1", requests)
	}
	audit := st.Audit(100)
	if len(audit) != 4 || audit[0].ID != "audit-024" || audit[3].ID != "audit-021" {
		t.Fatalf("audit after purge = %+v, want audit-024..audit-021", audit)
	}
	if _, ok := st.Account("acc-01"); ok {
		t.Fatal("seed account acc-01 survived the purge")
	}
	if _, err := st.AccountSecret("acc-01"); err == nil {
		t.Fatal("seed account secret survived the purge")
	}
	if _, err := st.AccountSecret("acc-07"); err != nil {
		t.Fatalf("live account secret was removed: %v", err)
	}

	again, err := st.PurgeDemo()
	if err != nil {
		t.Fatalf("second PurgeDemo: %v", err)
	}
	if again != (store.PurgeResult{}) {
		t.Fatalf("second PurgeDemo = %+v, want zero", again)
	}

	empty := store.NewEmpty()
	if got, err := empty.PurgeDemo(); err != nil || got != (store.PurgeResult{}) {
		t.Fatalf("PurgeDemo on empty store = %+v, %v; want zero, nil", got, err)
	}
}

// TestMetricsRealHealth locks the honest metrics contract: health follows the
// account state and live node count, and traffic is empty/zero rather than a
// synthetic series.
func TestMetricsRealHealth(t *testing.T) {
	st := store.New()
	metrics, err := st.Metrics(model.Range24h)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	wantScores := map[string]int{
		"acc-01": 100, "acc-02": 100, "acc-03": 100, "acc-04": 100,
		"acc-05": 40, "acc-06": 0,
	}
	if len(metrics.AccountHealth) != len(wantScores) {
		t.Fatalf("account health rows = %d, want %d", len(metrics.AccountHealth), len(wantScores))
	}
	for _, row := range metrics.AccountHealth {
		if row.Score != wantScores[row.AccountID] {
			t.Fatalf("%s health = %d, want %d", row.AccountID, row.Score, wantScores[row.AccountID])
		}
	}
	if len(metrics.Series) != 0 {
		t.Fatalf("series points = %d, want 0 without telemetry", len(metrics.Series))
	}
	if metrics.TelemetryAvailable {
		t.Fatal("telemetryAvailable = true without a traffic meter")
	}
	if metrics.Totals != (model.MetricsTotals{}) {
		t.Fatalf("totals without telemetry = %+v, want zero", metrics.Totals)
	}
	if metrics.GeneratedAt == "" {
		t.Fatal("generatedAt is empty")
	}
	week, err := st.Metrics(model.Range7d)
	if err != nil {
		t.Fatalf("Metrics 7d: %v", err)
	}
	if week.Range != model.Range7d || len(week.Series) != 0 || week.TelemetryAvailable {
		t.Fatalf("7d metrics = %+v, want empty series and no telemetry", week)
	}
	if week.Totals != (model.MetricsTotals{}) {
		t.Fatalf("7d totals = %+v, want zero", week.Totals)
	}

	empty := store.NewEmpty()
	account, err := empty.CreateAccount(accountInput("health", testPassword))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if got := singleHealth(t, empty); got.Score != 70 {
		t.Fatalf("active account without a live node = %d, want 70", got.Score)
	}
	if _, err := empty.PutNode(model.ProxyNode{
		ID: account.ID + ":server-1", AccountID: account.ID, Label: "server-1",
		Region: "cn-zj", State: model.NodeLive, Protocol: model.ProtocolSOCKS5, GatePort: 13000,
	}); err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	if got := singleHealth(t, empty); got.Score != 100 {
		t.Fatalf("active account with a live node = %d, want 100", got.Score)
	}
	paused := model.AccountPaused
	if _, err := empty.UpdateAccount(account.ID, model.AccountPatch{State: &paused}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if got := singleHealth(t, empty); got.Score != 40 {
		t.Fatalf("paused account = %d, want 40", got.Score)
	}
	if _, err := empty.MarkAuthFailed(account.ID, "WrongPassword"); err != nil {
		t.Fatalf("MarkAuthFailed: %v", err)
	}
	if got := singleHealth(t, empty); got.Score != 0 {
		t.Fatalf("auth_failed account = %d, want 0", got.Score)
	}
	expired := model.AccountExpired
	if _, err := empty.UpdateAccount(account.ID, model.AccountPatch{State: &expired}); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if got := singleHealth(t, empty); got.Score != 0 {
		t.Fatalf("expired account = %d, want 0", got.Score)
	}
}

func singleHealth(t *testing.T, st *store.Store) model.AccountHealth {
	t.Helper()
	metrics, err := st.Metrics(model.Range24h)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if len(metrics.AccountHealth) != 1 {
		t.Fatalf("account health rows = %d, want 1", len(metrics.AccountHealth))
	}
	return metrics.AccountHealth[0]
}

// TestPurgeDemoPrunesSealerEntries covers the bulk path: a seed account that
// carries a sealed secret in a snapshot written by another build must lose it
// in the sealer file too, not only in the snapshot map.
func TestPurgeDemoPrunesSealerEntries(t *testing.T) {
	sealer, secretsPath := newSealer(t)
	sealed, err := sealer.Seal("acc-01", []byte(testPassword))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	st := store.NewWithSealer(sealer)
	restoreWithSeedSecret(t, st, sealed)
	if got, err := st.AccountSecret("acc-01"); err != nil || string(got) != testPassword {
		t.Fatalf("AccountSecret before purge = %q, %v", got, err)
	}
	if _, ok := sealedEntries(t, secretsPath)["acc-01"]; !ok {
		t.Fatal("the sealer file has no entry for the seed account")
	}

	result, err := st.PurgeDemo()
	if err != nil {
		t.Fatalf("PurgeDemo: %v", err)
	}
	if result.Accounts != 6 {
		t.Fatalf("PurgeDemo accounts = %d, want 6", result.Accounts)
	}
	if _, err := st.AccountSecret("acc-01"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AccountSecret after purge error = %v, want ErrNotFound", err)
	}
	if _, ok := sealedEntries(t, secretsPath)["acc-01"]; ok {
		t.Fatal("PurgeDemo left the seed account's sealed entry in the sealer file")
	}
}

// TestPurgeDemoSealerFailureIsReported pins the error contract: a sealer that
// cannot remove a purged account's entry makes PurgeDemo fail so the caller
// does not save the half-purged snapshot.
func TestPurgeDemoSealerFailureIsReported(t *testing.T) {
	inner, _ := newSealer(t)
	sealed, err := inner.Seal("acc-01", []byte(testPassword))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	st := store.NewWithSealer(deleteFailSealer{Store: inner})
	restoreWithSeedSecret(t, st, sealed)
	if _, err := st.PurgeDemo(); err == nil {
		t.Fatal("PurgeDemo reported success while the sealer refused the delete")
	}
}

// restoreWithSeedSecret rewrites the store from its own snapshot with a sealed
// secret attached to the seed account acc-01.
func restoreWithSeedSecret(t *testing.T, st *store.Store, sealed []byte) {
	t.Helper()
	raw, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap store.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("Unmarshal snapshot: %v", err)
	}
	if snap.Secrets == nil {
		snap.Secrets = make(map[string][]byte)
	}
	snap.Secrets["acc-01"] = sealed
	doc, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal snapshot: %v", err)
	}
	if err := st.Restore(doc); err != nil {
		t.Fatalf("Restore: %v", err)
	}
}
