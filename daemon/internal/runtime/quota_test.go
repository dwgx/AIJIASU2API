package runtime

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// quotaTestAccount creates one account in an empty store.
func quotaTestAccount(t *testing.T, st *store.Store, label string) model.Account {
	t.Helper()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label:       label,
		Username:    "user-" + label,
		Password:    "quota-test-password",
		Platform:    model.PlatformLinux,
		Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("CreateAccount(%q): %v", label, err)
	}
	return account
}

// appendQuotaRow appends one metered gate row for account. Rows are stamped
// with the real clock because store.MonthlyUsage reads the store's own clock.
func appendQuotaRow(t *testing.T, st *store.Store, account model.Account, bytes int64) {
	t.Helper()
	st.AppendRequest(model.RequestLog{
		TS:           time.Now().UTC().Format(time.RFC3339Nano),
		AccountLabel: account.Label,
		Target:       "example.com:443",
		Status:       200,
		Bytes:        bytes,
		Source:       model.SourceGate,
	})
}

func setQuotaBudget(t *testing.T, st *store.Store, accountID string, budget *int64) {
	t.Helper()
	if _, err := st.UpdateAccount(accountID, model.AccountPatch{
		TrafficBudgetSet:   true,
		TrafficBudgetBytes: budget,
	}); err != nil {
		t.Fatalf("UpdateAccount budget: %v", err)
	}
}

// TestQuotaCheckMath covers the admission arithmetic: unlimited accounts pass,
// a configured budget refuses at and above the limit, and zero refuses every
// flow.
func TestQuotaCheckMath(t *testing.T) {
	st := store.NewEmpty()
	account := quotaTestAccount(t, st, "Northwind Ops")
	now := time.Now().UTC()
	q := newQuota(st, func() time.Time { return now }, time.Minute, time.Hour)

	if ok, used, budget := q.Check(account.ID); !ok || used != 0 || budget != 0 {
		t.Fatalf("unlimited check = (%v, %d, %d), want (true, 0, 0)", ok, used, budget)
	}
	if ok, _, _ := q.Check("acc-missing"); !ok {
		t.Fatal("unknown account was refused")
	}

	budget := int64(100)
	setQuotaBudget(t, st, account.ID, &budget)
	appendQuotaRow(t, st, account, 40)
	if ok, used, got := q.Check(account.ID); !ok || used != 40 || got != 100 {
		t.Fatalf("under budget = (%v, %d, %d), want (true, 40, 100)", ok, used, got)
	}

	appendQuotaRow(t, st, account, 60)
	q.Invalidate(account.ID)
	if ok, used, got := q.Check(account.ID); ok || used != 100 || got != 100 {
		t.Fatalf("at budget = (%v, %d, %d), want (false, 100, 100)", ok, used, got)
	}

	zero := int64(0)
	setQuotaBudget(t, st, account.ID, &zero)
	if ok, used, got := q.Check(account.ID); ok || used != 100 || got != 0 {
		t.Fatalf("zero budget = (%v, %d, %d), want (false, 100, 0)", ok, used, got)
	}

	setQuotaBudget(t, st, account.ID, nil)
	if ok, used, budget := q.Check(account.ID); !ok || used != 0 || budget != 0 {
		t.Fatalf("cleared budget = (%v, %d, %d), want (true, 0, 0)", ok, used, budget)
	}
}

// TestQuotaCacheInvalidation proves a metered row is invisible to the cache
// until Invalidate (the meter's path), and that the TTL expires the entry.
func TestQuotaCacheInvalidation(t *testing.T) {
	st := store.NewEmpty()
	account := quotaTestAccount(t, st, "Northwind Ops")
	budget := int64(10_000)
	setQuotaBudget(t, st, account.ID, &budget)

	now := time.Now().UTC()
	q := newQuota(st, func() time.Time { return now }, time.Minute, time.Hour)
	appendQuotaRow(t, st, account, 10)
	if _, used, _ := q.Check(account.ID); used != 10 {
		t.Fatalf("first check used = %d, want 10", used)
	}

	appendQuotaRow(t, st, account, 20)
	if _, used, _ := q.Check(account.ID); used != 10 {
		t.Fatalf("cached check used = %d, want the stale 10", used)
	}

	q.Invalidate(account.ID)
	if _, used, _ := q.Check(account.ID); used != 30 {
		t.Fatalf("invalidated check used = %d, want 30", used)
	}

	appendQuotaRow(t, st, account, 40)
	now = now.Add(2 * time.Minute)
	if _, used, _ := q.Check(account.ID); used != 70 {
		t.Fatalf("expired check used = %d, want 70", used)
	}
}

