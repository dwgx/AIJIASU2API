// Package schedule implements the server-side job scheduler: cron parsing,
// next-run calculation, the durable fire journal and the execution engine.
//
// It is deliberately a policy layer. It never opens a session, closes a gate
// or writes catalog state directly; disruptive actions call the existing
// runtime.NodeController, which owns the one-session guard.
package schedule

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// cron aliases accepted in place of a five-field expression.
var cronAliases = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

// Cron is a parsed five-field cron expression. It is immutable after ParseCron
// and safe for concurrent use.
type Cron struct {
	minutes [60]bool
	hours   [24]bool
	dom     [32]bool
	months  [13]bool
	dow     [7]bool

	domRestricted bool
	dowRestricted bool
}

// ParseCron parses a standard five-field expression (minute hour day-of-month
// month day-of-week). Seconds, @reboot and the non-portable L/W/# extensions
// are rejected; the caller surfaces the error as a 400.
func ParseCron(expr string) (Cron, error) {
	var cron Cron
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return cron, errors.New("cron expression is required")
	}
	if alias, ok := cronAliases[strings.ToLower(expr)]; ok {
		expr = alias
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return cron, fmt.Errorf("cron expression must have 5 fields, got %d", len(fields))
	}
	for _, field := range fields {
		if strings.ContainsAny(field, "?LW#") {
			return cron, fmt.Errorf("cron field %q uses an unsupported extension", field)
		}
	}
	if err := parseCronField(fields[0], 0, 59, func(v int) { cron.minutes[v] = true }); err != nil {
		return cron, fmt.Errorf("minute: %w", err)
	}
	if err := parseCronField(fields[1], 0, 23, func(v int) { cron.hours[v] = true }); err != nil {
		return cron, fmt.Errorf("hour: %w", err)
	}
	if err := parseCronField(fields[2], 1, 31, func(v int) { cron.dom[v] = true }); err != nil {
		return cron, fmt.Errorf("day-of-month: %w", err)
	}
	if err := parseCronField(fields[3], 1, 12, func(v int) { cron.months[v] = true }); err != nil {
		return cron, fmt.Errorf("month: %w", err)
	}
	if err := parseCronField(fields[4], 0, 7, func(v int) {
		if v == 7 {
			v = 0
		}
		cron.dow[v] = true
	}); err != nil {
		return cron, fmt.Errorf("day-of-week: %w", err)
	}
	cron.domRestricted = fields[2] != "*"
	cron.dowRestricted = fields[4] != "*"
	return cron, nil
}

func parseCronField(field string, min, max int, set func(int)) error {
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return fmt.Errorf("empty list item in %q", field)
		}
		step := 1
		rangePart := part
		if slash := strings.IndexByte(part, '/'); slash >= 0 {
			rangePart = part[:slash]
			parsed, err := strconv.Atoi(part[slash+1:])
			if err != nil || parsed < 1 {
				return fmt.Errorf("invalid step in %q", part)
			}
			step = parsed
		}
		start, end := min, max
		switch {
		case rangePart == "*":
			// full range
		case strings.Contains(rangePart, "-"):
			bounds := strings.SplitN(rangePart, "-", 2)
			parsedStart, err := strconv.Atoi(bounds[0])
			if err != nil {
				return fmt.Errorf("invalid range start in %q", part)
			}
			parsedEnd, err := strconv.Atoi(bounds[1])
			if err != nil {
				return fmt.Errorf("invalid range end in %q", part)
			}
			start, end = parsedStart, parsedEnd
		default:
			parsed, err := strconv.Atoi(rangePart)
			if err != nil {
				return fmt.Errorf("invalid value %q", part)
			}
			start, end = parsed, parsed
			if strings.Contains(part, "/") {
				end = max
			}
		}
		if start < min || end > max || start > end {
			return fmt.Errorf("value %q is outside %d-%d", part, min, max)
		}
		for value := start; value <= end; value += step {
			set(value)
		}
	}
	return nil
}

