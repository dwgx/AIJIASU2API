package schedule

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// Engine errors surfaced to the API.
var (
	// ErrSchedulesDisabled means the engine is not running; the read routes
	// still work but every mutation answers 503.
	ErrSchedulesDisabled = errors.New("schedules are disabled")
	// ErrRunInFlight means the same schedule already has a run in flight.
	ErrRunInFlight = errors.New("a run is already in flight for this schedule")
)

// Config configures an Engine. Store is required.
type Config struct {
	Store   *store.Store
	Catalog Catalog
	Logger  *slog.Logger
	FireLog *FireLog

	Tick          time.Duration
	MaxConcurrent int
	QueueSize     int
	StartDeadline time.Duration

	Now func() time.Time
	// Rand drives jitter and random rotation. Nil uses a time-seeded source.
	Rand *rand.Rand

	// AllowMultiSession and NodeLimit are re-checked at fire time: a daemon
	// that cannot guarantee one session must not run a switch-like action.
	// The zero value enforces one session.
	AllowMultiSession bool
	NodeLimit         int

	// Renewal sweep settings. The sweep is read-only and runs once per day.
	RenewalEnabled  bool
	RenewalLeadDays []int
	RenewalHour     int
	RenewalTimezone string

	// OnRun, when set, is called after a run is recorded, with the schedule it
	// belongs to. It runs on the worker goroutine (or the RunNow caller) and
	// must not block; the daemon uses it to emit customer webhook events.
	OnRun func(schedule model.Schedule, run model.ScheduleRun)

	// OnReminder, when set, is called once for every renewal mark a sweep
	// arms, on the goroutine that runs the sweep. It must not block; the daemon
	// uses it to emit customer webhook events.
	OnReminder func(ReminderEvent)
}

// uniqueDescending dedupes and sorts lead days so a sweep cannot emit the
// same threshold twice or count it twice.
func uniqueDescending(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, value := range values {
		if value < 1 || value > 365 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(out)))
	return out
}

// Metrics is the Prometheus-facing engine snapshot.
type Metrics struct {
	Runs        map[string]int
	FireUnknown int
	Inflight    int
	Paused      int
	Schedules   int
	LastRunUnix map[string]int64
	NextRunUnix map[string]int64
	CircuitOpen map[string]bool
}

type runJob struct {
	scheduleID string
	scheduled  time.Time
	enqueuedAt time.Time
	manual     bool
	dryRun     *bool
	force      bool
	catchUp    bool
}

type circuitState struct {
	failures int
	openedAt time.Time
}

// Engine owns the tick, the ready queue, the per-account leases and the fire
// journal. It never scans schedules on a hot path: Tick walks the table once
// per second, which is cheap at the 50-schedule cap.
type Engine struct {
	store   *store.Store
	catalog Catalog
	logger  *slog.Logger
	fireLog *FireLog
	cfg     Config

	mu           sync.Mutex
	running      map[string]struct{}
	runningSlot  map[string]time.Time
	queued       map[string]struct{}
	accountLease map[string]struct{}
	circuits     map[string]circuitState
	lastErrorAt  map[string]time.Time
	lastSkipAt   map[string]time.Time
	rand         *rand.Rand
	frozen       bool
	started      bool
	lastSweepDay string
	unknownFires int
	metrics      map[string]int

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	queue  chan runJob
}

