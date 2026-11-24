package api

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

func TestPeriodStart(t *testing.T) {
	wed := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	if got := periodStart(wed, usageWeek).Format(time.DateOnly); got != "2026-11-16" {
		t.Errorf("week of a Wednesday: %s", got)
	}
	sun := time.Date(2026, 11, 22, 23, 0, 0, 0, time.UTC)
	if got := periodStart(sun, usageWeek).Format(time.DateOnly); got != "2026-11-16" {
		t.Errorf("week of a Sunday: %s", got)
	}
	if got := periodStart(wed, usageMonth).Format(time.DateOnly); got != "2026-11-01" {
		t.Errorf("month: %s", got)
	}
}

// The org's usage by member, project and week, for owners and admins only,
// as JSON and CSV.
func TestTeamUsage(t *testing.T) {
	parallel(t)

	s, web := wallServer(t)
	routes := s.Routes()
	h := loginAs(t, routes, "u", "p")
	mia := joinAs(t, s, routes, "mia", store.RoleMember)
	owner := testOwner(t, s)

	week := periodStart(time.Now().UTC(), usageWeek)
	thisWeek, lastWeek := week.Add(time.Hour), week.AddDate(0, 0, -7).Add(time.Hour)
	run := func(at time.Time, in int64, cost float64) model.TodoRun {
		return model.TodoRun{At: at, Kind: model.RunFirst, ChatUsage: model.ChatUsage{InputTokens: in, OutputTokens: 1}, CostUSD: cost}
	}
	setTodo(t, s, web, func(x *model.Todo) {
		x.CreatedBy, x.Status = owner.User, model.StatusReview
		x.RunStats = []model.TodoRun{run(lastWeek, 10, 1), run(thisWeek, 20, 2)}
	})
	setTodo(t, s, web, func(x *model.Todo) {
		x.CreatedBy, x.Status = owner.User, model.StatusReview
		x.RunStats = []model.TodoRun{run(thisWeek, 5, 0.5)}
	})
	// Older than the range: not counted.
	setTodo(t, s, web, func(x *model.Todo) {
		x.CreatedBy, x.Status = owner.User, model.StatusReview
		x.RunStats = []model.TodoRun{run(week.AddDate(-1, 0, 0), 1000, 100)}
	})

	get := func(h http.Handler, url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		return w
	}
	if w := get(mia, "/api/usage/team"); w.Code != http.StatusForbidden {
		t.Fatalf("a member: %d", w.Code)
	}
	if w := get(h, "/api/usage/team?period=year"); w.Code != http.StatusBadRequest {
		t.Errorf("bad period: %d", w.Code)
	}

	w := get(h, "/api/usage/team?tz=0")
	if w.Code != http.StatusOK {
		t.Fatalf("usage: %d %s", w.Code, w.Body)
	}
	var u teamUsage
	if err := json.Unmarshal(w.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if len(u.Periods) != 8 || u.Periods[7] != week.Format(time.DateOnly) {
		t.Errorf("periods %v", u.Periods)
	}
	if len(u.Rows) != 2 {
		t.Fatalf("rows %+v", u.Rows)
	}
	last, this := u.Rows[0], u.Rows[1]
	if last.Period != week.AddDate(0, 0, -7).Format(time.DateOnly) || last.Runs != 1 || last.Tasks != 1 || last.CostUSD != 1 {
		t.Errorf("last week %+v", last)
	}
	if this.Runs != 2 || this.Tasks != 2 || this.InputTokens != 25 || this.CostUSD != 2.5 ||
		this.MemberID != owner.User || this.Member != "u" || this.ProjectID != web.ID || this.Project != web.Name {
		t.Errorf("this week %+v", this)
	}
	if u.Total.CostUSD != 3.5 || u.Total.Runs != 3 {
		t.Errorf("total %+v", u.Total)
	}

	// By month, everything since the start of last month.
	w = get(h, "/api/usage/team?tz=0&period=month")
	_ = json.Unmarshal(w.Body.Bytes(), &u)
	if u.Period != "month" || len(u.Periods) != 6 || u.Total.CostUSD != 3.5 {
		t.Errorf("by month %+v", u)
	}

	// The owner's read-only API token sees it too.
	secret, _, err := s.Auth.Accounts().CreateToken(store.NewToken{Kind: store.TokenAPI, Name: "t", UserID: owner.User, OrgID: owner.Org,
		Scopes: []string{store.ScopeRead}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if w := get(withToken(routes, secret), "/api/usage/team"); w.Code != http.StatusOK {
		t.Errorf("with an API token: %d %s", w.Code, w.Body)
	}

	w = get(h, "/api/usage/team?tz=0&format=csv")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("csv: %d %s", w.Code, w.Header())
	}
	rows, err := csv.NewReader(w.Body).ReadAll()
	if err != nil || len(rows) != 3 || rows[0][0] != "week" || rows[2][1] != "u" || rows[2][9] != "2.5000" {
		t.Errorf("csv %q %v", rows, err)
	}
}

// A task from before runs were kept counts once, its whole cost, when it
// finished (else started, else was created); a scan's cost counts for Dash;
// another org's tasks don't count.
func TestTeamUsageLegacyScansAndOrgs(t *testing.T) {
	parallel(t)

	s, web := wallServer(t)
	routes := s.Routes()
	h := loginAs(t, routes, "u", "p")
	owner := testOwner(t, s)

	week := periodStart(time.Now().UTC(), usageWeek)
	thisWeek, lastWeek := week.Add(time.Hour), week.AddDate(0, 0, -7).Add(time.Hour)
	// Finished this week, started last week: counts this week.
	setTodo(t, s, web, func(x *model.Todo) {
		x.CreatedBy, x.Status, x.CostUSD = owner.User, model.StatusReview, 4
		x.StartedAt, x.FinishedAt = &lastWeek, &thisWeek
	})
	// Started last week, not finished: counts last week.
	setTodo(t, s, web, func(x *model.Todo) {
		x.CreatedBy, x.Status, x.CostUSD = owner.User, model.StatusInProgress, 0.25
		x.StartedAt, x.FinishedAt = &lastWeek, nil
	})
	// Never started: counts when it was created (now).
	setTodo(t, s, web, func(x *model.Todo) {
		x.CreatedBy, x.Status, x.CostUSD = owner.User, model.StatusQueued, 0.125
		x.StartedAt, x.FinishedAt = nil, nil
	})
	// Free: no row.
	setTodo(t, s, web, func(x *model.Todo) { x.CreatedBy, x.Status = owner.User, model.StatusReview })
	for _, cost := range []float64{1, 0} {
		if _, err := raw(s).CreateScanRun(model.ScanRun{ProjectID: web.ID, Scan: "test-gaps", StartedAt: thisWeek, CostUSD: cost}); err != nil {
			t.Fatal(err)
		}
	}

	// Another org's costed task and scan.
	beta, err := raw(s).Accounts().CreateOrg(owner.User, "Beta")
	if err != nil {
		t.Fatal(err)
	}
	bs := raw(s).ForOrg(beta.ID)
	bp, err := bs.CreateProject(model.Project{Name: "beta-only", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	bt, err := bs.CreateTodo(model.Todo{ProjectID: bp.ID, Title: "beta task", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.UpdateTodo(bt.ID, func(x *model.Todo) error {
		x.CreatedBy, x.CostUSD = owner.User, 100
		x.RunStats = []model.TodoRun{{At: thisWeek, Kind: model.RunFirst, CostUSD: 100}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := raw(s).CreateScanRun(model.ScanRun{ProjectID: bp.ID, Scan: "test-gaps", StartedAt: thisWeek, CostUSD: 50}); err != nil {
		t.Fatal(err)
	}

	get := func(h http.Handler, url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		return w
	}
	w := get(h, "/api/usage/team?tz=0")
	if w.Code != http.StatusOK {
		t.Fatalf("usage: %d %s", w.Code, w.Body)
	}
	var u teamUsage
	if err := json.Unmarshal(w.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if u.Total.CostUSD != 5.375 || u.Total.Runs != 4 || u.Total.Tasks != 3 {
		t.Errorf("total %+v", u.Total)
	}
	if len(u.Rows) != 3 {
		t.Fatalf("rows %+v", u.Rows)
	}
	lw, tw := week.AddDate(0, 0, -7).Format(time.DateOnly), week.Format(time.DateOnly)
	var dash, last, this *teamUsageRow
	for i, r := range u.Rows {
		if r.ProjectID != web.ID {
			t.Errorf("row of another project %+v", r)
		}
		switch {
		case r.MemberID == 0:
			dash = &u.Rows[i]
		case r.Period == lw:
			last = &u.Rows[i]
		case r.Period == tw:
			this = &u.Rows[i]
		}
	}
	if dash == nil || dash.Member != "Dash" || dash.Period != tw || dash.Tasks != 0 || dash.Runs != 1 || dash.CostUSD != 1 {
		t.Errorf("Dash's row %+v", dash)
	}
	if last == nil || last.MemberID != owner.User || last.Tasks != 1 || last.Runs != 1 || last.CostUSD != 0.25 {
		t.Errorf("last week %+v", last)
	}
	if this == nil || this.MemberID != owner.User || this.Tasks != 2 || this.Runs != 2 || this.CostUSD != 4.125 {
		t.Errorf("this week %+v", this)
	}
}

// Bad queries are 400s; another Dash (a machine caller) may not see usage.
func TestTeamUsageRejects(t *testing.T) {
	parallel(t)

	s, _ := wallServer(t)
	routes := s.Routes()
	h := loginAs(t, routes, "u", "p")
	for _, q := range []string{
		"tz=99999", "tz=-99999", "tz=x",
		"from=x", "to=2026-13-01",
		"from=2026-02-01&to=2026-01-01",
		"from=2020-01-01&to=2026-01-01",
		"format=xml",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/usage/team?"+q, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("?%s: %d %s", q, w.Code, w.Body)
		}
	}
	// Just under three years is fine.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/usage/team?from=2023-01-02&to=2026-01-01", nil))
	if w.Code != http.StatusOK {
		t.Errorf("three years: %d %s", w.Code, w.Body)
	}

	r := httptest.NewRequest("GET", "/api/usage/team", nil)
	r = r.WithContext(auth.WithHub(r.Context()))
	w = httptest.NewRecorder()
	s.getTeamUsage(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("a machine caller: %d %s", w.Code, w.Body)
	}
}

func TestCSVCell(t *testing.T) {
	if csvCell("=cmd()") != "'=cmd()" || csvCell("web") != "web" {
		t.Error("csvCell")
	}
}
