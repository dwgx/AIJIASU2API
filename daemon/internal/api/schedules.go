package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/schedule"
	"ajsaccountd/internal/store"
)

// ScheduleService is the engine surface the API uses. Nil means
// AJS_SCHEDULES=0: reads still work, mutations answer 503.
type ScheduleService interface {
	RunNow(ctx context.Context, scheduleID string, dryRun bool, force bool) (model.ScheduleRun, error)
	IsRunning(scheduleID string) bool
	Metrics() schedule.Metrics
}

// CatalogLister is the optional read-only catalog view used to validate a
// rotation pool at create time. It is separate from CatalogConnector so a
// test double that only switches does not have to implement it.
type CatalogLister interface {
	CatalogServers(accountID string) ([]runtime.CatalogServer, bool)
}

func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	schedules := s.store.Schedules()
	for i := range schedules {
		schedules[i] = s.refreshRunCount(schedules[i])
	}
	query := r.URL.Query()
	if accountID := query.Get("accountId"); accountID != "" {
		filtered := schedules[:0]
		for _, scheduleValue := range schedules {
			if scheduleValue.Target.ID == accountID {
				filtered = append(filtered, scheduleValue)
			}
		}
		schedules = filtered
	}
	if raw := query.Get("enabled"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "enabled must be a boolean")
			return
		}
		filtered := schedules[:0]
		for _, scheduleValue := range schedules {
			if scheduleValue.Enabled == enabled {
				filtered = append(filtered, scheduleValue)
			}
		}
		schedules = filtered
	}
	start, end, ok := paginate(w, r, len(schedules))
	if !ok {
		return
	}
	s.setScheduleTime(w)
	writeJSON(w, http.StatusOK, schedules[start:end])
}

func (s *Server) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	schedule, ok := s.store.Schedule(pathParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "schedule not found")
		return
	}
	s.writeSchedule(w, http.StatusOK, s.refreshRunCount(schedule))
}

func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Schedules == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable, "schedules are disabled")
		return
	}
	var input model.ScheduleCreateInput
	if !decodeJSON(w, r, &input) {
		return
	}
	now := time.Now()
	scheduleValue := model.Schedule{
		Name:    input.Name,
		Action:  input.Action,
		Owner:   "admin",
		Enabled: input.Enabled != nil && *input.Enabled,
	}
	if input.Target != nil {
		scheduleValue.Target = *input.Target
	}
	if input.Trigger != nil {
		scheduleValue.Trigger = *input.Trigger
	}
	if input.Window != nil {
		window := *input.Window
		scheduleValue.Window = &window
	}
	scheduleValue.Guard = applyGuardInput(schedule.DefaultGuard(), input.Guard)
	scheduleValue.MisfirePolicy = input.MisfirePolicy
	scheduleValue.MisfireGraceSec = 300
	if input.MisfireGraceSec != nil {
		scheduleValue.MisfireGraceSec = *input.MisfireGraceSec
	}
	scheduleValue.ConcurrencyPolicy = input.ConcurrencyPolicy
	schedule.ApplyDefaults(&scheduleValue)
	if err := s.validateScheduleRequest(scheduleValue); err != nil {
		s.writeScheduleRequestError(w, err)
		return
	}
	if destructiveAction(scheduleValue.Action) && !scheduleValue.Guard.DryRun && input.Confirm != "ARM" {
		writeScheduleReason(w, http.StatusConflict, "CONFIRMATION_REQUIRED", "confirmation_required",
			`confirm:"ARM" is required to arm a destructive action`, nil)
		return
	}
	if scheduleValue.Enabled {
		if next, ok := schedule.NextRun(scheduleValue, now, 0); ok {
			scheduleValue.NextRunAt = next.UTC().Format(time.RFC3339)
		}
	}
	created, err := s.store.CreateSchedule(scheduleValue, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.Header().Set("Location", "/v1/schedules/"+created.ID)
	s.writeSchedule(w, http.StatusCreated, created)
}