// NewEngine validates cfg and opens the fire journal. It starts no goroutine
// and performs no vendor call.
func NewEngine(cfg Config) (*Engine, error) {
	if cfg.Store == nil {
		return nil, errors.New("schedule: engine requires a store")
	}
	if cfg.Tick <= 0 {
		cfg.Tick = time.Second
	}
	if cfg.Tick > 60*time.Second {
		cfg.Tick = 60 * time.Second
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 4
	}
	if cfg.MaxConcurrent > 16 {
		cfg.MaxConcurrent = 16
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 100
	}
	if cfg.StartDeadline <= 0 {
		cfg.StartDeadline = 60 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.NodeLimit <= 0 {
		cfg.NodeLimit = 1
	}
	if cfg.RenewalLeadDays == nil {
		cfg.RenewalLeadDays = []int{30, 14, 7, 3, 1}
	}
	cfg.RenewalLeadDays = uniqueDescending(cfg.RenewalLeadDays)
	if cfg.RenewalTimezone == "" {
		cfg.RenewalTimezone = "UTC"
	}
	engine := &Engine{
		store:        cfg.Store,
		catalog:      cfg.Catalog,
		logger:       cfg.Logger,
		fireLog:      cfg.FireLog,
		cfg:          cfg,
		running:      make(map[string]struct{}),
		runningSlot:  make(map[string]time.Time),
		queued:       make(map[string]struct{}),
		accountLease: make(map[string]struct{}),
		circuits:     make(map[string]circuitState),
		lastErrorAt:  make(map[string]time.Time),
		lastSkipAt:   make(map[string]time.Time),
		metrics:      make(map[string]int),
	}
	if engine.fireLog == nil {
		log, err := OpenFireLog("", cfg.Now)
		if err != nil {
			return nil, err
		}
		engine.fireLog = log
	}
	if cfg.Rand != nil {
		engine.rand = cfg.Rand
	} else {
		seed := uint64(cfg.Now().UnixNano())
		engine.rand = rand.New(rand.NewPCG(seed, seed>>1))
	}
	return engine, nil
}

// Start replays the journal, re-arms every enabled schedule and launches the
// tick plus the worker pool. It is idempotent: a second call is a no-op.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return nil
	}
	e.started = true
	e.ctx, e.cancel = context.WithCancel(ctx)
	e.mu.Unlock()

	now := e.cfg.Now()
	for _, unknown := range e.fireLog.TakeUnknown() {
		e.unknownFires++
		e.recordFire(unknown)
		e.audit(model.SeverityWarn, "system", "schedule.fire_unknown", unknown.Key,
			"reservation had no terminal result; not retried", map[string]string{
				"scheduleId": unknown.ScheduleID,
				"runId":      unknown.RunID,
				"fireKey":    unknown.Key,
			})
	}
	catchUps := e.rearm(now)

	e.queue = make(chan runJob, e.cfg.QueueSize)
	for range e.cfg.MaxConcurrent {
		e.wg.Add(1)
		go e.worker()
	}
	e.wg.Add(1)
	go e.tickLoop()
	for _, job := range catchUps {
		e.enqueue(job)
	}
	e.audit(model.SeverityInfo, "system", "schedule.engine_start", "scheduler",
		fmt.Sprintf("tick %s · %d workers · %d schedules", e.cfg.Tick, e.cfg.MaxConcurrent, len(e.store.Schedules())),
		map[string]string{
			"tick":        e.cfg.Tick.String(),
			"concurrency": fmt.Sprintf("%d", e.cfg.MaxConcurrent),
			"schedules":   fmt.Sprintf("%d", len(e.store.Schedules())),
		})
	return nil
}

// Stop cancels the tick, drops queued jobs and waits for in-flight runs up to
// the context deadline. An in-flight switch is allowed to finish: canceling it
// mid-open could leave a half-open vendor session.
func (e *Engine) Stop(ctx context.Context) error {
	e.mu.Lock()
	if !e.started {
		e.mu.Unlock()
		return nil
	}
	e.started = false
	if e.cancel != nil {
		e.cancel()
	}
	e.mu.Unlock()

	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	e.mu.Lock()
	inFlight := len(e.running)
	e.mu.Unlock()
	e.audit(model.SeverityInfo, "system", "schedule.engine_stop", "scheduler",
		"engine stopped", map[string]string{"inFlight": fmt.Sprintf("%d", inFlight)})
	return nil
}

// SetFrozen stops new runs without stopping the engine. In-flight runs finish.
func (e *Engine) SetFrozen(frozen bool) {
	e.mu.Lock()
	e.frozen = frozen
	e.mu.Unlock()
}

// Frozen reports the current freeze state.
func (e *Engine) Frozen() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.frozen
}

// Tick processes every due schedule once. It is exported so a fake clock can
// drive the engine without the wall-clock ticker.
func (e *Engine) Tick(now time.Time) {
	e.mu.Lock()
	frozen := e.frozen
	started := e.started
	e.mu.Unlock()
	if frozen || !started {
		return
	}
	for _, schedule := range e.store.Schedules() {
		if !schedule.Enabled || schedule.PausedReason != "" {
			continue
		}
		if schedule.NextRunAt == "" {
			// A row created outside the API (or restored without a slot) is
			// armed here rather than silently never running. A once schedule
			// whose target already passed is disabled, never replayed.
			nextRun := ""
			if next, ok := NextRun(schedule, now, e.drawJitter(schedule)); ok {
				nextRun = next.UTC().Format(time.RFC3339)
			}
			missedOnce := nextRun == "" && schedule.Trigger.Kind == "once"
			e.mutateSchedule(schedule.ID, func(next *model.Schedule) {
				next.NextRunAt = nextRun
				if missedOnce {
					next.Enabled = false
					next.PausedReason = "missed_window"
				}
			})
			if missedOnce {
				e.audit(model.SeverityInfo, "schedule:"+schedule.ID, "schedule.skip", schedule.ID,
					"missed_window", map[string]string{"scheduleId": schedule.ID, "reason": "missed_window"})
			}
			continue
		}
		next, err := time.Parse(time.RFC3339, schedule.NextRunAt)
		if err != nil || next.After(now) {
			continue
		}
		e.mu.Lock()
		_, running := e.running[schedule.ID]
		runningAt := e.runningSlot[schedule.ID]
		e.mu.Unlock()
		if running {
			if runningAt.Equal(next) {
				// The same slot is already executing; a second tick in the
				// same second is not a new occurrence.
				continue
			}
			e.skip(schedule, next, now, "in_flight", false, false)
			continue
		}
		if !e.enqueue(runJob{scheduleID: schedule.ID, scheduled: next, enqueuedAt: now}) {
			e.skip(schedule, next, now, "capacity", false, false)
		}
	}
	if started {
		e.maybeRenewalSweep(now)
	}
}

