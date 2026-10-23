package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// blockingSealer holds Seal until the test releases it, so a reader that needs
// the store lock can be observed while the seal is in flight.
type blockingSealer struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingSealer() *blockingSealer {
	return &blockingSealer{started: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingSealer) Seal(accountID string, plaintext []byte) ([]byte, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return append([]byte("sealed:"), plaintext...), nil
}

func (b *blockingSealer) Open(string, []byte) ([]byte, error) {
	return nil, errors.New("blockingSealer.Open is not used")
}

// recordingSealer records every account id it seals.
type recordingSealer struct {
	mu     sync.Mutex
	sealed []string
}

func (r *recordingSealer) Seal(accountID string, plaintext []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sealed = append(r.sealed, accountID)
	return append([]byte("sealed:"), plaintext...), nil
}

func (r *recordingSealer) Open(string, []byte) ([]byte, error) {
	return nil, errors.New("recordingSealer.Open is not used")
}

func (r *recordingSealer) ids() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sealed...)
}

// TestCreateAccountSealsOutsideLock is the F6 regression: a slow Seal must not
// block readers, because the store write lock is released across it.
func TestCreateAccountSealsOutsideLock(t *testing.T) {
	sealer := newBlockingSealer()
	st := store.NewWithSealer(sealer)

	created := make(chan error, 1)
	go func() {
		_, err := st.CreateAccount(accountInput("slow-seal", testPassword))
		created <- err
	}()
	select {
	case <-sealer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("CreateAccount never called Seal")
	}

	readers := make(chan struct{})
	go func() {
		_ = st.Accounts()
		_ = st.Nodes()
		_ = st.Users()
		_ = st.Settings()
		_ = st.Leases()
		close(readers)
	}()
	select {
	case <-readers:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("readers blocked while Seal was running")
	}

	close(sealer.release)
	if err := <-created; err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	accounts := st.Accounts()
	if len(accounts) == 0 || accounts[0].Label != "slow-seal" {
		t.Fatalf("created account missing from the store: %+v", accounts)
	}
}

// TestCreateAccountReservesLabel proves concurrent creates for the same label
// yield one account and one seal: the losers conflict before the slow call.
func TestCreateAccountReservesLabel(t *testing.T) {
	sealer := &recordingSealer{}
	st := store.NewWithSealer(sealer)
	before := len(st.Accounts())

	const workers = 8
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := st.CreateAccount(accountInput("race-label", testPassword))
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	succeeded, conflicted := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, store.ErrConflict):
			conflicted++
		default:
			t.Fatalf("CreateAccount error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != workers-1 {
		t.Fatalf("succeeded/conflicted = %d/%d, want 1/%d", succeeded, conflicted, workers-1)
	}
	if after := len(st.Accounts()); after != before+1 {
		t.Fatalf("account count = %d, want %d", after, before+1)
	}
	if ids := sealer.ids(); len(ids) != 1 {
		t.Fatalf("sealed ids = %v, want exactly one", ids)
	}
}

// TestCreateDeleteAccountConcurrent exercises the reserve/seal/insert path
// against concurrent deletes under the race detector.
func TestCreateDeleteAccountConcurrent(t *testing.T) {
	sealer, _ := newSealer(t)
	st := store.NewWithSealer(sealer)

	const workers = 16
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			account, err := st.CreateAccount(accountInput(fmt.Sprintf("conc-%02d", i), testPassword))
			if err != nil {
				t.Errorf("CreateAccount %d: %v", i, err)
				return
			}
			if i%3 == 0 {
				if err := st.DeleteAccount(account.ID); err != nil {
					t.Errorf("DeleteAccount %d: %v", i, err)
					return
				}
			}
			_ = st.Accounts()
		}(i)
	}
	wg.Wait()

	seen := make(map[string]struct{})
	for _, account := range st.Accounts() {
		if _, dup := seen[account.ID]; dup {
			t.Fatalf("duplicate account id %q", account.ID)
		}
		seen[account.ID] = struct{}{}
	}
}