func (s *Server) handlePatchSchedule(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Schedules == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable, "schedules are disabled")
		return
	}
	current, ok := s.store.Schedule(pathParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "schedule not found")
		return
	}
	expectedRevision, ok := parseScheduleIfMatch(r.Header.Get("If-Match"))
	if !ok {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, `If-Match "id:revision" is required`)
		return
	}
	var input model.SchedulePatchInput
	if !decodeJSON(w, r, &input) {
		return
	}
	next := current
	triggerChanged := false
	if input.Name != nil {
		next.Name = *input.Name
	}
	if input.Enabled != nil && *input.Enabled != next.Enabled {
		next.Enabled = *input.Enabled
		triggerChanged = true
		if *input.Enabled {
			next.PausedReason = ""
			next.ConsecutiveFailures = 0
			next.Degraded = false
		}
	}
	if input.Action != nil {
		next.Action = *input.Action
		triggerChanged = true
	}
	if input.Target != nil {
		next.Target = *input.Target
		triggerChanged = true
	}
	if input.Trigger != nil {
		next.Trigger = *input.Trigger
		triggerChanged = true
	}
	if input.Window != nil {
		window := *input.Window
		next.Window = &window
		triggerChanged = true
	}
	if input.Guard != nil {
		wasDryRun := next.Guard.DryRun
		next.Guard = applyGuardInput(next.Guard, input.Guard)
		if wasDryRun && !next.Guard.DryRun && destructiveAction(next.Action) && input.Confirm != "ARM" {
			writeScheduleReason(w, http.StatusConflict, "CONFIRMATION_REQUIRED", "confirmation_required",
				`confirm:"ARM" is required to arm a destructive action`, nil)
			return
		}
	}
	if input.Action != nil && destructiveAction(next.Action) && !next.Guard.DryRun && input.Confirm != "ARM" {
		writeScheduleReason(w, http.StatusConflict, "CONFIRMATION_REQUIRED", "confirmation_required",
			`confirm:"ARM" is required to arm a destructive action`, nil)
		return
	}
	if input.MisfirePolicy != nil {
		next.MisfirePolicy = *input.MisfirePolicy
	}
	if input.MisfireGraceSec != nil {
		next.MisfireGraceSec = *input.MisfireGraceSec
	}
	if input.ConcurrencyPolicy != nil {
		next.ConcurrencyPolicy = *input.ConcurrencyPolicy
	}
	schedule.ApplyDefaults(&next)
	if err := s.validateScheduleRequest(next); err != nil {
		s.writeScheduleRequestError(w, err)
		return
	}
	if input.Enabled != nil && !*input.Enabled {
		// An explicit disable clears the slot. A paused schedule keeps its
		// computed slot for display; only the engine skips it.
		next.NextRunAt = ""
	} else if next.Enabled || (triggerChanged && next.PausedReason != "") {
		if !current.Enabled || triggerChanged || next.NextRunAt == "" {
			if at, ok := schedule.NextRun(next, time.Now(), 0); ok {
				next.NextRunAt = at.UTC().Format(time.RFC3339)
			}
		}
	}
	updated, err := s.store.UpdateSchedule(next, expectedRevision, s.auditActor(r))
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeScheduleReason(w, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "stale_revision",
				err.Error(), map[string]string{"currentRevision": strconv.FormatInt(current.Revision, 10)})
			return
		}
		s.writeStoreError(w, err)
		return
	}
	s.writeSchedule(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Schedules == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable, "schedules are disabled")
		return
	}
	id := pathParam(r, "id")
	if s.cfg.Schedules.IsRunning(id) {
		writeScheduleReason(w, http.StatusConflict, CodeConflict, "in_flight",
			"a run is in flight for this schedule", nil)
		return
	}
	if err := s.store.DeleteSchedule(id, s.auditActor(r)); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRunSchedule(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Schedules == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable, "schedules are disabled")
		return
	}
	scheduleValue, ok := s.store.Schedule(pathParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "schedule not found")
		return
	}
	var input model.ScheduleRunInput
	if r.Body != nil && r.Body != http.NoBody {
		if !decodeJSON(w, r, &input) {
			return
		}
	}
	dryRun := scheduleValue.Guard.DryRun
	if input.DryRun != nil {
		dryRun = *input.DryRun
	}
	if !dryRun && destructiveAction(scheduleValue.Action) && input.Confirm != "RUN" {
		writeScheduleReason(w, http.StatusConflict, "CONFIRMATION_REQUIRED", "confirmation_required",
			`confirm:"RUN" is required for a manual destructive run`, nil)
		return
	}
	run, err := s.cfg.Schedules.RunNow(r.Context(), scheduleValue.ID, dryRun, input.Force)
	switch {
	case errors.Is(err, schedule.ErrSchedulesDisabled):
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable, "schedules are disabled")
		return
	case errors.Is(err, schedule.ErrRunInFlight):
		writeScheduleReason(w, http.StatusConflict, CodeConflict, "in_flight",
			"a run is already in flight for this schedule", nil)
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, "schedule not found")
		return
	case err != nil:
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