func (e *Engine) tickLoop() {
	defer e.wg.Done()
	ticker := time.NewTicker(e.cfg.Tick)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.Tick(e.cfg.Now())
		}
	}
}

func (e *Engine) worker() {
	defer e.wg.Done()
	for {
		select {
		case <-e.ctx.Done():
			return
		case job := <-e.queue:
			select {
			case <-e.ctx.Done():
				return
			default:
			}
			e.execute(job)
		}
	}
}

// audit records one scheduler audit row. The store poisons itself when the
// commit fails; logging at error level keeps the failure visible and execute
// stops scheduling on its next tick.
func (e *Engine) audit(severity model.Severity, actor, action, target, detail string, meta map[string]string) error {
	if _, err := e.store.WriteAudit(severity, actor, action, target, detail, meta); err != nil {
		e.logger.Error("schedule_audit_persist_failed", "action", action, "target", target, "error", err)
		return err
	}
	return nil
}

// recordFire mirrors the fire journal into the store under the same rule.
func (e *Engine) recordFire(fire model.ScheduleFire) error {
	if err := e.store.RecordScheduleFire(fire); err != nil {
		e.logger.Error("schedule_fire_persist_failed", "schedule_id", fire.ScheduleID,
			"key", fire.Key, "error", err)
		return err
	}
	return nil
}

// mutateSchedule applies one schedule update; a failed commit poisons the
// store and is never dropped on the floor.
func (e *Engine) mutateSchedule(id string, fn func(*model.Schedule)) error {
	if _, err := e.store.MutateSchedule(id, fn); err != nil {
		e.logger.Error("schedule_update_persist_failed", "schedule_id", id, "error", err)
		return err
	}
	return nil
}

func (e *Engine) execute(job runJob) {
	if err := e.store.DurabilityError(); err != nil {
		// A failed commit poisons the store; main is terminating. Do not start
		// another run whose result could never be recorded.
		e.logger.Error("schedule_store_poisoned", "schedule_id", job.scheduleID, "error", err)
		return
	}
	e.mu.Lock()
	delete(e.queued, job.scheduleID)
	_, already := e.running[job.scheduleID]
	if already && !job.manual {
		e.mu.Unlock()
		return
	}
	e.running[job.scheduleID] = struct{}{}
	e.runningSlot[job.scheduleID] = job.scheduled
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.running, job.scheduleID)
		delete(e.runningSlot, job.scheduleID)
		e.mu.Unlock()
	}()
	if !job.manual && e.cfg.Now().Sub(job.enqueuedAt) > e.cfg.StartDeadline {
		if schedule, ok := e.store.Schedule(job.scheduleID); ok {
			e.skip(schedule, job.scheduled, e.cfg.Now(), "capacity", false, false)
		}
		return
	}
	e.run(job)
}

func (e *Engine) enqueue(job runJob) bool {
	e.mu.Lock()
	if _, queued := e.queued[job.scheduleID]; queued {
		e.mu.Unlock()
		return true
	}
	if len(e.queue) >= cap(e.queue) {
		e.mu.Unlock()
		return false
	}
	e.queued[job.scheduleID] = struct{}{}
	e.mu.Unlock()
	select {
	case e.queue <- job:
		return true
	default:
		e.mu.Lock()
		delete(e.queued, job.scheduleID)
		e.mu.Unlock()
		return false
	}
}

