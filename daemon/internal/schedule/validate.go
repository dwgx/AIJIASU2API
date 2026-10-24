package schedule

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// FieldError names the offending request field so the API can return
// details.field without parsing a message.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

func fieldErr(field, format string, args ...any) error {
	return &FieldError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// DefaultGuard is the create default: a first click cannot mutate anything.
func DefaultGuard() model.ScheduleGuard {
	return model.ScheduleGuard{CooldownSec: 600, MaxRunsPerDay: 24, DryRun: true}
}

// ApplyDefaults fills the optional create fields. enabled and dryRun are
// resolved by the caller because they are pointers in the request body.
func ApplyDefaults(s *model.Schedule) {
	if s.MisfirePolicy == "" {
		s.MisfirePolicy = "skip"
	}
	if s.ConcurrencyPolicy == "" {
		s.ConcurrencyPolicy = "forbid"
	}
	if s.Owner == "" {
		s.Owner = "admin"
	}
}

// Validate checks the closed schema of one schedule. It is pure and does not
// touch the store; existence, uniqueness and the 50-schedule cap are store
// concerns. now is the server clock for the first-run bound.
func Validate(s *model.Schedule, now time.Time) error {
	return validate(s, now, true)
}

// ValidatePreview is Validate without the "has a reachable next run" check.
// The preview surfaces an unreachable window as a warning instead of a 400.
func ValidatePreview(s *model.Schedule, now time.Time) error {
	return validate(s, now, false)
}

func validate(s *model.Schedule, now time.Time, requireReachable bool) error {
	switch s.Action {
	case model.ActionAccountRotate, model.ActionAccountReconnect:
	case model.ActionRenewalReminder:
		return fieldErr("action", "%q is system-only", s.Action)
	default:
		return fieldErr("action", "%q is not supported in P0", s.Action)
	}
	if s.Target.Kind != "account" {
		return fieldErr("target.kind", "must be account for %s", s.Action)
	}
	if strings.TrimSpace(s.Target.ID) == "" {
		return fieldErr("target.id", "is required")
	}
	switch s.Action {
	case model.ActionAccountRotate:
		if _, err := DecodeRotateParams(s.Target.Params); err != nil {
			return err
		}
	case model.ActionAccountReconnect:
		if _, err := DecodeReconnectParams(s.Target.Params); err != nil {
			return err
		}
	}
	if err := validateTrigger(s, now); err != nil {
		return err
	}
	if _, err := parseWindow(s.Window); err != nil {
		return &FieldError{Field: "window", Message: err.Error()}
	}
	if s.Guard.CooldownSec < 0 || s.Guard.CooldownSec > 86400 {
		return fieldErr("guard.cooldownSec", "must be between 0 and 86400")
	}
	if s.Guard.MaxRunsPerDay < 1 || s.Guard.MaxRunsPerDay > 1000 {
		return fieldErr("guard.maxRunsPerDay", "must be between 1 and 1000")
	}
	switch s.MisfirePolicy {
	case "skip", "run_once":
	default:
		return fieldErr("misfirePolicy", "must be skip or run_once")
	}
	if s.MisfireGraceSec < 0 || s.MisfireGraceSec > 3600 {
		return fieldErr("misfireGraceSec", "must be between 0 and 3600")
	}
	if s.Trigger.Kind == "once" && s.MisfirePolicy == "run_once" {
		return fieldErr("misfirePolicy", "run_once is not allowed for a once trigger")
	}
	switch s.ConcurrencyPolicy {
	case "forbid", "queue":
	default:
		return fieldErr("concurrencyPolicy", "must be forbid or queue")
	}
	if requireReachable {
		probe := *s
		probe.LastRunAt = ""
		probe.LastScheduledAt = ""
		probe.CreatedAt = ""
		if _, ok := NextRun(probe, now, 0); !ok {
			return fieldErr("trigger", "has no reachable next run")
		}
	}
	return nil
}

func validateTrigger(s *model.Schedule, now time.Time) error {
	trigger := s.Trigger
	loc, err := LoadTimezone(trigger.Timezone)
	if err != nil {
		return fieldErr("trigger.timezone", "%s", err)
	}
	switch trigger.Kind {
	case "cron":
		if strings.TrimSpace(trigger.Expr) == "" {
			return fieldErr("trigger.expr", "is required")
		}
		if trigger.EverySec != 0 || trigger.At != "" {
			return fieldErr("trigger", "cron accepts only expr, timezone and jitterSec")
		}
		if trigger.JitterSec < 0 || trigger.JitterSec > 300 {
			return fieldErr("trigger.jitterSec", "must be between 0 and 300 for cron")
		}
		cron, err := ParseCron(trigger.Expr)
		if err != nil {
			return fieldErr("trigger.expr", "%s", err)
		}
		if _, ok := cron.Next(now, loc); !ok {
			return fieldErr("trigger.expr", "has no occurrence within 366 days")
		}
	case "interval":
		if trigger.EverySec < 60 || trigger.EverySec > 2592000 {
			return fieldErr("trigger.everySec", "must be between 60 and 2592000")
		}
		maxJitter := trigger.EverySec / 2
		if maxJitter > 300 {
			maxJitter = 300
		}
		if trigger.JitterSec < 0 || trigger.JitterSec > maxJitter {
			return fieldErr("trigger.jitterSec", "must be between 0 and %d", maxJitter)
		}
		if trigger.Expr != "" || trigger.At != "" {
			return fieldErr("trigger", "interval accepts only everySec, timezone and jitterSec")
		}
	case "once":
		if trigger.At == "" {
			return fieldErr("trigger.at", "is required")
		}
		if trigger.Expr != "" || trigger.EverySec != 0 || trigger.JitterSec != 0 {
			return fieldErr("trigger", "once accepts only at and timezone")
		}
		at, err := time.Parse(time.RFC3339, trigger.At)
		if err != nil {
			return fieldErr("trigger.at", "must be an RFC 3339 timestamp")
		}
		if !at.After(now) {
			return fieldErr("trigger.at", "must be in the future")
		}
		if at.After(now.Add(366 * 24 * time.Hour)) {
			return fieldErr("trigger.at", "must be at most 366 days away")
		}
	default:
		return fieldErr("trigger.kind", "must be cron, interval or once")
	}
	return nil
}

// DecodeRotateParams strictly decodes account.rotate params. Unknown keys are
// rejected so a typo cannot silently select a different strategy.
func DecodeRotateParams(raw json.RawMessage) (model.RotateParams, error) {
	params := model.RotateParams{Strategy: "same-region", ExcludeCurrent: new(true), Rollback: new(true)}
	if len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "null" {
		if err := strictDecode(raw, &params); err != nil {
			return model.RotateParams{}, fieldErr("target.params", "%s", err)
		}
	}
	if params.Strategy == "" {
		params.Strategy = "same-region"
	}
	switch params.Strategy {
	case "pool":
		if len(params.ServerIDs) < 2 {
			return model.RotateParams{}, fieldErr("target.params.serverIds", "a pool needs at least 2 server ids")
		}
	case "same-region", "next-server", "random":
	default:
		return model.RotateParams{}, fieldErr("target.params.strategy", "must be pool, same-region, next-server or random")
	}
	seen := make(map[string]struct{}, len(params.ServerIDs))
	normalized := make([]string, 0, len(params.ServerIDs))
	for _, raw := range params.ServerIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			return model.RotateParams{}, fieldErr("target.params.serverIds", "must not contain an empty id")
		}
		if _, ok := seen[id]; ok {
			return model.RotateParams{}, fieldErr("target.params.serverIds", "must not contain duplicate %q", id)
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	params.ServerIDs = normalized
	params.Region = strings.TrimSpace(params.Region)
	if params.DrainSeconds < 0 || params.DrainSeconds > 60 {
		return model.RotateParams{}, fieldErr("target.params.drainSeconds", "must be between 0 and 60")
	}
	if params.TimeoutSeconds < 0 || (params.TimeoutSeconds != 0 && (params.TimeoutSeconds < 10 || params.TimeoutSeconds > 120)) {
		return model.RotateParams{}, fieldErr("target.params.timeoutSeconds", "must be between 10 and 120")
	}
	return params, nil
}

// DecodeReconnectParams strictly decodes account.reconnect params.
func DecodeReconnectParams(raw json.RawMessage) (model.ReconnectParams, error) {
	var params model.ReconnectParams
	if len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "null" {
		if err := strictDecode(raw, &params); err != nil {
			return model.ReconnectParams{}, fieldErr("target.params", "%s", err)
		}
	}
	if params.TimeoutSeconds != 0 && (params.TimeoutSeconds < 10 || params.TimeoutSeconds > 120) {
		return model.ReconnectParams{}, fieldErr("target.params.timeoutSeconds", "must be between 10 and 120")
	}
	return params, nil
}

func strictDecode(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("must contain a single JSON value")
	}
	return nil
}

