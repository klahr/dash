package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/klahr/dash/internal/model"
)

// Team usage: what the org's Claude runs used and cost, by member, project
// and week or month, for its owners and admins (Organization → Billing & usage).
// It adds up each task's runs (Todo.RunStats; a task from before those were
// kept counts once, its whole cost, when it finished) and each scan run. A
// task's runs count for its creator (Todo.WorkFor: for a task Dash made,
// whoever its work was for); a scan's for Dash. Deleted tasks are gone, and
// so is what they cost.

// teamUsageRow is one period's use by one member in one project.
type teamUsageRow struct {
	Period    string `json:"period"` // the period's first day, YYYY-MM-DD
	MemberID  int64  `json:"member_id"`
	Member    string `json:"member"`
	ProjectID string `json:"project_id"`
	Project   string `json:"project"`
	teamUsageSum
}

// teamUsageSum is what a set of runs used.
type teamUsageSum struct {
	Tasks               int     `json:"tasks"` // tasks with a run in it
	Runs                int     `json:"runs"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CostUSD             float64 `json:"cost_usd"`
}

func (a *teamUsageSum) add(r model.TodoRun) {
	a.Runs++
	a.InputTokens += r.InputTokens
	a.OutputTokens += r.OutputTokens
	a.CacheReadTokens += r.CacheReadTokens
	a.CacheCreationTokens += r.CacheCreationTokens
	a.CostUSD += r.CostUSD
}

func (a *teamUsageSum) merge(b teamUsageSum) {
	a.Tasks += b.Tasks
	a.Runs += b.Runs
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.CacheReadTokens += b.CacheReadTokens
	a.CacheCreationTokens += b.CacheCreationTokens
	a.CostUSD += b.CostUSD
}

type teamUsage struct {
	Period  string         `json:"period"` // "week" (from Monday) or "month"
	From    string         `json:"from"`   // first day counted, YYYY-MM-DD
	To      string         `json:"to"`     // last day counted
	Periods []string       `json:"periods"`
	Rows    []teamUsageRow `json:"rows"`
	Total   teamUsageSum   `json:"total"`
}

// Kinds of period teamUsage groups by.
const (
	usageWeek  = "week"
	usageMonth = "month"
)

// periodStart is the first day of the period t falls in, in t's zone.
func periodStart(t time.Time, period string) time.Time {
	y, m, d := t.Date()
	if period == usageMonth {
		return time.Date(y, m, 1, 0, 0, 0, 0, t.Location())
	}
	day := time.Date(y, m, d, 0, 0, 0, 0, t.Location())
	back := (int(day.Weekday()) + 6) % 7 // days since Monday
	return day.AddDate(0, 0, -back)
}

func nextPeriod(t time.Time, period string) time.Time {
	if period == usageMonth {
		return t.AddDate(0, 1, 0)
	}
	return t.AddDate(0, 0, 7)
}

// teamUsageQuery reads ?period=week|month&from=&to=&tz= (tz as the
// browser's getTimezoneOffset()). Without from, it covers the last 8 weeks
// or 6 months up to to (default today).
func teamUsageQuery(r *http.Request, now time.Time) (period string, from, end time.Time, err error) {
	q := r.URL.Query()
	loc := time.Local
	if v := q.Get("tz"); v != "" {
		m, err := strconv.Atoi(v)
		if err != nil || m < -16*60 || m > 16*60 {
			return "", from, end, fmt.Errorf("%w: tz must be minutes behind UTC, as getTimezoneOffset() gives them", errBadRequest)
		}
		loc = time.FixedZone("", -m*60)
	}
	now = now.In(loc)
	switch period = q.Get("period"); period {
	case "":
		period = usageWeek
	case usageWeek, usageMonth:
	default:
		return "", from, end, fmt.Errorf("%w: period must be week or month", errBadRequest)
	}
	day := func(name string, def time.Time) (time.Time, error) {
		v := q.Get(name)
		if v == "" {
			return def, nil
		}
		t, err := time.ParseInLocation(time.DateOnly, v, loc)
		if err != nil {
			return t, fmt.Errorf("%w: %s must be a date, YYYY-MM-DD", errBadRequest, name)
		}
		return t, nil
	}
	y, m, d := now.Date()
	to, err := day("to", time.Date(y, m, d, 0, 0, 0, 0, loc))
	if err != nil {
		return "", from, end, err
	}
	def := periodStart(to, period).AddDate(0, 0, -7*7)
	if period == usageMonth {
		def = periodStart(to, period).AddDate(0, -5, 0)
	}
	if from, err = day("from", def); err != nil {
		return "", from, end, err
	}
	if to.Before(from) {
		return "", from, end, fmt.Errorf("%w: to is before from", errBadRequest)
	}
	if to.Sub(from) > 3*366*24*time.Hour {
		return "", from, end, fmt.Errorf("%w: at most three years at a time", errBadRequest)
	}
	return period, from, to.AddDate(0, 0, 1), nil
}

// teamUsage adds up what org's Claude runs from from (inclusive) to end
// (exclusive) used, by period, member and project.
func (s *Server) teamUsage(org int64, period string, from, end time.Time) teamUsage {
	loc := from.Location()
	out := teamUsage{Period: period, From: from.Format(time.DateOnly), To: end.AddDate(0, 0, -1).Format(time.DateOnly),
		Periods: []string{}, Rows: []teamUsageRow{}}
	for p := periodStart(from, period); p.Before(end); p = nextPeriod(p, period) {
		out.Periods = append(out.Periods, p.Format(time.DateOnly))
	}

	data := s.Store.ForOrg(org)
	projects := map[string]string{}
	for _, p := range data.ListProjects() {
		projects[p.ID] = p.Name
	}
	names := map[int64]string{0: "Dash", model.DeletedUser: "Deleted user"}
	if ms, err := s.Store.Accounts().Members(org); err == nil {
		for _, m := range ms {
			names[m.ID] = m.Username
		}
	}
	name := func(id int64) string {
		if n, ok := names[id]; ok {
			return n
		}
		n := fmt.Sprintf("User %d", id) // no longer a member
		if u, err := s.Store.Accounts().User(id); err == nil {
			n = u.Username
		}
		names[id] = n
		return n
	}

	type key struct {
		period  string
		member  int64
		project string
	}
	sums := map[key]*teamUsageSum{}
	counted := map[key]map[string]bool{} // the tasks counted in each
	in := func(at time.Time) (string, bool) {
		if at.Before(from) || !at.Before(end) {
			return "", false
		}
		return periodStart(at.In(loc), period).Format(time.DateOnly), true
	}
	add := func(k key, task string, r model.TodoRun) {
		sum := sums[k]
		if sum == nil {
			sum, counted[k] = &teamUsageSum{}, map[string]bool{}
			sums[k] = sum
		}
		sum.add(r)
		if task != "" && !counted[k][task] {
			counted[k][task] = true
			sum.Tasks++
		}
	}

	for _, t := range data.ListTodos("") {
		member := t.WorkFor()[0]
		runs := t.RunStats
		if len(runs) == 0 && t.CostUSD != 0 {
			// From before runs were kept: its whole cost, when it finished.
			at := t.CreatedAt
			if t.FinishedAt != nil {
				at = *t.FinishedAt
			} else if t.StartedAt != nil {
				at = *t.StartedAt
			}
			runs = []model.TodoRun{{At: at, CostUSD: t.CostUSD}}
		}
		for _, r := range runs {
			if p, ok := in(r.At); ok {
				add(key{p, member, t.ProjectID}, t.ID, r)
			}
		}
	}
	for id := range projects {
		for _, sr := range data.ListScanRuns(id, "") {
			if sr.CostUSD == 0 {
				continue
			}
			if p, ok := in(sr.StartedAt); ok {
				add(key{p, 0, id}, "", model.TodoRun{At: sr.StartedAt, CostUSD: sr.CostUSD})
			}
		}
	}

	for k, sum := range sums {
		out.Rows = append(out.Rows, teamUsageRow{Period: k.period, MemberID: k.member, Member: name(k.member),
			ProjectID: k.project, Project: projects[k.project], teamUsageSum: *sum})
		out.Total.merge(*sum)
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		if a.Period != b.Period {
			return a.Period < b.Period
		}
		if a.Member != b.Member {
			return a.Member < b.Member
		}
		return a.Project < b.Project
	})
	return out
}

// getTeamUsage answers GET /api/usage/team: the org's usage for its owners
// and admins, as JSON or (format=csv) a spreadsheet.
func (s *Server) getTeamUsage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.callerOr(w, r)
	if !ok {
		return
	}
	if c.Machine || !s.orgAdmin(c) {
		writeError(w, http.StatusForbidden, "only the organization's owners and admins see its usage")
		return
	}
	period, from, end, err := teamUsageQuery(r, time.Now())
	if err != nil {
		writeErr(w, err)
		return
	}
	u := s.teamUsage(c.Org, period, from, end)
	w.Header().Set("Cache-Control", "no-store")
	switch r.URL.Query().Get("format") {
	case "", "json":
		writeJSON(w, http.StatusOK, u)
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="dash-usage-%s-%s.csv"`, u.From, u.To))
		writeTeamUsageCSV(w, u)
	default:
		writeErr(w, fmt.Errorf("%w: format must be json or csv", errBadRequest))
	}
}

func writeTeamUsageCSV(w http.ResponseWriter, u teamUsage) {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{u.Period, "member", "project", "tasks", "runs", "input_tokens", "output_tokens",
		"cache_read_tokens", "cache_creation_tokens", "cost_usd"})
	i64 := func(n int64) string { return strconv.FormatInt(n, 10) }
	for _, r := range u.Rows {
		_ = cw.Write([]string{r.Period, csvCell(r.Member), csvCell(r.Project), strconv.Itoa(r.Tasks), strconv.Itoa(r.Runs),
			i64(r.InputTokens), i64(r.OutputTokens), i64(r.CacheReadTokens), i64(r.CacheCreationTokens),
			strconv.FormatFloat(r.CostUSD, 'f', 4, 64)})
	}
	cw.Flush()
}

// csvCell keeps a name a spreadsheet would read as a formula a name.
func csvCell(s string) string {
	if s != "" && (s[0] == '=' || s[0] == '+' || s[0] == '-' || s[0] == '@' || s[0] == '\t' || s[0] == '\r') {
		return "'" + s
	}
	return s
}