// RunNow executes one schedule synchronously for POST .../actions/run. It
// takes the same guards and account lease as a scheduled run.
func (e *Engine) RunNow(ctx context.Context, scheduleID string, dryRun bool, force bool) (model.ScheduleRun, error) {
	e.mu.Lock()
	if !e.started {
		e.mu.Unlock()
		return model.ScheduleRun{}, ErrSchedulesDisabled
	}
	if _, running := e.running[scheduleID]; running {
		e.mu.Unlock()
		return model.ScheduleRun{}, ErrRunInFlight
	}
	e.running[scheduleID] = struct{}{}
	e.runningSlot[scheduleID] = time.Time{}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.running, scheduleID)
		delete(e.runningSlot, scheduleID)
		e.mu.Unlock()
	}()
	schedule, ok := e.store.Schedule(scheduleID)
	if !ok {
		return model.ScheduleRun{}, store.ErrNotFound
	}
	now := e.cfg.Now()
	job := runJob{scheduleID: scheduleID, scheduled: now, enqueuedAt: now, manual: true,
		dryRun: new(dryRun), force: force}
	run, err := e.runWithContext(ctx, job, schedule)
	if err != nil {
		return run, err
	}
	return run, nil
}

// IsRunning reports whether a run is in flight for scheduleID.
func (e *Engine) IsRunning(scheduleID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.running[scheduleID]
	return ok
}

// Metrics returns a copy of the counters.
func (e *Engine) Metrics() Metrics {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := Metrics{
		Runs:        make(map[string]int, len(e.metrics)),
		FireUnknown: e.unknownFires,
		Inflight:    len(e.running),
		LastRunUnix: make(map[string]int64),
		NextRunUnix: make(map[string]int64),
		CircuitOpen: make(map[string]bool),
	}
	for result, count := range e.metrics {
		out.Runs[result] = count
	}
	for _, schedule := range e.store.Schedules() {
		out.Schedules++
		if schedule.PausedReason != "" || !schedule.Enabled {
			out.Paused++
		}
		if at, err := time.Parse(time.RFC3339, schedule.LastRunAt); err == nil {
			out.LastRunUnix[schedule.ID] = at.Unix()
		}
		if at, err := time.Parse(time.RFC3339, schedule.NextRunAt); err == nil {
			out.NextRunUnix[schedule.ID] = at.Unix()
		}
	}
	now := e.cfg.Now()
	for account, state := range e.circuits {
		if !state.openedAt.IsZero() && now.Sub(state.openedAt) < 5*time.Minute {
			out.CircuitOpen[account] = true
		}
	}
	return out
}

// Checkpoint compacts the fire journal to the records already in the snapshot.
// main calls it after a successful state save.
func (e *Engine) Checkpoint() error {
	// Keep every journal record, including reservations still in flight. The
	// snapshot carries terminal records; dropping a live reservation would
	// re-open the at-most-once gate after a crash.
	return e.fireLog.Compact(e.fireLog.Records())
}

func (e *Engine) run(job runJob) {
	schedule, ok := e.store.Schedule(job.scheduleID)
	if !ok {
		return
	}
	_, _ = e.runWithContext(e.ctx, job, schedule)
}

