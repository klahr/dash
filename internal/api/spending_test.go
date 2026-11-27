package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

func TestSpendingCapsAreSetReadAndRaisedByRetry(t *testing.T) {
	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	h := loggedIn(t, s.Routes())

	w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"spending":{"task_usd":5,"day_usd":40,"month_usd":400,"zone":"Europe/Stockholm"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("set caps: %d %s", w.Code, w.Body)
	}
	var got model.Project
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Spending == nil || got.Spending.TaskUSD != 5 || got.Spending.DayUSD != 40 || got.Spending.MonthUSD != 400 || got.Spending.Zone != "Europe/Stockholm" {
		t.Fatalf("caps %+v", got.Spending)
	}
	for _, bad := range []string{`{"spending":{"task_usd":-1}}`, `{"spending":{"day_usd":5,"zone":"Mars/Olympus"}}`} {
		if w := do(t, h, "PATCH", "/api/projects/"+p.ID, bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
	// Warned periods are Dash's: a client can't set them.
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"spending":{"task_usd":5,"warned":["day:2026-11-27"]}}`); w.Code != http.StatusOK {
		t.Fatal(w.Body)
	} else if cur, _ := raw(s).GetProject(p.ID); len(cur.Spending.Warned) != 0 {
		t.Errorf("warned set from the API: %v", cur.Spending.Warned)
	}

	// A task stopped at its cap: Retry raises the cap once.
	now := time.Now()
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "costly", Prompt: "x"})
	if err == nil {
		td, err = raw(s).UpdateTodo(td.ID, func(x *model.Todo) error {
			x.CostUSD, x.CapHit = 5.2, 5
			x.RunStats = []model.TodoRun{{At: now, Kind: model.RunFirst, CostUSD: 5.2, Estimated: true}}
			return nil
		})
	}
	for _, st := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress, model.StatusFailed} {
		if err == nil {
			td, err = raw(s).SetStatus(td.ID, st, model.TaskCapError(5))
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if w := do(t, h, "POST", "/api/todos/"+td.ID+"/enqueue", ""); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body)
	}
	td, _ = raw(s).GetTodo(td.ID)
	if td.CapUSD != 10.2 || td.CapHit != 0 || td.Status != model.StatusQueued {
		t.Errorf("after retry: cap %v, hit %v, %s", td.CapUSD, td.CapHit, td.Status)
	}
	if c := model.TaskCap(got, td); c != 10.2 {
		t.Errorf("task cap %v", c)
	}

	w = do(t, h, "GET", "/api/projects/"+p.ID+"/spending", "")
	if w.Code != http.StatusOK {
		t.Fatalf("spending: %d %s", w.Code, w.Body)
	}
	var v spendingView
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	// The last PATCH replaced the caps without a zone: the server's.
	if len(v.Days) != trendDays || v.Days[trendDays-1].USD != 5.2 || !v.Estimated || v.Today != 5.2 || v.Zone != time.Local.String() {
		t.Errorf("view: today %v, estimated %v, zone %q, last day %+v", v.Today, v.Estimated, v.Zone, v.Days[trendDays-1])
	}

	// All 0 turns caps off.
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"spending":{}}`); w.Code != http.StatusOK {
		t.Fatal(w.Body)
	} else if cur, _ := raw(s).GetProject(p.ID); cur.Spending != nil {
		t.Errorf("caps off: %+v", cur.Spending)
	}
}

func TestSpendingPerMergedTask(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	p := model.Project{ID: "p", Spending: &model.Spending{Zone: "UTC", DayUSD: 1}}
	todos := []model.Todo{
		{ID: "a", Title: "A", Status: model.StatusMerged, MergedCommit: "abc", CostUSD: 2, UpdatedAt: now.Add(-time.Hour)},
		{ID: "b", Title: "B", Status: model.StatusMerged, MergedCommit: "def", CostUSD: 1, UpdatedAt: now.Add(-48 * time.Hour)},
		{ID: "c", Title: "closed", Status: model.StatusMerged, NoChanges: true, CostUSD: 9, UpdatedAt: now},
		{ID: "d", Title: "old", Status: model.StatusMerged, MergedCommit: "0ld", CostUSD: 9, UpdatedAt: now.AddDate(0, 0, -40)},
		{ID: "e", Title: "review", Status: model.StatusReview, CostUSD: 9, UpdatedAt: now},
	}
	v := spendingOf(p, todos, nil, now)
	if len(v.Merged) != 2 || v.Merged[0].ID != "a" || v.PerMerged != 1.5 {
		t.Errorf("merged %+v, per merged %v", v.Merged, v.PerMerged)
	}
}

func TestHubTaskCapOnTheRunner(t *testing.T) {
	if c := hubTaskCap(dispatch.Task{}, 3); c != 0 {
		t.Errorf("no cap: %v", c)
	}
	if c := hubTaskCap(dispatch.Task{CapUSD: 5, SpentUSD: 2}, 0); c != 3 {
		t.Errorf("a fresh copy gets what is left: %v", c)
	}
	if c := hubTaskCap(dispatch.Task{CapUSD: 5, SpentUSD: 2}, 2); c != 5 {
		t.Errorf("the copy that cost the 2: %v", c)
	}
	if c := hubTaskCap(dispatch.Task{CapUSD: 5, SpentUSD: 6}, 6); c != 6.01 {
		t.Errorf("over the cap already: %v", c)
	}
}
