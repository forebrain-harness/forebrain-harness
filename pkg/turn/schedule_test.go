package turn

import (
	"testing"
	"time"
)

func mustParseSchedule(t *testing.T, expr string, now time.Time) Schedule {
	t.Helper()
	s, err := ParseSchedule(expr, now)
	if err != nil {
		t.Fatalf("ParseSchedule(%q): %v", expr, err)
	}
	return s
}

// The shapes a user actually types, and what each one means once it fires.
func TestParseAcceptsTheDocumentedScheduleShapes(t *testing.T) {
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	once := mustParseSchedule(t, "in 30m", now)
	if once.ScheduleKind != ScheduleScheduleOnce || !once.At.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("in 30m = %#v", once)
	}
	if once.DefaultRepeatLimit() != 1 {
		t.Fatal("a one-shot must be spent after one fire")
	}
	if _, ok := once.Next(once.At); ok {
		t.Fatal("a one-shot must not fire again once its moment has passed")
	}

	abs := mustParseSchedule(t, "2026-03-15T09:00:00", now)
	if abs.ScheduleKind != ScheduleScheduleOnce || abs.At.Hour() != 9 {
		t.Fatalf("absolute timestamp = %#v", abs)
	}

	every := mustParseSchedule(t, "every 2h", now)
	if every.ScheduleKind != ScheduleScheduleEvery || every.Interval != 2*time.Hour {
		t.Fatalf("every 2h = %#v", every)
	}
	if every.DefaultRepeatLimit() != 0 {
		t.Fatal("a recurring schedule runs until it is stopped")
	}
	next, ok := every.Next(now)
	if !ok || !next.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("next = %v, %v", next, ok)
	}

	bare := mustParseSchedule(t, "30m", now)
	if bare.ScheduleKind != ScheduleScheduleEvery || bare.Interval != 30*time.Minute {
		t.Fatalf("bare duration = %#v", bare)
	}

	daily := mustParseSchedule(t, "daily at 7am", now)
	fire, ok := daily.Next(now)
	if !ok || fire.Hour() != 7 || fire.Day() != 16 {
		t.Fatalf("daily at 7am from 08:00 = %v (want 07:00 tomorrow)", fire)
	}

	weekdays := mustParseSchedule(t, "weekdays at 9am", now)
	fire, ok = weekdays.Next(now)
	if !ok || fire.Weekday() == time.Saturday || fire.Weekday() == time.Sunday || fire.Hour() != 9 {
		t.Fatalf("weekdays at 9am = %v", fire)
	}

	monday := mustParseSchedule(t, "every monday 9am", now)
	fire, ok = monday.Next(now)
	if !ok || fire.Weekday() != time.Monday || fire.Hour() != 9 {
		t.Fatalf("every monday 9am = %v", fire)
	}

	expr := mustParseSchedule(t, "0 9 * * *", now)
	fire, ok = expr.Next(now)
	if !ok || fire.Hour() != 9 || fire.Minute() != 0 {
		t.Fatalf("cron expression = %v", fire)
	}

	daysUnit := mustParseSchedule(t, "every 1d", now)
	if daysUnit.Interval != 24*time.Hour {
		t.Fatalf("every 1d = %v", daysUnit.Interval)
	}
}

// A schedule faster than the floor is a busy loop against a provider, so it is
// refused at parse time rather than throttled later where nobody sees it.
func TestParseRefusesSchedulesBelowTheFloor(t *testing.T) {
	now := time.Now()
	for _, expr := range []string{"every 30s", "10s"} {
		if _, err := ParseSchedule(expr, now); err == nil {
			t.Fatalf("ParseSchedule(%q) must refuse a sub-minute interval", expr)
		}
	}
	if _, err := ParseSchedule("in -5m", now); err == nil {
		t.Fatal("a one-shot in the past must be refused")
	}
	if _, err := ParseSchedule("whenever i feel like it", now); err == nil {
		t.Fatal("unparsable text must be refused rather than silently never firing")
	}
	if _, err := ParseSchedule("", now); err == nil {
		t.Fatal("an empty schedule must be refused")
	}
}

func TestParseClockReadsAmPmAndTwentyFourHour(t *testing.T) {
	cases := map[string][2]int{
		"9am":   {9, 0},
		"9:30":  {9, 30},
		"7pm":   {19, 0},
		"12am":  {0, 0},
		"12pm":  {12, 0},
		"07:05": {7, 5},
	}
	for raw, want := range cases {
		h, m, err := parseClock(raw)
		if err != nil || h != want[0] || m != want[1] {
			t.Fatalf("parseClock(%q) = %d:%d, %v; want %d:%d", raw, h, m, err, want[0], want[1])
		}
	}
}
