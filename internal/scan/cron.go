package scan

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed five-field cron expression (minute hour day-of-month
// month day-of-week), in the server's local time. Fields take *, numbers,
// ranges (1-5), steps (*/15, 1-30/2), lists (1,15) and month and weekday
// names (jan, mon). @hourly, @daily, @weekly, @monthly and @yearly work too.
type Cron struct {
	min, hour, dom, month, dow uint64 // bit n set = n matches
	domAny, dowAny             bool   // the field started with * (or ?)
}

var cronMacros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var (
	monthNames = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	dayNames   = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
)

// ParseCron parses a cron expression.
func ParseCron(expr string) (Cron, error) {
	expr = strings.TrimSpace(strings.ToLower(expr))
	if m, ok := cronMacros[expr]; ok {
		expr = m
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return Cron{}, fmt.Errorf("%q: want five fields (minute hour day month weekday), e.g. \"0 6 * * *\"", expr)
	}
	var c Cron
	var err error
	if c.min, err = cronField(f[0], 0, 59, nil, 0); err != nil {
		return c, fmt.Errorf("minute: %w", err)
	}
	if c.hour, err = cronField(f[1], 0, 23, nil, 0); err != nil {
		return c, fmt.Errorf("hour: %w", err)
	}
	if c.dom, err = cronField(f[2], 1, 31, nil, 0); err != nil {
		return c, fmt.Errorf("day of month: %w", err)
	}
	if c.month, err = cronField(f[3], 1, 12, monthNames, 1); err != nil {
		return c, fmt.Errorf("month: %w", err)
	}
	if c.dow, err = cronField(f[4], 0, 7, dayNames, 0); err != nil {
		return c, fmt.Errorf("day of week: %w", err)
	}
	if c.dow&(1<<7) != 0 { // 7 is Sunday too
		c.dow |= 1
	}
	// As in Vixie cron, a day field starting with * (e.g. */2) counts as
	// unrestricted for the OR rule, so it is ANDed with the other day field.
	c.domAny = strings.HasPrefix(f[2], "*") || strings.HasPrefix(f[2], "?")
	c.dowAny = strings.HasPrefix(f[4], "*") || strings.HasPrefix(f[4], "?")
	return c, nil
}

func cronField(s string, lo, hi int, names []string, nameBase int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(s, ",") {
		rng, stepS, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepS)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("bad step %q", stepS)
			}
			step = n
		}
		var from, to int
		switch {
		case rng == "*" || rng == "?":
			from, to = lo, hi
		default:
			a, b, isRange := strings.Cut(rng, "-")
			var err error
			if from, err = cronValue(a, lo, hi, names, nameBase); err != nil {
				return 0, err
			}
			to = from
			if isRange {
				if to, err = cronValue(b, lo, hi, names, nameBase); err != nil {
					return 0, err
				}
				if to < from {
					return 0, fmt.Errorf("range %q runs backwards", rng)
				}
			} else if hasStep {
				to = hi // "5/15" means 5-hi/15
			}
		}
		for v := from; v <= to; v += step {
			bits |= 1 << v
		}
	}
	return bits, nil
}

func cronValue(s string, lo, hi int, names []string, base int) (int, error) {
	for i, n := range names {
		if s == n {
			return i + base, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	if v < lo || v > hi {
		return 0, fmt.Errorf("%d is outside %d-%d", v, lo, hi)
	}
	return v, nil
}

func (c Cron) dayMatches(t time.Time) bool {
	dom := c.dom&(1<<t.Day()) != 0
	dow := c.dow&(1<<int(t.Weekday())) != 0
	if c.domAny || c.dowAny {
		return dom && dow // a * field matches every day
	}
	return dom || dow // both restricted: either one, as in classic cron
}

// Next is the first time after t that the expression matches, to the
// minute; the zero time if it never does (e.g. "0 0 30 2 *").
func (c Cron) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if c.month&(1<<int(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !c.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if c.hour&(1<<t.Hour()) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if c.min&(1<<t.Minute()) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

// Due reports whether a run is due at now when the last one was at anchor:
// whether a slot came after anchor and is not in the future. However many
// slots were missed (the server was down), that is one run.
func Due(c Cron, anchor, now time.Time) bool {
	next := c.Next(anchor)
	return !next.IsZero() && !next.After(now)
}