func (e *Engine) runWithContext(ctx context.Context, job runJob, schedule model.Schedule) (model.ScheduleRun, error) {
	now := e.cfg.Now()
	if job.manual && e.Frozen() {
		return e.recordSkip(schedule, job, now, "frozen"), nil
	}
	if job.manual {
		// A manual run is explicit: it bypasses the window and cooldown but
		// still respects the account lock and (unless forced) the daily cap.
		if !job.force {
			if reason := e.dailyCapReason(schedule, now); reason != "" {
				run := e.recordSkip(schedule, job, now, reason)
				return run, nil
			}
		}
	} else {
		if e.Frozen() {
			e.skip(schedule, job.scheduled, now, "frozen", true, false)
			return model.ScheduleRun{}, nil
		}
		if !schedule.Enabled || schedule.PausedReason != "" {
			e.advance(schedule, job.scheduled, now)
			return model.ScheduleRun{}, nil
		}
		loc, err := LoadTimezone(schedule.Trigger.Timezone)
		if err != nil {
			loc = time.UTC
		}
		if win, err := parseWindow(schedule.Window); err == nil && win != nil && !win.contains(job.scheduled, loc) {
			e.skip(schedule, job.scheduled, now, "outside_window", false, false)
			return model.ScheduleRun{}, nil
		}
		if e.inCooldown(schedule, now) {
			e.skip(schedule, job.scheduled, now, "cooldown", false, false)
			return model.ScheduleRun{}, nil
		}
		if reason := e.dailyCapReason(schedule, now); reason != "" {
			e.skip(schedule, job.scheduled, now, reason, false, false)
			return model.ScheduleRun{}, nil
		}
		if e.circuitOpen(schedule.Target.ID, now) {
			e.skip(schedule, job.scheduled, now, "circuit_open", false, false)
			return model.ScheduleRun{}, nil
		}
	}

	dryRun := schedule.Guard.DryRun
	if job.dryRun != nil {
		dryRun = *job.dryRun
	}
	runID, runIDErr := e.store.NextRunID()
	if runIDErr != nil {
		e.logger.Error("schedule_run_id_persist_failed", "schedule_id", schedule.ID, "error", runIDErr)
		return model.ScheduleRun{}, runIDErr
	}
	fireKey := fireKeyFor(schedule.ID, job.scheduled)
	if job.manual {
		fireKey = "manual:" + runID
	}
	reserved, err := e.fireLog.Reserve(fireKey, schedule.ID, job.scheduled.Unix(), runID,
		"schedule:"+schedule.ID, now)
	if err != nil {
		run := e.finishRun(schedule, job, runID, fireKey, now, actionOutcome{
			Result: "error", Reason: "persist", ErrorClass: "persist", Detail: "fire journal unavailable", Pause: true,
		}, now)
		return run, nil
	}
	if !reserved {
		run := e.recordSkip(schedule, job, now, "duplicate_fire")
		return run, nil
	}

	if dryRun {
		// A dry run performs no mutation, so it does not take the account
		// execution lease; it still records the attempt and the fire key.
		outcome := e.performSafe(ctx, schedule, true)
		finished := e.cfg.Now()
		state := "dry_run"
		if outcome.Result == "error" {
			state = "failed"
		}
		if err := e.fireLog.Finish(fireKey, state, finished); err != nil {
			e.logger.Warn("schedule_fire_finish_failed", "schedule_id", schedule.ID, "fire_key", fireKey, "error", err)
		}
		return e.finishRun(schedule, job, runID, fireKey, now, outcome, finished), nil
	}

	release, ok := e.acquireAccount(schedule.Target.ID, schedule.ConcurrencyPolicy, ctx)
	if !ok {
		finished := e.cfg.Now()
		_ = e.fireLog.Finish(fireKey, "skipped", finished)
		e.recordFire(model.ScheduleFire{
			Key: fireKey, ScheduleID: schedule.ID, ScheduledUnix: job.scheduled.Unix(),
			State: "skipped", RunID: runID, Owner: "schedule:" + schedule.ID,
			ReservedAt: now.UTC().Format(time.RFC3339), FinishedAt: finished.UTC().Format(time.RFC3339),
		})
		run := e.recordSkip(schedule, job, now, "in_flight")
		return run, nil
	}
	outcome := e.performSafe(ctx, schedule, dryRun)
	release()
	finished := e.cfg.Now()
	state := "failed"
	switch outcome.Result {
	case "ok":
		state = "succeeded"
	case "skipped":
		state = "skipped"
	case "dry_run":
		state = "dry_run"
	case "unknown":
		state = "unknown"
	}
	if err := e.fireLog.Finish(fireKey, state, finished); err != nil {
		e.logger.Warn("schedule_fire_finish_failed", "schedule_id", schedule.ID, "fire_key", fireKey, "error", err)
	}
	run := e.finishRun(schedule, job, runID, fireKey, now, outcome, finished)
	return run, nil
}

func (e *Engine) finishRun(schedule model.Schedule, job runJob, runID, fireKey string, started time.Time, outcome actionOutcome, finished time.Time) model.ScheduleRun {
	state := "failed"
	result := "error"
	switch outcome.Result {
	case "ok":
		state, result = "succeeded", "ok"
	case "skipped":
		state, result = "skipped", "skipped"
	case "dry_run":
		state, result = "dry_run", "dry_run"
	case "unknown":
		state, result = "unknown", "unknown"
	}
	run := model.ScheduleRun{
		ID:           runID,
		ScheduleID:   schedule.ID,
		FireKey:      fireKey,
		Trigger:      triggerKind(schedule, job.manual),
		ScheduledFor: job.scheduled.UTC().Format(time.RFC3339),
		StartedAt:    started.UTC().Format(time.RFC3339),
		FinishedAt:   finished.UTC().Format(time.RFC3339),
		State:        state,
		Result:       result,
		Reason:       outcome.Reason,
		ErrorClass:   outcome.ErrorClass,
		Detail:       outcome.Detail,
		AuditID:      outcome.AuditID,
		Actor:        "schedule:" + schedule.ID,
	}
	stored, persistErr := e.store.AppendScheduleRun(run)
	if persistErr != nil {
		// The run row is not durable: the store is poisoned. Stop the
		// completion here — no fire mirror, no schedule update, no audit and no
		// customer notification for a run the journal cannot hold.
		e.logger.Error("schedule_run_persist_failed", "schedule_id", schedule.ID, "error", persistErr)
		return run
	}
	run = stored
	fire := model.ScheduleFire{
		Key:           fireKey,
		ScheduleID:    schedule.ID,
		ScheduledUnix: job.scheduled.Unix(),
		State:         state,
		RunID:         runID,
		Owner:         "schedule:" + schedule.ID,
		ReservedAt:    started.UTC().Format(time.RFC3339),
		FinishedAt:    finished.UTC().Format(time.RFC3339),
	}
	if err := e.recordFire(fire); err != nil {
		return run
	}
	e.mu.Lock()
	e.metrics[result]++
	e.mu.Unlock()
	e.updateAfterRun(schedule, job, run, outcome, started, finished)
	if job.manual {
		if err := e.audit(model.SeverityInfo, "operator", "schedule.run", schedule.ID,
			run.Reason, map[string]string{
				"scheduleId": schedule.ID,
				"runId":      run.ID,
				"result":     run.Result,
				"reason":     run.Reason,
				"dryRun":     fmt.Sprintf("%t", dryRunOf(job, schedule)),
			}); err != nil {
			return run
		}
	}
	if result == "error" {
		if outcome.ErrorClass == "transient" || outcome.ErrorClass == "internal" || outcome.ErrorClass == "invariant" {
			e.recordCircuitFailure(schedule.Target.ID)
		}
		e.auditError(schedule, run, started)
	}
	// The customer webhook is the one step that leaves the process: it fires
	// only when every row above is durable. A poisoned store stops here even
	// when a later audit (auditError) failed.
	if err := e.store.DurabilityError(); err != nil {
		e.logger.Error("schedule_notify_suppressed", "schedule_id", schedule.ID, "error", err)
		return run
	}
	if e.cfg.OnRun != nil {
		e.cfg.OnRun(schedule, run)
	}
	return run
}