// Next returns the first matching instant strictly after `after` in loc. It
// walks calendar days (max 366) and skips a candidate whose wall clock does not
// match because it fell in a spring-forward gap. An ambiguous fall-back time
// returns the earlier instant.
func (c Cron) Next(after time.Time, loc *time.Location) (time.Time, bool) {
	if loc == nil {
		loc = time.UTC
	}
	start := after.In(loc)
	dayStart := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	for offset := 0; offset <= 366; offset++ {
		day := dayStart.AddDate(0, 0, offset)
		year, month, dayOfMonth := day.Date()
		if !c.months[int(month)] {
			continue
		}
		if !c.dayMatches(dayOfMonth, day.Weekday()) {
			continue
		}
		firstHour, firstMinute := 0, 0
		if offset == 0 {
			firstHour, firstMinute = start.Hour(), start.Minute()+1
			if firstMinute == 60 {
				firstMinute = 0
				firstHour++
			}
		}
		for hour := firstHour; hour < 24; hour++ {
			if !c.hours[hour] {
				continue
			}
			for minute := 0; minute < 60; minute++ {
				if hour == firstHour && minute < firstMinute {
					continue
				}
				if !c.minutes[minute] {
					continue
				}
				candidate := time.Date(year, month, dayOfMonth, hour, minute, 0, 0, loc)
				if candidate.Day() != dayOfMonth || int(candidate.Month()) != int(month) ||
					candidate.Hour() != hour || candidate.Minute() != minute {
					// Spring-forward gap: the wall clock does not exist.
					continue
				}
				if earlier := candidate.Add(-time.Hour); earlier.After(after) &&
					earlier.Hour() == hour && earlier.Minute() == minute &&
					earlier.Day() == dayOfMonth && int(earlier.Month()) == int(month) {
					candidate = earlier
				}
				if !candidate.After(after) {
					continue
				}
				return candidate, true
			}
		}
	}
	return time.Time{}, false
}

func (c Cron) dayMatches(dayOfMonth int, weekday time.Weekday) bool {
	domMatch := c.dom[dayOfMonth]
	dowMatch := c.dow[int(weekday)]
	switch {
	case c.domRestricted && c.dowRestricted:
		// Vixie cron: when both are restricted a day matches if either does.
		return domMatch || dowMatch
	case c.domRestricted:
		return domMatch
	case c.dowRestricted:
		return dowMatch
	default:
		return true
	}
}

// LoadTimezone resolves an IANA name. Empty selects UTC; "Local" and fixed
// offsets are rejected because a schedule must mean the same clock after a
// restart on another host.
func LoadTimezone(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return time.UTC, nil
	}
	if strings.EqualFold(name, "local") {
		return nil, errors.New("timezone must be an IANA name, not Local")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("timezone %q is not a valid IANA name", name)
	}
	if loc == nil {
		return nil, fmt.Errorf("timezone %q is not a valid IANA name", name)
	}
	return loc, nil
}

// window is the parsed ScheduleWindow.
type window struct {
	days  [7]bool
	any   bool
	start int // minutes since midnight, inclusive
	end   int // minutes since midnight, exclusive
}

func parseWindow(w *model.ScheduleWindow) (*window, error) {
	if w == nil {
		return nil, nil
	}
	parsed := &window{}
	if len(w.Days) == 0 {
		parsed.any = true
	}
	for _, day := range w.Days {
		if day < 0 || day > 6 {
			return nil, fmt.Errorf("window.days must be 0-6")
		}
		if parsed.days[day] {
			return nil, fmt.Errorf("window.days contains duplicate %d", day)
		}
		parsed.days[day] = true
	}
	var err error
	parsed.start, err = parseClock(w.Start)
	if err != nil {
		return nil, fmt.Errorf("window.start: %w", err)
	}
	parsed.end, err = parseClock(w.End)
	if err != nil {
		return nil, fmt.Errorf("window.end: %w", err)
	}
	if parsed.start == parsed.end {
		return nil, errors.New("window.start and window.end must differ")
	}
	return parsed, nil
}

func parseClock(value string) (int, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, fmt.Errorf("%q must be HH:MM", value)
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, fmt.Errorf("%q must be HH:MM", value)
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, fmt.Errorf("%q must be HH:MM", value)
	}
	return hour*60 + minute, nil
}

// contains reports whether t (interpreted in loc) is inside the window. A
// cross-midnight window belongs to its start day, so Monday 22:00-06:00
// includes Tuesday 02:00 but excludes Tuesday 22:00.
func (w *window) contains(t time.Time, loc *time.Location) bool {
	if w == nil {
		return true
	}
	local := t.In(loc)
	minutes := local.Hour()*60 + local.Minute()
	day := int(local.Weekday())
	if w.start < w.end {
		return w.dayOK(day) && minutes >= w.start && minutes < w.end
	}
	if w.dayOK(day) && minutes >= w.start {
		return true
	}
	yesterday := local.AddDate(0, 0, -1)
	return w.dayOK(int(yesterday.Weekday())) && minutes < w.end
}