// TestUserQuotaPointerNotAliased is the F8 regression: mutating a returned or
// input quota pointer must not change store state.
func TestUserQuotaPointerNotAliased(t *testing.T) {
	st := store.New()
	quota := int64(1000)
	user, err := st.CreateUser(model.CreateUserInput{Name: "quota-user", QuotaBytesMonth: &quota})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.QuotaBytesMonth == nil || *user.QuotaBytesMonth != 1000 {
		t.Fatalf("created quota = %v", user.QuotaBytesMonth)
	}
	quota = 1
	*user.QuotaBytesMonth = 2
	requireQuota := func(want int64, when string) {
		t.Helper()
		got, ok := st.User(user.ID)
		if !ok || got.QuotaBytesMonth == nil || *got.QuotaBytesMonth != want {
			t.Fatalf("%s: store quota = %v (ok=%v), want %d", when, got.QuotaBytesMonth, ok, want)
		}
	}
	requireQuota(1000, "after mutating the create input and result")

	next := int64(2000)
	updated, err := st.UpdateUser(user.ID, model.UserPatch{QuotaSet: true, QuotaBytesMonth: &next})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	next = 3
	*updated.QuotaBytesMonth = 4
	requireQuota(2000, "after mutating the update input and result")

	for _, listed := range st.Users() {
		if listed.ID == user.ID && listed.QuotaBytesMonth != nil {
			*listed.QuotaBytesMonth = 5
		}
	}
	requireQuota(2000, "after mutating the Users snapshot")
}

// TestAuditRetention is the F5 regression: the history keeps the newest
// AuditRetention events and drops the oldest.
func TestAuditRetention(t *testing.T) {
	st := store.New()
	seed := st.Audit(store.AuditRetention)
	if len(seed) == 0 {
		t.Fatal("seeded audit history is empty")
	}
	// The seed is newest first, so its last row carries the highest id and
	// therefore the current audit sequence.
	seedSeq, err := strconv.Atoi(strings.TrimPrefix(seed[len(seed)-1].ID, "audit-"))
	if err != nil {
		t.Fatalf("seed audit id %q: %v", seed[len(seed)-1].ID, err)
	}

	const extra = 5
	appended := store.AuditRetention + extra
	for i := range appended {
		theme := model.ThemeDark
		if i%2 == 0 {
			theme = model.ThemeLight
		}
		if _, err := st.UpdateSettings(model.SettingsPatch{Theme: &theme}); err != nil {
			t.Fatalf("UpdateSettings %d: %v", i, err)
		}
	}

	events := st.Audit(store.AuditRetention + 10)
	if len(events) != store.AuditRetention {
		t.Fatalf("retained audit events = %d, want %d", len(events), store.AuditRetention)
	}
	wantNewest := fmt.Sprintf("audit-%03d", seedSeq+appended)
	wantOldest := fmt.Sprintf("audit-%03d", seedSeq+appended-store.AuditRetention+1)
	if events[0].ID != wantNewest {
		t.Fatalf("newest audit id = %q, want %q", events[0].ID, wantNewest)
	}
	if last := events[len(events)-1].ID; last != wantOldest {
		t.Fatalf("oldest retained audit id = %q, want %q", last, wantOldest)
	}
}

// TestRestoreTrimsHistories proves an oversized snapshot (older build or a
// hand-edited file) cannot reintroduce unbounded history.
func TestRestoreTrimsHistories(t *testing.T) {
	snap := store.Snapshot{Version: store.SnapshotVersion}
	for i := range store.RequestRetention + 7 {
		snap.Requests = append(snap.Requests, model.RequestLog{ID: fmt.Sprintf("req-%05d", i)})
	}
	for i := range store.AuditRetention + 7 {
		snap.Audit = append(snap.Audit, model.AuditEvent{ID: fmt.Sprintf("audit-%05d", i)})
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal snapshot: %v", err)
	}

	st := store.New()
	if err := st.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := len(st.Requests()); got != store.RequestRetention {
		t.Fatalf("restored requests = %d, want %d", got, store.RequestRetention)
	}
	requests := st.Requests()
	if requests[0].ID != "req-00000" {
		t.Fatalf("restored newest request = %q, want req-00000", requests[0].ID)
	}
	if got := len(st.Audit(store.AuditRetention + 7)); got != store.AuditRetention {
		t.Fatalf("restored audit events = %d, want %d", got, store.AuditRetention)
	}
}