func (e *Engine) updateAfterRun(schedule model.Schedule, job runJob, run model.ScheduleRun, outcome actionOutcome, started, finished time.Time) {
	failures := schedule.ConsecutiveFailures
	pause := outcome.Pause
	switch {
	case outcome.Result == "ok" || outcome.Result == "dry_run":
		failures = 0
	case outcome.ErrorClass == "transient" || outcome.ErrorClass == "internal" || outcome.ErrorClass == "invariant":
		failures++
		if failures >= 5 {
			pause = true
		}
	}
	nextRun := ""
	if schedule.Trigger.Kind != "once" {
		probe := schedule
		probe.LastScheduledAt = job.scheduled.UTC().Format(time.RFC3339)
		probe.LastRunAt = started.UTC().Format(time.RFC3339)
		if outcome.Result == "skipped" {
			// A guard/conflict skip is not a missed run: advance to the next
			// occurrence instead of applying the run_once catch-up.
			probe.MisfirePolicy = "skip"
		}
		base := job.scheduled
		if job.catchUp {
			// The catch-up itself is the one replay; the next slot is
			// computed from now so it cannot chain a second one.
			base = finished
		}
		if next, ok := NextAfter(probe, base, finished, e.drawJitter(schedule)); ok {
			nextRun = next.UTC().Format(time.RFC3339)
		}
	}
	if schedule.Trigger.Kind == "once" {
		if outcome.Result == "dry_run" {
			// A manual dry run must not consume the one-shot target: keep the
			// future slot so the real run still happens.
			if at, err := time.Parse(time.RFC3339, schedule.NextRunAt); err == nil && at.After(finished) {
				nextRun = schedule.NextRunAt
			}
		} else {
			nextRun = ""
		}
	}
	if outcome.Result == "error" && outcome.Reason == "" {
		outcome.Reason = "error"
	}
	runCount := e.runCountToday(schedule, finished)
	e.mutateSchedule(schedule.ID, func(next *model.Schedule) {
		if outcome.Result != "skipped" {
			next.LastRunAt = started.UTC().Format(time.RFC3339)
		}
		next.LastScheduledAt = job.scheduled.UTC().Format(time.RFC3339)
		next.LastResult = outcome.Result
		next.LastError = outcome.Reason
		next.ConsecutiveFailures = failures
		next.Degraded = failures >= 3
		next.NextRunAt = nextRun
		next.RunCountToday = runCount
		if outcome.HasCursor {
			next.Cursor = outcome.Cursor
		}
		if pause {
			// A paused schedule keeps the computed slot for display; the
			// engine skips it until an operator resumes it.
			next.Enabled = false
			next.PausedReason = outcome.Reason
			next.NextRunAt = nextRun
		}
		if schedule.Trigger.Kind == "once" && outcome.Result != "dry_run" {
			// A one-shot attempt is never replayed: it is done, failed or
			// skipped, so the schedule is disabled. A dry run keeps it armed.
			next.Enabled = false
			next.NextRunAt = ""
			if outcome.Result == "error" && outcome.Reason != "" && next.PausedReason == "" {
				next.PausedReason = outcome.Reason
			}
		}
	})
	if pause {
		e.auditPause(schedule, outcome.Reason)
	}
}

