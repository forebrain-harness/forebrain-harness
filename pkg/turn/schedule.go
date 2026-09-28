package turn

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	cronv3 "github.com/robfig/cron/v3"
)

// ScheduleKind separates the two schedule shapes that behave differently once they
// fire: a one-shot is spent, everything else is rescheduled.
type ScheduleKind string

const (
	// ScheduleScheduleOnce fires a single time: "in 30m", or an absolute timestamp.
	ScheduleScheduleOnce ScheduleKind = "once"
	// ScheduleScheduleEvery fires on a fixed interval: "every 2h", or a bare "30m".
	ScheduleScheduleEvery ScheduleKind = "every"
	// ScheduleScheduleCron fires on a calendar expression: "0 9 * * *", "daily at 7am".
	ScheduleScheduleCron ScheduleKind = "cron"
)

// Schedule is a parsed schedule expression. Keeping the original text lets a
// surface show the user what they typed rather than a normalised rewrite of it.
type Schedule struct {
	Raw          string
	ScheduleKind ScheduleKind
	Interval     time.Duration
	At           time.Time
	Cron         cronv3.Schedule
}

// DefaultRepeatLimit is how many times a schedule of this shape may fire when
// the user does not say. A one-shot is spent after one fire; the recurring
// shapes run until they are paused or removed.
func (s Schedule) DefaultRepeatLimit() int {
	if s.ScheduleKind == ScheduleScheduleOnce {
		return 1
	}
	return 0
}

// Next returns when this schedule fires after the given moment, and whether it
// fires again at all. A one-shot whose moment has passed is spent.
func (s Schedule) Next(after time.Time) (time.Time, bool) {
	switch s.ScheduleKind {
	case ScheduleScheduleOnce:
		if s.At.After(after) {
			return s.At, true
		}
		return time.Time{}, false
	case ScheduleScheduleEvery:
		if s.Interval <= 0 {
			return time.Time{}, false
		}
		return after.Add(s.Interval), true
	case ScheduleScheduleCron:
		if s.Cron == nil {
			return time.Time{}, false
		}
		next := s.Cron.Next(after)
		if next.IsZero() {
			return time.Time{}, false
		}
		return next, true
	default:
		return time.Time{}, false
	}
}

// MinScheduleInterval is the floor for a recurring schedule. A job that fires faster
// than this is a busy loop against a model provider, not a schedule.
const MinScheduleInterval = time.Minute

var cronParser = cronv3.NewParser(
	cronv3.Minute | cronv3.Hour | cronv3.Dom | cronv3.Month | cronv3.Dow | cronv3.Descriptor,
)

// Parse reads a schedule expression. The accepted forms are, in order of the
// check below:
//
//	"in 30m", "in 2h"          — one-shot, relative to now
//	"2026-03-15T09:00:00"      — one-shot, absolute (RFC3339 or local ISO)
//	"every 30m", "every 2h"    — fixed interval
//	"30m"                      — bare duration, same as "every 30m"
//	"daily at 7am"             — every day at that time
//	"weekdays at 9am"          — Monday to Friday at that time
//	"every monday 9am"         — that weekday at that time
//	"0 9 * * *", "@daily"      — cron expression or descriptor
//
// now is the reference for relative forms; pass the caller's clock so the
// result is testable.
func ParseSchedule(expr string, now time.Time) (Schedule, error) {
	raw := strings.TrimSpace(expr)
	if raw == "" {
		return Schedule{}, fmt.Errorf("schedule is empty")
	}
	lower := strings.ToLower(raw)

	if rest, ok := strings.CutPrefix(lower, "in "); ok {
		d, err := parseDuration(rest)
		if err != nil {
			return Schedule{}, err
		}
		if d <= 0 {
			return Schedule{}, fmt.Errorf("schedule %q must be in the future", raw)
		}
		return Schedule{Raw: raw, ScheduleKind: ScheduleScheduleOnce, At: now.Add(d)}, nil
	}

	if at, ok := parseTimestamp(raw, now); ok {
		return Schedule{Raw: raw, ScheduleKind: ScheduleScheduleOnce, At: at}, nil
	}

	// "every <weekday> <time>" and "every day at 9am" are calendar rules, not
	// intervals, so they are tried before the interval reading of "every".
	if sched, ok, err := parseCalendar(lower); ok || err != nil {
		if err != nil {
			return Schedule{}, err
		}
		sched.Raw = raw
		return sched, nil
	}

	interval := lower
	if rest, ok := strings.CutPrefix(lower, "every "); ok {
		interval = rest
	}
	if d, err := parseDuration(interval); err == nil {
		if d < MinScheduleInterval {
			return Schedule{}, fmt.Errorf("schedule %q is faster than the %s minimum", raw, MinScheduleInterval)
		}
		return Schedule{Raw: raw, ScheduleKind: ScheduleScheduleEvery, Interval: d}, nil
	}

	if c, err := cronParser.Parse(raw); err == nil {
		return Schedule{Raw: raw, ScheduleKind: ScheduleScheduleCron, Cron: c}, nil
	}
	return Schedule{}, fmt.Errorf("unrecognised schedule %q", raw)
}

