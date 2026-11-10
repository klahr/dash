package scan

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func TestCronNext(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ expr, after, want string }{
		{"0 6 * * *", "2026-11-07 05:59", "2026-11-07 06:00"},
		{"0 6 * * *", "2026-11-07 06:00", "2026-11-08 06:00"}, // strictly after
		{"*/15 * * * *", "2026-11-07 10:01", "2026-11-07 10:15"},
		{"30 9 * * 1-5", "2026-11-07 10:00", "2026-11-09 09:30"}, // Saturday → Monday
		{"0 6 * * mon", "2026-11-07 10:00", "2026-11-09 06:00"},
		{"0 0 * * 7", "2026-11-07 10:00", "2026-11-08 00:00"}, // 7 is Sunday
		{"0 0 1 jan *", "2026-11-07 10:00", "2027-01-01 00:00"},
		{"@hourly", "2026-11-07 10:20", "2026-11-07 11:00"},
		{"@daily", "2026-11-07 10:20", "2026-11-08 00:00"},
		{"0 12 1,15 * *", "2026-11-02 00:00", "2026-11-15 12:00"},
		// Day of month and weekday both set: either matches.
		{"0 0 13 * fri", "2026-11-07 10:00", "2026-11-13 00:00"},
		{"0 0 20 * mon", "2026-11-07 10:00", "2026-11-09 00:00"},
		// A day field starting with * is ANDed, as in Vixie cron: odd-day Mondays.
		{"0 6 */2 * mon", "2026-11-09 06:00", "2026-11-23 06:00"},
		{"0 6 1-31/2 * mon", "2026-11-09 06:00", "2026-11-11 06:00"},
		{"5/20 * * * *", "2026-11-07 10:06", "2026-11-07 10:25"},
	} {
		cr, err := ParseCron(c.expr)
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		if got := cr.Next(at(c.after)); !got.Equal(at(c.want)) {
			t.Errorf("%s after %s: %s, want %s", c.expr, c.after, got.Format("2006-01-02 15:04 Mon"), c.want)
		}
	}
	never, _ := ParseCron("0 0 30 2 *")
	if got := never.Next(at("2026-01-01 00:00")); !got.IsZero() {
		t.Errorf("Feb 30: %s", got)
	}
}

func TestParseCronErrors(t *testing.T) {
	t.Parallel()

	for _, e := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *", "5-1 * * * *", "x * * * *"} {
		if _, err := ParseCron(e); err == nil {
			t.Errorf("%q: no error", e)
		}
	}
}

func TestDue(t *testing.T) {
	t.Parallel()

	daily, _ := ParseCron("0 6 * * *")
	// Ran this morning: not due again until tomorrow 06:00.
	if Due(daily, at("2026-11-07 06:00"), at("2026-11-07 23:59")) {
		t.Error("due twice in a day")
	}
	if !Due(daily, at("2026-11-07 06:00"), at("2026-11-08 06:00")) {
		t.Error("not due at the next slot")
	}
	// Down for three days: due (once — the run moves the anchor to now).
	if !Due(daily, at("2026-11-04 06:00"), at("2026-11-07 12:00")) {
		t.Error("missed run not due")
	}
	if Due(daily, at("2026-11-07 12:00"), at("2026-11-07 12:01")) {
		t.Error("caught-up run due again")
	}
	never, _ := ParseCron("0 0 31 2 *")
	if Due(never, at("2026-01-01 00:00"), at("2030-01-01 00:00")) {
		t.Error("impossible schedule due")
	}
}
