package spend

import (
	"math"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestTotalsRollOverAtTheDayAndMonthInTheZone(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Skip(err)
	}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	es := []Entry{
		{At: at("2026-10-31 23:30"), USD: 7},   // last month
		{At: at("2026-11-01 00:10"), USD: 1},   // this month, an earlier day
		{At: at("2026-11-27 00:00"), USD: 2},   // today, from its first minute
		{At: at("2026-11-27 23:59"), USD: 0.5}, // today, estimated
	}
	es[3].Estimated = true
	tt := TotalsAt(es, at("2026-11-27 12:00"), loc)
	if !near(tt.Day, 2.5) || !near(tt.Month, 3.5) || !tt.DayEstimated {
		t.Fatalf("day %v, month %v, estimated %v", tt.Day, tt.Month, tt.DayEstimated)
	}
	if tt.DayKey != "day:2026-11-27" || tt.MonthKey != "month:2026-11" {
		t.Errorf("keys %q %q", tt.DayKey, tt.MonthKey)
	}
	if !tt.DayEnd.Equal(at("2026-11-28 00:00")) || !tt.MonthEnd.Equal(at("2026-12-01 00:00")) {
		t.Errorf("ends %v %v", tt.DayEnd, tt.MonthEnd)
	}

	// The next day: today's spend is yesterday's.
	next := TotalsAt(es, at("2026-11-28 00:01"), loc)
	if next.Day != 0 || !near(next.Month, 3.5) {
		t.Errorf("next day: day %v, month %v", next.Day, next.Month)
	}
	// The next month: nothing counts.
	dec := TotalsAt(es, at("2026-12-01 00:00"), loc)
	if dec.Day != 0 || dec.Month != 0 || dec.MonthKey != "month:2026-12" {
		t.Errorf("next month: day %v, month %v, key %q", dec.Day, dec.Month, dec.MonthKey)
	}
}

func TestHoldTakesTheMonthFirst(t *testing.T) {
	tt := Totals{Day: 6, Month: 60, DayEnd: time.Unix(100, 0), MonthEnd: time.Unix(200, 0)}
	if h := Hold(&model.Spending{DayUSD: 10, MonthUSD: 100}, tt); h != nil {
		t.Errorf("under both caps: %+v", h)
	}
	if h := Hold(&model.Spending{DayUSD: 5, MonthUSD: 100}, tt); h == nil || h.Period != model.SpendDay || !h.Until.Equal(tt.DayEnd) {
		t.Errorf("over the day's: %+v", h)
	}
	if h := Hold(&model.Spending{DayUSD: 5, MonthUSD: 60}, tt); h == nil || h.Period != model.SpendMonth || !h.Until.Equal(tt.MonthEnd) {
		t.Errorf("over both, reaching the month's exactly: %+v", h)
	}
	if h := Hold(&model.Spending{TaskUSD: 1}, tt); h != nil {
		t.Errorf("a task cap holds no project: %+v", h)
	}
	if h := Hold(nil, tt); h != nil {
		t.Errorf("no caps: %+v", h)
	}
}

func TestWarningsOncePerPeriod(t *testing.T) {
	s := &model.Spending{DayUSD: 10, MonthUSD: 100}
	tt := Totals{Day: 8, Month: 50, DayKey: "day:2026-11-27", MonthKey: "month:2026-11"}
	w := Warnings(s, tt)
	if len(w) != 1 || w[0].Kind != model.SpendDay || w[0].Key != tt.DayKey {
		t.Fatalf("at 80%% of the day's: %+v", w)
	}
	s.Warned = []string{tt.DayKey, "day:2026-11-26", "month:2026-10"}
	if w := Warnings(s, tt); len(w) != 0 {
		t.Errorf("warned already: %+v", w)
	}
	if got := KeepWarned(s.Warned, tt); len(got) != 1 || got[0] != tt.DayKey {
		t.Errorf("kept warned %v", got)
	}
	// The next day warns again.
	tt.DayKey = "day:2026-11-28"
	if w := Warnings(s, tt); len(w) != 1 || w[0].Key != "day:2026-11-28" {
		t.Errorf("next day: %+v", w)
	}
	if !TaskWarns(5, 4, 0) || TaskWarns(5, 3.9, 0) || TaskWarns(5, 4.5, 5) || !TaskWarns(10, 9, 5) || TaskWarns(0, 9, 0) {
		t.Error("TaskWarns")
	}
}

func TestEntriesAndTrend(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	done := now.Add(-48 * time.Hour)
	todos := []model.Todo{
		{ID: "a", RunStats: []model.TodoRun{{At: now.Add(-time.Hour), CostUSD: 1}, {At: now.Add(-25 * time.Hour), CostUSD: 2, Estimated: true}, {At: now}}},
		{ID: "old", CostUSD: 4, FinishedAt: &done}, // from before runs were kept
		{ID: "none"},
	}
	scans := []model.ScanRun{{StartedAt: now.Add(-time.Minute), CostUSD: 0.5}, {StartedAt: now}}
	es := Entries(todos, scans)
	if len(es) != 4 {
		t.Fatalf("entries %+v", es)
	}
	days := Trend(es, now, time.UTC, 30)
	if len(days) != 30 || days[29].Date != "2026-11-27" || days[0].Date != "2026-10-29" {
		t.Fatalf("days %v … %v", days[0], days[len(days)-1])
	}
	if !near(days[29].USD, 1.5) || !near(days[28].USD, 2) || !days[28].Estimated || !near(days[27].USD, 4) {
		t.Errorf("last days %+v", days[27:])
	}
}

func TestEstimate(t *testing.T) {
	u := model.ChatUsage{InputTokens: 1_000_000, OutputTokens: 100_000, CacheReadTokens: 2_000_000, CacheCreationTokens: 400_000}
	usd, ok := Estimate("claude-opus-5-5", false, u)
	// 4 + 0.1*20 + 2*0.2 + 0.4*5
	if !ok || !near(usd, 4+2+0.4+2) {
		t.Errorf("opus 5.5: %v %v", usd, ok)
	}
	if usd, _ := Estimate("claude-sonnet-4-6", false, model.ChatUsage{OutputTokens: 1_000_000}); !near(usd, 15) {
		t.Errorf("sonnet 4.6 output: %v", usd)
	}
	if usd, _ := Estimate("claude-opus-4-1-20250805", false, model.ChatUsage{InputTokens: 1_000_000}); !near(usd, 15) {
		t.Errorf("opus 4.1 input: %v", usd)
	}
	// Codex: GPT prices; its default model is priced as GPT-5.
	if usd, _ := Estimate("", true, model.ChatUsage{InputTokens: 1_000_000, CacheReadTokens: 1_000_000, OutputTokens: 1_000_000}); !near(usd, 1.25+0.125+10) {
		t.Errorf("codex default: %v", usd)
	}
	if usd, _ := Estimate("gpt-5-mini", true, model.ChatUsage{OutputTokens: 1_000_000}); !near(usd, 2) {
		t.Errorf("gpt-5-mini: %v", usd)
	}
	if usd, _ := Estimate("o4-mini", true, model.ChatUsage{InputTokens: 1_000_000}); !near(usd, 1.1) {
		t.Errorf("o4-mini: %v", usd)
	}
	// No tokens: nothing is known, nothing counts.
	if usd, ok := Estimate("gpt-5", true, model.ChatUsage{}); ok || usd != 0 {
		t.Errorf("no tokens: %v %v", usd, ok)
	}
}