// TestQuotaCacheDropsStaleMonthEntry proves the month key defeats a still-fresh
// cache entry: crossing into a new UTC month recomputes instead of reusing the
// previous month's total.
func TestQuotaCacheDropsStaleMonthEntry(t *testing.T) {
	st := store.NewEmpty()
	account := quotaTestAccount(t, st, "Northwind Ops")
	budget := int64(10_000)
	setQuotaBudget(t, st, account.ID, &budget)
	appendQuotaRow(t, st, account, 25)

	now := time.Now().UTC()
	q := newQuota(st, func() time.Time { return now }, time.Hour, time.Hour)
	// Plant a fresh entry from a different month with an impossible total.
	q.usage[account.ID] = quotaUsage{
		month:   "1999-01",
		bytes:   999_999,
		expires: now.Add(time.Hour),
	}
	if _, used, _ := q.Check(account.ID); used != 25 {
		t.Fatalf("month mismatch used = %d, want the store's 25", used)
	}
}

// TestQuotaStaleFillCannotOverwriteAdvance pins the cache-fill race: a fill
// that read the store before a chunk landed must not install that read after
// advance moved the cached total forward, or admission undercounts until the
// TTL. The read hook pauses the fill between its store read and its install,
// which is the exact window the compare-and-set has to reject.
func TestQuotaStaleFillCannotOverwriteAdvance(t *testing.T) {
	st := store.NewEmpty()
	account := quotaTestAccount(t, st, "Northwind Ops")
	budget := int64(1000)
	setQuotaBudget(t, st, account.ID, &budget)
	appendQuotaRow(t, st, account, 10)

	now := time.Now().UTC()
	q := newQuota(st, func() time.Time { return now }, time.Minute, time.Hour)
	if _, used, _ := q.Check(account.ID); used != 10 {
		t.Fatalf("first check used = %d, want 10", used)
	}

	// Expire the entry without touching its generation: the next Check has to
	// read the store, which is where the fill pauses.
	now = now.Add(2 * time.Minute)

	readDone := make(chan struct{})
	release := make(chan struct{})
	first := true
	q.readUsage = func(accountID string) int64 {
		used := st.MonthlyUsage(accountID)
		if first {
			first = false
			close(readDone)
			<-release
		}
		return used
	}

	checks := make(chan int64, 1)
	go func() {
		_, used, _ := q.Check(account.ID)
		checks <- used
	}()

	<-readDone
	appendQuotaRow(t, st, account, 30)
	q.advance(account.ID, 30)
	close(release)

	if used := <-checks; used != 40 {
		t.Fatalf("concurrent check used = %d, want the advanced 40", used)
	}
	if _, used, _ := q.Check(account.ID); used != 40 {
		t.Fatalf("cached check used = %d, want 40 after the fill", used)
	}
}

// TestQuotaColdFillCannotOverwriteAdvance pins the same race on a cold cache:
// advance has no entry to move, so it must still invalidate the fill's read or
// the pre-chunk total gets cached. The read hook blocks only the first read;
// the fill's retry reads the store again.
func TestQuotaColdFillCannotOverwriteAdvance(t *testing.T) {
	st := store.NewEmpty()
	account := quotaTestAccount(t, st, "Northwind Ops")
	budget := int64(1000)
	setQuotaBudget(t, st, account.ID, &budget)

	now := time.Now().UTC()
	q := newQuota(st, func() time.Time { return now }, time.Minute, time.Hour)

	readDone := make(chan struct{})
	release := make(chan struct{})
	first := true
	q.readUsage = func(accountID string) int64 {
		used := st.MonthlyUsage(accountID)
		if first {
			first = false
			close(readDone)
			<-release
		}
		return used
	}

	checks := make(chan int64, 1)
	go func() {
		_, used, _ := q.Check(account.ID)
		checks <- used
	}()

	<-readDone
	appendQuotaRow(t, st, account, 30)
	q.advance(account.ID, 30)
	close(release)

	if used := <-checks; used != 30 {
		t.Fatalf("concurrent check used = %d, want the advanced 30", used)
	}
	if _, used, _ := q.Check(account.ID); used != 30 {
		t.Fatalf("cached check used = %d, want 30 after the fill", used)
	}
}

