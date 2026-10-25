package runtime

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// TestQuotaProgress proves the in-flight meter: every chunk advances the
// durable account and user counters, the budget check sees the live total, a
// chunk that crosses the budget is refused, and the completed log row does not
// count the same bytes again.
func TestQuotaProgress(t *testing.T) {
	st := store.NewEmpty()
	account := quotaTestAccount(t, st, "Progress Ops")
	user, err := st.CreateUser(model.CreateUserInput{
		Name:      "progress-bot",
		Allowlist: []string{"127.0.0.1/32"},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	budget := int64(300 << 10)
	setQuotaBudget(t, st, account.ID, &budget)
	if _, err := st.UpdateUser(user.ID, model.UserPatch{QuotaSet: true, QuotaBytesMonth: &budget}); err != nil {
		t.Fatalf("UpdateUser quota: %v", err)
	}

	meter := NewFlowMeter(st)
	// Populate both admission caches with the pre-flow zero totals, exactly as
	// a real CONNECT would before the relay starts.
	if ok, _, _ := meter.Quota().Check(account.ID); !ok {
		t.Fatal("fresh account refused")
	}
	if ok, class, _, _ := meter.Identity().Admit(gate.Principal{UserID: user.ID}, "127.0.0.1"); !ok {
		t.Fatalf("fresh user refused: %s", class)
	}

	chunk := int64(100 << 10)
	for i := range 2 {
		if !meter.Progress(user.ID, account.ID, chunk) {
			t.Fatalf("chunk %d refused below the budget", i+1)
		}
	}
	if got := st.MonthlyUsage(account.ID); got != 2*chunk {
		t.Fatalf("account usage after two chunks = %d, want %d", got, 2*chunk)
	}
	if got := st.UserMonthlyUsage(user.ID); got != 2*chunk {
		t.Fatalf("user usage after two chunks = %d, want %d", got, 2*chunk)
	}

	// The third chunk reaches the budget: it is still accounted (the bytes
	// crossed the tunnel) and then refuses the flow.
	if meter.Progress(user.ID, account.ID, chunk) {
		t.Fatal("chunk at the budget was allowed")
	}
	if got := st.MonthlyUsage(account.ID); got != 3*chunk {
		t.Fatalf("account usage after refusal = %d, want %d", got, 3*chunk)
	}
	if got := st.UserMonthlyUsage(user.ID); got != 3*chunk {
		t.Fatalf("user usage after refusal = %d, want %d", got, 3*chunk)
	}

	// Both admission pre-checks now see the live total.
	if ok, used, _ := meter.Quota().Check(account.ID); ok || used != 3*chunk {
		t.Fatalf("account check after refusal = %v/%d, want false/%d", ok, used, 3*chunk)
	}
	if ok, class, used, _ := meter.Identity().Admit(gate.Principal{UserID: user.ID}, "127.0.0.1"); ok || class != gate.FlowErrorUserQuota || used != 3*chunk {
		t.Fatalf("user admit after refusal = %v/%q/%d, want false/user_quota_exceeded/%d", ok, class, used, 3*chunk)
	}

	// The gate's completion row is log-only: the relay already counted it.
	st.AppendRequest(model.RequestLog{
		UserID: user.ID, UserName: user.Name, AccountLabel: account.Label,
		Source: model.SourceGate, Bytes: 3 * chunk, MeteredLive: true,
	})
	if got := st.MonthlyUsage(account.ID); got != 3*chunk {
		t.Fatalf("account usage after live-metered row = %d, want %d", got, 3*chunk)
	}

	// A row written without live metering still advances both counters.
	st.AppendRequest(model.RequestLog{
		UserID: user.ID, UserName: user.Name, AccountLabel: account.Label,
		Source: model.SourceGate, Bytes: 50,
	})
	if got := st.MonthlyUsage(account.ID); got != 3*chunk+50 {
		t.Fatalf("account usage after ordinary row = %d, want %d", got, 3*chunk+50)
	}
	if got := st.UserMonthlyUsage(user.ID); got != 3*chunk+50 {
		t.Fatalf("user usage after ordinary row = %d, want %d", got, 3*chunk+50)
	}
}

// waitFlowClass waits for one finished flow with the given error class.
func waitFlowClass(t *testing.T, srv *gate.Server, class string) gate.FlowRecord {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		for _, record := range srv.Flows(0) {
			if record.ErrorClass == class {
				return record
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for a %q flow; have %+v", class, srv.Flows(0))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestUserFlowCap proves the per-user active-flow cap: two admitted flows fill
// AJS_GATE_USER_FLOWS=2, the third is refused at admission with user_flow_limit
// and never reaches OpenFlow, and closing one flow releases the slot.
func TestUserFlowCap(t *testing.T) {
	t.Setenv("AJS_GATE_USER_FLOWS", "2")
	f := newIdentityFixture(t, []string{"127.0.0.1/32"}, nil)

	var held []net.Conn
	for i := range 2 {
		conn := f.dial(t)
		if status := authenticate(t, conn, f.user.ID, f.secret); status != 0 {
			t.Fatalf("flow %d auth status = %#x, want 0x00", i+1, status)
		}
		if rep := connectIPv4(t, conn, net.IPv4(127, 0, 0, 1), 443); rep != 0 {
			t.Fatalf("flow %d CONNECT reply = %#x, want success", i+1, rep)
		}
		// Hold the tunnel open for the rest of the test.
		_ = conn.SetDeadline(time.Time{})
		held = append(held, conn)
	}
	if got := f.identity.ActiveFlows(f.user.ID); got != 2 {
		t.Fatalf("active flows = %d, want 2", got)
	}
	opens := len(f.session.recorded())

	third := f.dial(t)
	if status := authenticate(t, third, f.user.ID, f.secret); status != 0 {
		t.Fatalf("third auth status = %#x, want 0x00 (the cap is post-authentication)", status)
	}
	if rep := connectIPv4(t, third, net.IPv4(127, 0, 0, 1), 443); rep != 0x02 {
		t.Fatalf("third CONNECT reply = %#x, want 0x02 (not allowed by ruleset)", rep)
	}
	record := waitFlowClass(t, f.server, gate.FlowErrorUserFlowLimit)
	if record.Status != gate.FlowStatusRejected {
		t.Fatalf("cap record status = %q, want rejected", record.Status)
	}
	if got := len(f.session.recorded()); got != opens {
		t.Fatalf("OpenFlow calls after cap refusal = %d, want %d", got, opens)
	}

	// Closing one admitted flow releases its slot.
	_ = held[0].Close()
	deadline := time.Now().Add(3 * time.Second)
	for f.identity.ActiveFlows(f.user.ID) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("active flows = %d after release, want 1", f.identity.ActiveFlows(f.user.ID))
		}
		time.Sleep(5 * time.Millisecond)
	}
	conn := f.dial(t)
	if status := authenticate(t, conn, f.user.ID, f.secret); status != 0 {
		t.Fatalf("fourth auth status = %#x, want 0x00", status)
	}
	if rep := connectIPv4(t, conn, net.IPv4(127, 0, 0, 1), 443); rep != 0 {
		t.Fatalf("fourth CONNECT reply = %#x, want success after release", rep)
	}
	_ = conn.Close()
	_ = held[1].Close()
}

// TestQuotaProgressConcurrent pins the concurrent bound: every chunk a flow
// reports is accounted exactly once, each flow stops on its first refusal, and
// the total cannot exceed the budget by more than one chunk per in-flight flow.
func TestQuotaProgressConcurrent(t *testing.T) {
	st := store.NewEmpty()
	account := quotaTestAccount(t, st, "Concurrent Ops")
	user, err := st.CreateUser(model.CreateUserInput{Name: "concurrent-bot"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	budget := int64(1 << 20)
	setQuotaBudget(t, st, account.ID, &budget)
	if _, err := st.UpdateUser(user.ID, model.UserPatch{QuotaSet: true, QuotaBytesMonth: &budget}); err != nil {
		t.Fatalf("UpdateUser quota: %v", err)
	}

	meter := NewFlowMeter(st)
	chunk := int64(256 << 10)
	const flows = 8
	var allowed, refused atomic.Int64
	var wg sync.WaitGroup
	for range flows {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each flow relays until the meter refuses it; the refused chunk is
			// still accounted because those bytes crossed the tunnel.
			for range 8 {
				if meter.Progress(user.ID, account.ID, chunk) {
					allowed.Add(1)
					continue
				}
				refused.Add(1)
				return
			}
		}()
	}
	wg.Wait()

	total := (allowed.Load() + refused.Load()) * chunk
	if got := st.MonthlyUsage(account.ID); got != total {
		t.Fatalf("account usage = %d, want %d (every reported chunk counted once)", got, total)
	}
	if got := st.UserMonthlyUsage(user.ID); got != total {
		t.Fatalf("user usage = %d, want %d", got, total)
	}
	if refused.Load() == 0 {
		t.Fatal("no concurrent flow was refused")
	}
	if limit := budget + int64(flows)*chunk; total > limit {
		t.Fatalf("total = %d, want at most %d (budget plus one chunk per flow)", total, limit)
	}
	if ok, used, _ := meter.Quota().Check(account.ID); ok || used < budget {
		t.Fatalf("account check = %v/%d, want refusal at or above %d", ok, used, budget)
	}
}

// TestQuotaRefusedFlowChargesRelayedBytes is finding F1 from ACCEPTANCE-DRILL2
// at runtime level: a flow cut off by the in-flight meter must advance the
// durable monthly counters by exactly the bytes the relay moved. The refused
// chunk crossed the tunnel once, so usageDelta must equal the finished flow's
// BytesIn+BytesOut instead of charging that chunk a second time from flush.
func TestQuotaRefusedFlowChargesRelayedBytes(t *testing.T) {
	budget := int64(300 << 10)
	f := newIdentityFixture(t, []string{"127.0.0.1/32"}, &budget)
	accountBefore := f.store.MonthlyUsage(f.account.ID)
	userBefore := f.store.UserMonthlyUsage(f.user.ID)

	conn := f.dial(t)
	if status := authenticate(t, conn, f.user.ID, f.secret); status != 0 {
		t.Fatalf("auth status = %#x, want 0x00", status)
	}
	if rep := connectIPv4(t, conn, net.IPv4(127, 0, 0, 1), 443); rep != 0 {
		t.Fatalf("CONNECT reply = %#x, want success", rep)
	}
	// Drain the echo like a real client: an unread loopback echo fills the
	// client socket buffer and stalls both directions before the second chunk
	// can cross the budget.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = io.Copy(io.Discard, conn)
	}()
	// Relay past the budget. The gate closes the tunnel once the meter refuses,
	// so the client write may fail part-way; that failure is the refusal.
	_, _ = conn.Write(make([]byte, 1<<20))
	record := waitFlowClass(t, f.server, gate.FlowErrorQuota)
	<-readDone
	if record.Status != gate.FlowStatusOK {
		t.Fatalf("quota record status = %q, want accepted (the flow reached relay)", record.Status)
	}
	relayed := record.BytesIn + record.BytesOut
	if relayed < budget {
		t.Fatalf("relayed bytes = %d, want at least the %d budget so a chunk was refused in flight", relayed, budget)
	}

	accountDelta := f.store.MonthlyUsage(f.account.ID) - accountBefore
	if accountDelta != relayed {
		t.Fatalf("account usage delta = %d, want %d (relayed bytes charged exactly once)", accountDelta, relayed)
	}
	userDelta := f.store.UserMonthlyUsage(f.user.ID) - userBefore
	if userDelta != relayed {
		t.Fatalf("user usage delta = %d, want %d (relayed bytes charged exactly once)", userDelta, relayed)
	}
}
