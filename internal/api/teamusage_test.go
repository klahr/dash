package api

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestCSVCell(t *testing.T) {
	if csvCell("=cmd()") != "'=cmd()" || csvCell("web") != "web" {
		t.Error("csvCell")
	}
}