func (e *Engine) skip(schedule model.Schedule, scheduled, now time.Time, reason string, quiet, manual bool) model.ScheduleRun {
	runID, runIDErr := e.store.NextRunID()
	if runIDErr != nil {
		e.logger.Error("schedule_run_id_persist_failed", "schedule_id", schedule.ID, "error", runIDErr)
		return model.ScheduleRun{}
	}
	run := model.ScheduleRun{
		ID:           runID,
		ScheduleID:   schedule.ID,
		Trigger:      triggerKind(schedule, manual),
		ScheduledFor: scheduled.UTC().Format(time.RFC3339),
		StartedAt:    now.UTC().Format(time.RFC3339),
		FinishedAt:   now.UTC().Format(time.RFC3339),
		State:        "skipped",
		Result:       "skipped",
		Reason:       reason,
		Actor:        "schedule:" + schedule.ID,
	}
	stored, persistErr := e.store.AppendScheduleRun(run)
	if persistErr != nil {
		// Same rule as finishRun: a skip the journal cannot hold must not
		// advance the schedule, rewrite its state or emit an audit row.
		e.logger.Error("schedule_run_persist_failed", "schedule_id", schedule.ID, "error", persistErr)
		return run
	}
	run = stored
	e.mu.Lock()
	e.metrics["skipped"]++
	e.mu.Unlock()
	nextRun := ""
	if schedule.Trigger.Kind != "once" {
		probe := schedule
		probe.LastScheduledAt = scheduled.UTC().Format(time.RFC3339)
		// A skip advances to the next occurrence; run_once is only for a
		// daemon that was down, never for a guard rejection.
		probe.MisfirePolicy = "skip"
		if next, ok := NextAfter(probe, scheduled, now, e.drawJitter(schedule)); ok {
			nextRun = next.UTC().Format(time.RFC3339)
		}
	}
	runCount := e.runCountToday(schedule, now)
	e.mutateSchedule(schedule.ID, func(next *model.Schedule) {
		next.LastScheduledAt = scheduled.UTC().Format(time.RFC3339)
		next.LastResult = "skipped"
		next.LastError = reason
		next.NextRunAt = nextRun
		next.RunCountToday = runCount
	})
	if !quiet {
		e.auditSkip(schedule, reason, now)
	}
	return run
}

func (e *Engine) recordSkip(schedule model.Schedule, job runJob, now time.Time, reason string) model.ScheduleRun {
	return e.skip(schedule, job.scheduled, now, reason, false, job.manual)
}

func (e *Engine) advance(schedule model.Schedule, scheduled, now time.Time) {
	if schedule.Trigger.Kind == "once" {
		e.mutateSchedule(schedule.ID, func(next *model.Schedule) { next.NextRunAt = "" })
		return
	}
	probe := schedule
	probe.LastScheduledAt = scheduled.UTC().Format(time.RFC3339)
	probe.MisfirePolicy = "skip"
	nextRun := ""
	if next, ok := NextAfter(probe, scheduled, now, e.drawJitter(schedule)); ok {
		nextRun = next.UTC().Format(time.RFC3339)
	}
	e.mutateSchedule(schedule.ID, func(next *model.Schedule) {
		next.LastScheduledAt = scheduled.UTC().Format(time.RFC3339)
		next.NextRunAt = nextRun
	})
}

func (e *Engine) rearm(now time.Time) []runJob {
	catchUps := make([]runJob, 0)
	for _, schedule := range e.store.Schedules() {
		if !schedule.Enabled || schedule.PausedReason != "" {
			continue
		}
		next, err := time.Parse(time.RFC3339, schedule.NextRunAt)
		if err == nil && next.After(now) {
			continue
		}
		if err == nil && schedule.MisfirePolicy == "run_once" &&
			allowsRunOnce(schedule.Action) && withinGrace(schedule, now, next) {
			catchUps = append(catchUps, runJob{scheduleID: schedule.ID, scheduled: next, enqueuedAt: now, catchUp: true})
		} else if err == nil && !next.IsZero() {
			e.audit(model.SeverityInfo, "schedule:"+schedule.ID, "schedule.skip", schedule.ID,
				"missed_window", map[string]string{"scheduleId": schedule.ID, "reason": "missed_window"})
		}
		probe := schedule
		probe.LastScheduledAt = now.UTC().Format(time.RFC3339)
		nextRun := ""
		if computed, ok := NextRun(probe, now, e.drawJitter(schedule)); ok {
			nextRun = computed.UTC().Format(time.RFC3339)
		}
		e.mutateSchedule(schedule.ID, func(next *model.Schedule) {
			next.NextRunAt = nextRun
			if schedule.Trigger.Kind == "once" && nextRun == "" {
				next.Enabled = false
				next.PausedReason = "missed_window"
			}
		})
	}
	return catchUps
}