// Preview computes the next occurrences without reading or writing state. It
// is the API's pure preview path.
func Preview(s model.Schedule, now time.Time, count int) ([]model.SchedulePreviewRun, []string, error) {
	if count <= 0 {
		count = 5
	}
	if count > 50 {
		count = 50
	}
	loc, err := LoadTimezone(s.Trigger.Timezone)
	if err != nil {
		return nil, nil, fieldErr("trigger.timezone", "%s", err)
	}
	win, err := parseWindow(s.Window)
	if err != nil {
		return nil, nil, &FieldError{Field: "window", Message: err.Error()}
	}
	runs := make([]model.SchedulePreviewRun, 0, count)
	warnings := make([]string, 0, 2)
	cursor := now
	var cron Cron
	if s.Trigger.Kind == "cron" {
		cron, err = ParseCron(s.Trigger.Expr)
		if err != nil {
			return nil, nil, fieldErr("trigger.expr", "%s", err)
		}
	}
	every := time.Duration(s.Trigger.EverySec) * time.Second
	for len(runs) < count {
		var next time.Time
		var ok bool
		switch s.Trigger.Kind {
		case "cron":
			next, ok = cron.Next(cursor, loc)
			if ok && win != nil {
				for attempts := 0; attempts < 366 && !win.contains(next, loc); attempts++ {
					next, ok = cron.Next(next, loc)
				}
				if ok && !win.contains(next, loc) {
					ok = false
				}
			}
		case "interval":
			if every <= 0 {
				return nil, nil, fieldErr("trigger.everySec", "must be positive")
			}
			next = cursor.Add(every)
			if win != nil {
				next, ok = advanceIntoWindow(next, every, win, loc, now)
			} else {
				ok = true
			}
		case "once":
			if len(runs) > 0 {
				ok = false
				break
			}
			next, err = time.Parse(time.RFC3339, s.Trigger.At)
			ok = err == nil && next.After(now) && (win == nil || win.contains(next, loc))
		default:
			return nil, nil, fieldErr("trigger.kind", "must be cron, interval or once")
		}
		if !ok {
			if len(runs) == 0 {
				warnings = append(warnings, "window_unreachable")
			}
			break
		}
		runs = append(runs, model.SchedulePreviewRun{
			At:       next.In(loc).Format(time.RFC3339),
			UTC:      next.UTC().Format(time.RFC3339),
			InWindow: win == nil || win.contains(next, loc),
		})
		cursor = next
	}
	if s.Action == model.ActionAccountRotate {
		if params, err := DecodeRotateParams(s.Target.Params); err == nil && params.Strategy == "pool" && len(params.ServerIDs) < 2 {
			warnings = append(warnings, "pool_too_small")
		}
	}
	return runs, warnings, nil
}