// parseDuration accepts Go's own syntax plus the bare day unit people expect
// from a scheduler ("1d", "7d"), which time.ParseDuration does not know.
func parseDuration(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if rest, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.ParseFloat(strings.TrimSpace(rest), 64); err == nil {
			return time.Duration(n * float64(24*time.Hour)), nil
		}
	}
	d, err := time.ParseDuration(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		return 0, fmt.Errorf("unrecognised duration %q", raw)
	}
	return d, nil
}

func parseTimestamp(raw string, now time.Time) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, raw, now.Location()); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

var weekdayNames = map[string]int{
	"sunday": 0, "sun": 0,
	"monday": 1, "mon": 1,
	"tuesday": 2, "tue": 2, "tues": 2,
	"wednesday": 3, "wed": 3,
	"thursday": 4, "thu": 4, "thurs": 4,
	"friday": 5, "fri": 5,
	"saturday": 6, "sat": 6,
}

// parseCalendar reads the plain-language forms and compiles them to a cron
// expression, so one evaluator decides every calendar fire. It reports ok=false
// when the text is not a calendar rule at all, leaving the other readings to
// try; an error means it was one and could not be understood.
func parseCalendar(lower string) (Schedule, bool, error) {
	words := strings.Fields(strings.ReplaceAll(lower, ",", " "))
	if len(words) == 0 {
		return Schedule{}, false, nil
	}
	if words[0] == "every" {
		words = words[1:]
	}
	if len(words) == 0 {
		return Schedule{}, false, nil
	}

	dow := "*"
	switch words[0] {
	case "day", "daily":
		words = words[1:]
	case "weekday", "weekdays":
		dow = "1-5"
		words = words[1:]
	case "weekend", "weekends":
		dow = "0,6"
		words = words[1:]
	default:
		if n, ok := weekdayNames[words[0]]; ok {
			dow = strconv.Itoa(n)
			words = words[1:]
		} else {
			return Schedule{}, false, nil
		}
	}

	if len(words) > 0 && words[0] == "at" {
		words = words[1:]
	}
	clock := "00:00"
	if len(words) > 0 {
		clock = words[0]
	}
	hour, minute, err := parseClock(clock)
	if err != nil {
		return Schedule{}, true, err
	}
	expr := fmt.Sprintf("%d %d * * %s", minute, hour, dow)
	c, perr := cronParser.Parse(expr)
	if perr != nil {
		return Schedule{}, true, fmt.Errorf("unrecognised schedule time %q", clock)
	}
	return Schedule{ScheduleKind: ScheduleScheduleCron, Cron: c}, true, nil
}

// parseClock reads "9am", "9:30pm", "07:00" and "7".
func parseClock(raw string) (hour, minute int, err error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, 0, fmt.Errorf("empty time")
	}
	pm := false
	switch {
	case strings.HasSuffix(s, "am"):
		s = strings.TrimSuffix(s, "am")
	case strings.HasSuffix(s, "pm"):
		s = strings.TrimSuffix(s, "pm")
		pm = true
	}
	s = strings.TrimSpace(s)
	hourPart, minutePart, hasMinute := strings.Cut(s, ":")
	hour, err = strconv.Atoi(strings.TrimSpace(hourPart))
	if err != nil {
		return 0, 0, fmt.Errorf("unrecognised time %q", raw)
	}
	if hasMinute {
		minute, err = strconv.Atoi(strings.TrimSpace(minutePart))
		if err != nil {
			return 0, 0, fmt.Errorf("unrecognised time %q", raw)
		}
	}
	if pm && hour < 12 {
		hour += 12
	}
	if !pm && hour == 12 && strings.HasSuffix(strings.ToLower(strings.TrimSpace(raw)), "am") {
		hour = 0
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("time out of range %q", raw)
	}
	return hour, minute, nil
}