func (e *Engine) inCooldown(schedule model.Schedule, now time.Time) bool {
	if schedule.Guard.CooldownSec <= 0 || schedule.LastRunAt == "" {
		return false
	}
	last, err := time.Parse(time.RFC3339, schedule.LastRunAt)
	if err != nil {
		return false
	}
	return now.Sub(last) < time.Duration(schedule.Guard.CooldownSec)*time.Second
}

func (e *Engine) dailyCapReason(schedule model.Schedule, now time.Time) string {
	loc, err := LoadTimezone(schedule.Trigger.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	count := e.store.ScheduleRunCount(schedule.ID, start, start.AddDate(0, 0, 1))
	if count >= schedule.Guard.MaxRunsPerDay {
		return "max_runs_per_day"
	}
	return ""
}

func (e *Engine) runCountToday(schedule model.Schedule, now time.Time) int {
	loc, err := LoadTimezone(schedule.Trigger.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return e.store.ScheduleRunCount(schedule.ID, start, start.AddDate(0, 0, 1))
}

func (e *Engine) acquireAccount(accountID, policy string, ctx context.Context) (func(), bool) {
	acquire := func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		if _, busy := e.accountLease[accountID]; busy {
			return false
		}
		e.accountLease[accountID] = struct{}{}
		return true
	}
	if acquire() {
		return func() {
			e.mu.Lock()
			delete(e.accountLease, accountID)
			e.mu.Unlock()
		}, true
	}
	if policy != "queue" {
		return nil, false
	}
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-timer.C:
			return nil, false
		case <-ticker.C:
			if acquire() {
				return func() {
					e.mu.Lock()
					delete(e.accountLease, accountID)
					e.mu.Unlock()
				}, true
			}
		}
	}
}

func (e *Engine) circuitOpen(accountID string, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	state, ok := e.circuits[accountID]
	if !ok || state.openedAt.IsZero() {
		return false
	}
	if now.Sub(state.openedAt) >= 5*time.Minute {
		delete(e.circuits, accountID)
		return false
	}
	return true
}

func (e *Engine) recordCircuitSuccess(accountID string) {
	e.mu.Lock()
	delete(e.circuits, accountID)
	e.mu.Unlock()
}

func (e *Engine) recordCircuitFailure(accountID string) {
	e.mu.Lock()
	state := e.circuits[accountID]
	state.failures++
	if state.failures >= 3 {
		state.openedAt = e.cfg.Now()
	}
	e.circuits[accountID] = state
	e.mu.Unlock()
}

func (e *Engine) auditSkip(schedule model.Schedule, reason string, now time.Time) {
	if reason == "frozen" || reason == "paused" || reason == "duplicate_fire" || reason == "in_flight" {
		return
	}
	e.mu.Lock()
	last, seen := e.lastSkipAt[schedule.ID]
	if seen && now.Sub(last) < time.Hour {
		e.mu.Unlock()
		return
	}
	e.lastSkipAt[schedule.ID] = now
	e.mu.Unlock()
	e.audit(model.SeverityInfo, "schedule:"+schedule.ID, "schedule.skip", schedule.ID,
		reason, map[string]string{"scheduleId": schedule.ID, "reason": reason, "result": "skipped"})
}

func (e *Engine) auditError(schedule model.Schedule, run model.ScheduleRun, now time.Time) {
	e.mu.Lock()
	last, seen := e.lastErrorAt[schedule.ID]
	if seen && now.Sub(last) < time.Hour {
		e.mu.Unlock()
		return
	}
	e.lastErrorAt[schedule.ID] = now
	e.mu.Unlock()
	e.audit(model.SeverityWarn, "schedule:"+schedule.ID, "schedule.error", schedule.ID,
		run.Reason, map[string]string{
			"scheduleId": schedule.ID,
			"runId":      run.ID,
			"reason":     run.Reason,
			"errorClass": run.ErrorClass,
			"result":     run.Result,
		})
}

func (e *Engine) auditPause(schedule model.Schedule, reason string) {
	e.audit(model.SeverityWarn, "schedule:"+schedule.ID, "schedule.paused", schedule.ID,
		reason, map[string]string{"scheduleId": schedule.ID, "reason": reason})
}

func (e *Engine) drawJitter(schedule model.Schedule) time.Duration {
	limit := schedule.Trigger.JitterSec
	if limit <= 0 {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return Jitter(e.rand, limit)
}

func (e *Engine) randLockedIntn(n int) int {
	if n <= 0 {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rand.IntN(n)
}