func (w *window) dayOK(day int) bool {
	return w.any || w.days[day]
}

// NextRun computes the next occurrence for an armed schedule. jitter is the
// already-drawn jitter for this occurrence; persisting it inside NextRunAt is
// what makes a crash unable to move the slot.
func NextRun(s model.Schedule, now time.Time, jitter time.Duration) (time.Time, bool) {
	loc, err := LoadTimezone(s.Trigger.Timezone)
	if err != nil {
		loc = time.UTC
	}
	win, err := parseWindow(s.Window)
	if err != nil {
		return time.Time{}, false
	}
	switch s.Trigger.Kind {
	case "once":
		if s.LastRunAt != "" || s.LastScheduledAt != "" {
			return time.Time{}, false
		}
		at, err := time.Parse(time.RFC3339, s.Trigger.At)
		if err != nil {
			return time.Time{}, false
		}
		if !at.After(now) {
			return time.Time{}, false
		}
		if !win.contains(at, loc) {
			return time.Time{}, false
		}
		return at, true
	case "interval":
		every := time.Duration(s.Trigger.EverySec) * time.Second
		if every <= 0 {
			return time.Time{}, false
		}
		base := now
		if parsed, err := time.Parse(time.RFC3339, s.LastScheduledAt); err == nil {
			base = parsed
		} else if parsed, err := time.Parse(time.RFC3339, s.LastRunAt); err == nil {
			base = parsed
		} else if parsed, err := time.Parse(time.RFC3339, s.CreatedAt); err == nil {
			base = parsed
		}
		candidate := base.Add(every + jitter)
		if !candidate.After(now) {
			if s.MisfirePolicy == "run_once" && withinGrace(s, now, candidate) {
				return now.Add(jitter), true
			}
			candidate = now.Add(every + jitter)
		}
		return advanceIntoWindow(candidate, every+jitter, win, loc, now)
	case "cron":
		cron, err := ParseCron(s.Trigger.Expr)
		if err != nil {
			return time.Time{}, false
		}
		cursor := now
		for attempt := 0; attempt <= 366; attempt++ {
			candidate, ok := cron.Next(cursor, loc)
			if !ok {
				return time.Time{}, false
			}
			jittered := candidate.Add(jitter)
			if win.contains(jittered, loc) {
				return jittered, true
			}
			cursor = candidate
		}
		return time.Time{}, false
	default:
		return time.Time{}, false
	}
}

// NextAfter computes the next run after a slot was processed, without
// replaying missed occurrences. scheduled is the slot that just ran or was
// skipped.
func NextAfter(s model.Schedule, scheduled, now time.Time, jitter time.Duration) (time.Time, bool) {
	if s.Trigger.Kind == "once" {
		return time.Time{}, false
	}
	if s.Trigger.Kind == "interval" {
		every := time.Duration(s.Trigger.EverySec) * time.Second
		loc, err := LoadTimezone(s.Trigger.Timezone)
		if err != nil {
			loc = time.UTC
		}
		win, err := parseWindow(s.Window)
		if err != nil {
			return time.Time{}, false
		}
		candidate := scheduled.Add(every + jitter)
		if !candidate.After(now) {
			if s.MisfirePolicy == "run_once" && withinGrace(s, now, candidate) {
				return now.Add(jitter), true
			}
			candidate = now.Add(every + jitter)
		}
		return advanceIntoWindow(candidate, every+jitter, win, loc, now)
	}
	return NextRun(s, now, jitter)
}

func withinGrace(s model.Schedule, now, missed time.Time) bool {
	if s.MisfireGraceSec <= 0 {
		return false
	}
	grace := time.Duration(s.MisfireGraceSec) * time.Second
	return now.Sub(missed) >= 0 && now.Sub(missed) <= grace
}

func advanceIntoWindow(candidate time.Time, step time.Duration, win *window, loc *time.Location, now time.Time) (time.Time, bool) {
	if win == nil {
		return candidate, true
	}
	for attempt := 0; attempt <= 366*24*60; attempt++ {
		if win.contains(candidate, loc) {
			return candidate, true
		}
		candidate = candidate.Add(step)
	}
	return time.Time{}, false
}

// Jitter returns a uniform duration in [0, maxSec] seconds.
func Jitter(r interface{ IntN(int) int }, maxSec int) time.Duration {
	if maxSec <= 0 || r == nil {
		return 0
	}
	return time.Duration(r.IntN(maxSec+1)) * time.Second
}