// TestQuotaAuditThrottle proves quota.exceeded is written once per account per
// hour, not once per refused flow.
func TestQuotaAuditThrottle(t *testing.T) {
	st := store.NewEmpty()
	account := quotaTestAccount(t, st, "Northwind Ops")
	budget := int64(10)
	setQuotaBudget(t, st, account.ID, &budget)
	appendQuotaRow(t, st, account, 10)

	now := time.Now().UTC()
	q := newQuota(st, func() time.Time { return now }, time.Minute, time.Hour)
	countAudits := func() int {
		count := 0
		for _, event := range st.Audit(100) {
			if event.Action == "quota.exceeded" {
				count++
			}
		}
		return count
	}

	for range 3 {
		if ok, _, _ := q.Check(account.ID); ok {
			t.Fatal("over-budget check passed")
		}
	}
	if got := countAudits(); got != 1 {
		t.Fatalf("audit rows = %d, want 1 within the hour", got)
	}

	now = now.Add(time.Hour + time.Second)
	if ok, _, _ := q.Check(account.ID); ok {
		t.Fatal("over-budget check passed after the audit interval")
	}
	if got := countAudits(); got != 2 {
		t.Fatalf("audit rows = %d, want 2 after the interval", got)
	}
}

// quotaGateSession records OpenFlow calls and echoes bytes.
type quotaGateSession struct {
	opens atomic.Int64
}

func (s *quotaGateSession) OpenFlow(ctx context.Context, host string, port int) (net.Conn, error) {
	s.opens.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = io.Copy(server, server)
	}()
	return client, nil
}

// socksQuotaConnect performs a no-auth SOCKS5 CONNECT and returns the reply
// code. The caller closes the connection.
func socksQuotaConnect(t *testing.T, addr string) (byte, net.Conn) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial gate %s: %v", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	host := "example.com"
	request := []byte{5, 1, 0, 3, byte(len(host))}
	request = append(request, host...)
	request = append(request, 0, 80)
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("write connect: %v", err)
	}
	var head [10]byte
	if _, err := io.ReadFull(conn, head[:4]); err != nil {
		t.Fatalf("read connect reply: %v", err)
	}
	// The reply's BND.ADDR is a domain/port or an IPv4/IPv6 address; drain it
	// so the caller sees only the reply code.
	var rest int
	switch head[3] {
	case 1:
		rest = 4 + 2
	case 4:
		rest = 16 + 2
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			t.Fatalf("read bound host length: %v", err)
		}
		rest = int(length[0]) + 2
	}
	if rest > 0 {
		if _, err := io.ReadFull(conn, make([]byte, rest)); err != nil {
			t.Fatalf("read bound address: %v", err)
		}
	}
	return head[1], conn
}

// TestFlowMeterGateFactoryEnforcesQuota proves the runtime wiring: the metered
// factory attaches the store-backed quota hook, an over-budget account is
// refused with 0x02 before OpenFlow, and clearing the budget re-admits flows
// without restarting the gate.
func TestFlowMeterGateFactoryEnforcesQuota(t *testing.T) {
	st := store.NewEmpty()
	account := quotaTestAccount(t, st, "Northwind Ops")
	budget := int64(10)
	setQuotaBudget(t, st, account.ID, &budget)
	appendQuotaRow(t, st, account, 25)

	meter := NewFlowMeter(st)
	session := &quotaGateSession{}
	srv, err := meter.GateFactory()(gate.Config{
		NodeID:    nodeID(model.SlotKey{AccountID: account.ID, Index: 1}, "s1"),
		AccountID: account.ID,
		Listen:    "127.0.0.1:0",
	}, session)
	if err != nil {
		t.Fatalf("GateFactory: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	code, conn := socksQuotaConnect(t, srv.Addr().String())
	_ = conn.Close()
	if code != 0x02 {
		t.Fatalf("over-budget reply = %#x, want 0x02", code)
	}
	if opens := session.opens.Load(); opens != 0 {
		t.Fatalf("OpenFlow calls = %d, want 0 for a refused flow", opens)
	}

	setQuotaBudget(t, st, account.ID, nil)
	code, conn = socksQuotaConnect(t, srv.Addr().String())
	_ = conn.Close()
	if code != 0x00 {
		t.Fatalf("unlimited reply = %#x, want success", code)
	}
	if opens := session.opens.Load(); opens != 1 {
		t.Fatalf("OpenFlow calls = %d, want 1 after clearing the budget", opens)
	}
}