func (s *Server) handleScheduleRuns(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if _, ok := s.store.Schedule(id); !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "schedule not found")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "limit must be a non-negative integer")
			return
		}
		limit = parsed
	}
	if limit > 200 {
		limit = 200
	}
	s.setScheduleTime(w)
	writeJSON(w, http.StatusOK, s.store.ScheduleRuns(id, limit))
}

func (s *Server) handleSchedulePreview(w http.ResponseWriter, r *http.Request) {
	input := model.SchedulePreviewInput{Action: model.ActionAccountReconnect}
	if r.Method == http.MethodPost {
		if !decodeJSON(w, r, &input) {
			return
		}
	} else {
		query := r.URL.Query()
		trigger := model.ScheduleTrigger{
			Kind:     firstNonEmpty(query.Get("kind"), query.Get("trigger")),
			Expr:     query.Get("expr"),
			Timezone: query.Get("timezone"),
		}
		if raw := query.Get("everySec"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, CodeInvalidArgument, "everySec must be an integer")
				return
			}
			trigger.EverySec = parsed
		}
		if raw := query.Get("jitterSec"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, CodeInvalidArgument, "jitterSec must be an integer")
				return
			}
			trigger.JitterSec = parsed
		}
		trigger.At = query.Get("at")
		input.Trigger = &trigger
		if action := query.Get("action"); action != "" {
			input.Action = model.ScheduleAction(action)
		}
		if days := query.Get("days"); days != "" {
			window := &model.ScheduleWindow{Start: query.Get("start"), End: query.Get("end")}
			for _, part := range strings.Split(days, ",") {
				parsed, err := strconv.Atoi(strings.TrimSpace(part))
				if err != nil {
					writeError(w, http.StatusBadRequest, CodeInvalidArgument, "days must be comma-separated integers")
					return
				}
				window.Days = append(window.Days, parsed)
			}
			input.Window = window
		}
	}
	if input.Trigger == nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "trigger is required")
		return
	}
	preview := model.Schedule{
		Action:  input.Action,
		Target:  model.ScheduleTarget{Kind: "account", ID: "preview"},
		Trigger: *input.Trigger,
		Window:  input.Window,
		Guard:   schedule.DefaultGuard(),
	}
	schedule.ApplyDefaults(&preview)
	if preview.Action == "" {
		preview.Action = model.ActionAccountReconnect
	}
	if err := schedule.ValidatePreview(&preview, time.Now()); err != nil {
		s.writeScheduleRequestError(w, err)
		return
	}
	count := 5
	if raw := r.URL.Query().Get("count"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "count must be a positive integer")
			return
		}
		count = parsed
	}
	runs, warnings, err := schedule.Preview(preview, time.Now(), count)
	if err != nil {
		s.writeScheduleRequestError(w, err)
		return
	}
	s.setScheduleTime(w)
	writeJSON(w, http.StatusOK, model.SchedulePreview{
		ServerTime: time.Now().UTC().Format(time.RFC3339Nano),
		Runs:       runs,
		Warnings:   warnings,
	})
}

// validateScheduleRequest runs the pure schema checks and the store/catalog
// existence checks a create or PATCH needs.
func (s *Server) validateScheduleRequest(scheduleValue model.Schedule) error {
	if err := schedule.Validate(&scheduleValue, time.Now()); err != nil {
		return err
	}
	if _, ok := s.store.Account(scheduleValue.Target.ID); !ok {
		return &store.Error{Kind: store.ErrNotFound, Message: fmt.Sprintf("account %q not found", scheduleValue.Target.ID)}
	}
	if scheduleValue.Action != model.ActionAccountRotate {
		return nil
	}
	if s.cfg.Catalog == nil {
		return &store.Error{Kind: store.ErrConflict, Message: "no retained login result; log in again"}
	}
	params, err := schedule.DecodeRotateParams(scheduleValue.Target.Params)
	if err != nil {
		return err
	}
	lister, ok := s.cfg.Catalog.(CatalogLister)
	if !ok {
		return nil
	}
	servers, loginAvailable := lister.CatalogServers(scheduleValue.Target.ID)
	if !loginAvailable {
		return &store.Error{Kind: store.ErrConflict, Message: "no retained login result; log in again"}
	}
	eligible := make(map[string]bool, len(servers))
	for _, server := range servers {
		eligible[server.ID] = server.Eligible
	}
	for _, id := range params.ServerIDs {
		ok, found := eligible[id]
		if !found {
			return &schedule.FieldError{Field: "target.params.serverIds",
				Message: fmt.Sprintf("server %q is not in the retained catalog", id)}
		}
		if !ok {
			return &schedule.FieldError{Field: "target.params.serverIds",
				Message: fmt.Sprintf("server %q is not eligible", id)}
		}
	}
	return nil
}

// refreshRunCount derives runCountToday from persisted runs in the trigger
// timezone's calendar day, so a midnight rollover or restart cannot leave a
// stale counter in the read paths.
func (s *Server) refreshRunCount(scheduleValue model.Schedule) model.Schedule {
	loc, err := schedule.LoadTimezone(scheduleValue.Trigger.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	scheduleValue.RunCountToday = s.store.ScheduleRunCount(scheduleValue.ID, start, start.AddDate(0, 0, 1))
	return scheduleValue
}

func (s *Server) writeSchedule(w http.ResponseWriter, status int, scheduleValue model.Schedule) {
	w.Header().Set("ETag", scheduleETag(scheduleValue))
	s.setScheduleTime(w)
	writeJSON(w, status, scheduleValue)
}

func (s *Server) setScheduleTime(w http.ResponseWriter) {
	w.Header().Set("X-APC-Server-Time", time.Now().UTC().Format(time.RFC3339Nano))
}

func (s *Server) writeScheduleRequestError(w http.ResponseWriter, err error) {
	var fieldErr *schedule.FieldError
	if errors.As(err, &fieldErr) {
		writeScheduleReason(w, http.StatusBadRequest, CodeInvalidArgument, "invalid_argument",
			fieldErr.Error(), map[string]string{"field": fieldErr.Field})
		return
	}
	s.writeStoreError(w, err)
}

func writeScheduleReason(w http.ResponseWriter, status int, code, reason, message string, details map[string]string) {
	writeJSON(w, status, errorResponse{Error: errorDetail{
		Code: code, Message: message, Reason: reason, Details: details,
	}})
}

func scheduleETag(scheduleValue model.Schedule) string {
	return fmt.Sprintf("%q", fmt.Sprintf("%s:%d", scheduleValue.ID, scheduleValue.Revision))
}

func parseScheduleIfMatch(header string) (int64, bool) {
	header = strings.TrimSpace(header)
	header = strings.TrimPrefix(header, "W/")
	header = strings.Trim(header, `"`)
	if header == "" {
		return 0, false
	}
	index := strings.LastIndexByte(header, ':')
	if index < 0 {
		return 0, false
	}
	revision, err := strconv.ParseInt(header[index+1:], 10, 64)
	if err != nil {
		return 0, false
	}
	return revision, true
}

func destructiveAction(action model.ScheduleAction) bool {
	switch action {
	case model.ActionAccountSwitch, model.ActionAccountRotate, model.ActionAccountReconnect,
		model.ActionAccountLogin, model.ActionAccountLogout, model.ActionNodeDrain,
		model.ActionLeaseRotate:
		return true
	default:
		return false
	}
}

// applyGuardInput overlays the fields the client sent onto base. An omitted
// field keeps the default or current value.
func applyGuardInput(base model.ScheduleGuard, input *model.ScheduleGuardInput) model.ScheduleGuard {
	if input == nil {
		return base
	}
	if input.CooldownSec != nil {
		base.CooldownSec = *input.CooldownSec
	}
	if input.MaxRunsPerDay != nil {
		base.MaxRunsPerDay = *input.MaxRunsPerDay
	}
	if input.DryRun != nil {
		base.DryRun = *input.DryRun
	}
	return base
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
