package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// wallServer is boardServer with the wall page among its static files.
func wallServer(t *testing.T) (*Server, model.Project) {
	t.Helper()
	s, _, web := boardServer(t)
	s.Static = fstest.MapFS{"index.html": {Data: []byte("ok")}, "wall.html": {Data: []byte("<title>{{DASH_NAME}} wall</title>")}}
	return s, web
}

func getWallJSON(t *testing.T, h http.Handler, url string) (wallData, int) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
	var d wallData
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatalf("%s: %v\n%s", url, err, w.Body)
		}
	}
	return d, w.Code
}

// setTodo creates a task in p and forces its fields (status, times) as given.
func setTodo(t *testing.T, s *Server, p model.Project, fn func(*model.Todo)) model.Todo {
	t.Helper()
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "task", Prompt: "secret prompt"})
	if err != nil {
		t.Fatal(err)
	}
	want := td
	fn(&want)
	// Walk the lifecycle to the wanted status, then set the fields.
	paths := map[model.TodoStatus][]model.TodoStatus{
		model.StatusQueued:     {model.StatusQueued},
		model.StatusInProgress: {model.StatusQueued, model.StatusInProgress},
		model.StatusReview:     {model.StatusQueued, model.StatusInProgress, model.StatusReview},
		model.StatusMerging:    {model.StatusQueued, model.StatusInProgress, model.StatusReview, model.StatusMerging},
		model.StatusMerged:     {model.StatusQueued, model.StatusInProgress, model.StatusReview, model.StatusMerged},
		model.StatusFailed:     {model.StatusQueued, model.StatusInProgress, model.StatusFailed},
	}
	for _, st := range paths[want.Status] {
		if _, err := raw(s).UpdateTodo(td.ID, func(x *model.Todo) error { x.Status = st; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	td, err = raw(s).UpdateTodo(td.ID, func(x *model.Todo) error { fn(x); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return td
}

func at(d time.Duration) *time.Time { x := time.Now().Add(d); return &x }

func TestWallAggregates(t *testing.T) {
	s, web := wallServer(t)
	h := loginAs(t, s.Routes(), "u", "p")

	dep := setTodo(t, s, web, func(x *model.Todo) { x.Title, x.Status = "dep", model.StatusDraft })
	setTodo(t, s, web, func(x *model.Todo) { x.Status = model.StatusQueued })
	setTodo(t, s, web, func(x *model.Todo) { x.Status, x.DependsOn = model.StatusQueued, []string{dep.ID} })
	setTodo(t, s, web, func(x *model.Todo) { x.Status, x.Waiting = model.StatusQueued, "all runners offline" })
	setTodo(t, s, web, func(x *model.Todo) {
		x.Title, x.Status, x.StartedAt, x.Runs, x.CostUSD, x.DiffAdd, x.DiffDel = "Building it", model.StatusInProgress, at(-10*time.Minute), 2, 1.5, 20, 3
		x.SentBack = "failing tests"
	})
	setTodo(t, s, web, func(x *model.Todo) {
		x.Title, x.Status, x.FinishedAt, x.Checks = "old review", model.StatusReview, at(-3*time.Hour), &model.Checks{Passed: true}
	})
	setTodo(t, s, web, func(x *model.Todo) {
		x.Title, x.Status, x.FinishedAt = "new review", model.StatusReview, at(-5*time.Minute)
	})
	setTodo(t, s, web, func(x *model.Todo) { x.Status, x.PR = model.StatusMerging, &model.PullRequest{Number: 7} })
	setTodo(t, s, web, func(x *model.Todo) {
		x.Title, x.Status, x.FinishedAt, x.Runs, x.DiffAdd, x.DiffDel, x.CostUSD = "landed", model.StatusMerged, at(-time.Minute), 1, 100, 40, 2
	})
	setTodo(t, s, web, func(x *model.Todo) {
		x.Status, x.FinishedAt, x.Runs = model.StatusMerged, at(-49*time.Hour), 3 // not today
	})
	setTodo(t, s, web, func(x *model.Todo) {
		x.Title, x.Status, x.FinishedAt, x.Error, x.Resumable = "broke", model.StatusFailed, at(-2*time.Hour), "merge conflict in a.go\nmore", true
	})
	if _, _, err := raw(s).UpsertIssue(model.Issue{ProjectID: web.ID, Title: "XSS in form", Type: model.IssueVuln, Severity: model.SevHigh, Details: "d"}); err != nil {
		t.Fatal(err)
	}
	// Another org's work never shows.
	owner := testOwner(t, s)
	two, err := s.Auth.Accounts().CreateOrg(owner.User, "Two")
	if err != nil {
		t.Fatal(err)
	}
	theirs, _ := raw(s).CreateProject(model.Project{Name: "theirs", OrgID: two.ID})
	setTodo(t, s, theirs, func(x *model.Todo) {
		x.Title, x.Status, x.StartedAt = "their task", model.StatusInProgress, at(-time.Minute)
	})

	d, code := getWallJSON(t, h, "/api/wall")
	if code != http.StatusOK {
		t.Fatalf("GET /api/wall: %d", code)
	}
	st := d.Stages
	if st.Queued != 3 || st.Blocked != 1 || st.WaitingRunner != 1 {
		t.Errorf("queued %+v", st)
	}
	if st.Running != 1 || st.Review != 2 || st.Merging != 1 || st.MergingPR != 1 || st.MergedToday != 1 || st.MergedAdd != 100 || st.Failed != 1 || st.Resumable != 1 {
		t.Errorf("stages %+v", st)
	}
	if st.ReviewOldest == nil || time.Since(*st.ReviewOldest) < 2*time.Hour {
		t.Errorf("oldest review %v", st.ReviewOldest)
	}
	if len(d.Running) != 1 || d.Running[0].Title != "Building it" || d.Running[0].Run != 3 || d.Running[0].CostUSD == nil ||
		*d.Running[0].CostUSD != 1.5 || d.Running[0].SentBack != "failing tests" || d.Running[0].Runner != "this computer" {
		t.Errorf("running %+v", d.Running)
	}
	if len(d.Needs) != 3 || d.Needs[0].Title != "old review" || !d.Needs[0].Old || d.Needs[0].Tests != "passed" ||
		d.Needs[1].Title != "new review" || d.Needs[1].Old || d.Needs[2].Kind != "failed" || d.Needs[2].Reason != "merge conflict in a.go" || !d.Needs[2].Old {
		t.Errorf("needs %+v", d.Needs)
	}
	if d.Issues.Open != 1 || d.Issues.Severity["high"] != 1 || len(d.Issues.Top) != 1 {
		t.Errorf("issues %+v", d.Issues)
	}
	if d.Today.Merged != 1 || d.Today.FirstTry == nil || *d.Today.FirstTry != 1 || d.Today.SpendUSD == nil || *d.Today.SpendUSD != 3.5 {
		t.Errorf("today %+v", d.Today)
	}
	if len(d.Runners) != 1 || d.Queue.Busy != 1 || d.Queue.Slots < 1 {
		t.Errorf("runners %+v queue %+v", d.Runners, d.Queue)
	}
	if len(d.Events) == 0 || d.Events[0].Kind != "merged" {
		t.Errorf("events %+v", d.Events)
	}
	body, _ := json.Marshal(d)
	if strings.Contains(string(body), "their task") || strings.Contains(string(body), "secret prompt") {
		t.Errorf("leaked: %s", body)
	}

	// Privacy: no titles, prompts or costs.
	d, _ = getWallJSON(t, h, "/api/wall?privacy=1")
	body, _ = json.Marshal(d)
	for _, secret := range []string{"Building it", "old review", "landed", "XSS in form"} {
		if strings.Contains(string(body), secret) {
			t.Errorf("privacy mode shows %q", secret)
		}
	}
	if d.Running[0].CostUSD != nil || d.Today.SpendUSD != nil || !d.Privacy {
		t.Errorf("privacy mode shows costs: %+v %+v", d.Running[0], d.Today)
	}

	// The project filter, by name or id.
	if d, code := getWallJSON(t, h, "/api/wall?project=other"); code != http.StatusOK || d.Stages.Running != 0 || d.Project != "other" {
		t.Errorf("filtered: %d %+v", code, d.Stages)
	}
	if d, _ := getWallJSON(t, h, "/api/wall?project="+web.ID); d.Stages.Running != 1 {
		t.Errorf("filtered by id: %+v", d.Stages)
	}
	if _, code := getWallJSON(t, h, "/api/wall?project="+theirs.ID); code != http.StatusNotFound {
		t.Errorf("another org's project: %d", code)
	}
}

func TestWallActivity(t *testing.T) {
	s, web := wallServer(t)
	h := loginAs(t, s.Routes(), "u", "p")
	td := setTodo(t, s, web, func(x *model.Todo) { x.Status, x.StartedAt = model.StatusInProgress, at(-time.Minute) })
	s.Hub.Publish(model.Event{Type: model.EvTodoEvent, ProjectID: web.ID, TodoID: td.ID,
		Data: model.RunEvent{Kind: "tool_use", Tool: "Edit", Input: json.RawMessage(`{"file_path":"web/app.js"}`), At: time.Now()}})
	d, _ := getWallJSON(t, h, "/api/wall")
	if len(d.Running) != 1 || d.Running[0].Activity != "Edit web/app.js" {
		t.Errorf("activity %+v", d.Running)
	}
	if d, _ := getWallJSON(t, h, "/api/wall?privacy=1"); d.Running[0].Activity != "" {
		t.Errorf("privacy mode shows activity %q", d.Running[0].Activity)
	}
}

func TestWallNeedsSignInOrToken(t *testing.T) {
	s, _ := wallServer(t)
	h := s.Routes()
	if _, code := getWallJSON(t, h, "/api/wall"); code != http.StatusUnauthorized {
		t.Errorf("signed out: %d", code)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/wall", nil))
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "next=%2Fwall") {
		t.Errorf("signed-out page: %d %s", w.Code, w.Header().Get("Location"))
	}
	w = httptest.NewRecorder()
	loginAs(t, h, "u", "p").ServeHTTP(w, httptest.NewRequest("GET", "/wall", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "wall") {
		t.Errorf("signed-in page: %d %s", w.Code, w.Body)
	}
}

func TestWallTokens(t *testing.T) {
	s, web := wallServer(t)
	routes := s.Routes()
	h := loginAs(t, routes, "u", "p")
	setTodo(t, s, web, func(x *model.Todo) { x.Title, x.Status, x.StartedAt = "ours", model.StatusInProgress, at(-time.Minute) })

	// An org admin makes one.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/wall/tokens", strings.NewReader(`{"name":"Office TV"}`)))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var made struct {
		Token  store.WallToken `json:"token"`
		Secret string          `json:"secret"`
		Path   string          `json:"path"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &made)
	if made.Secret == "" || made.Path != "/wall?token="+made.Secret || made.Token.Name != "Office TV" {
		t.Fatalf("made %+v", made)
	}

	// Valid: the page and its data, without signing in.
	d, code := getWallJSON(t, routes, "/api/wall?token="+made.Secret)
	if code != http.StatusOK || len(d.Running) != 1 || d.Running[0].Title != "ours" {
		t.Fatalf("with token: %d %+v", code, d.Running)
	}
	w = httptest.NewRecorder()
	routes.ServeHTTP(w, httptest.NewRequest("GET", "/wall?token="+made.Secret, nil))
	if w.Code != http.StatusOK {
		t.Errorf("page with token: %d", w.Code)
	}
	// ...and nothing else: not the API, as a query or a bearer token.
	for _, url := range []string{"/api/projects?token=" + made.Secret, "/api/wall/tokens?token=" + made.Secret, "/?token=" + made.Secret} {
		w = httptest.NewRecorder()
		routes.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		if w.Code == http.StatusOK {
			t.Errorf("%s with a wall token: %d", url, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/api/projects", nil)
	r.Header.Set("Authorization", "Bearer "+made.Secret)
	w = httptest.NewRecorder()
	routes.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wall token as a bearer token: %d", w.Code)
	}
	if _, code := getWallJSON(t, routes, "/api/wall?token=dashw_nonsense"); code != http.StatusUnauthorized {
		t.Errorf("unknown token: %d", code)
	}

	// Wrong org: a token of another org shows that org's wall only, and
	// its admin can't revoke ours.
	owner := testOwner(t, s)
	two, err := s.Auth.Accounts().CreateOrg(owner.User, "Two")
	if err != nil {
		t.Fatal(err)
	}
	theirs, _ := raw(s).CreateProject(model.Project{Name: "theirs", OrgID: two.ID})
	setTodo(t, s, theirs, func(x *model.Todo) {
		x.Title, x.Status, x.StartedAt = "theirs", model.StatusInProgress, at(-time.Minute)
	})
	other, _, err := s.Store.Accounts().CreateWallToken(two.ID, owner.User, "their TV", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d, _ = getWallJSON(t, routes, "/api/wall?token="+other)
	if len(d.Running) != 1 || d.Running[0].Title != "theirs" || d.Org != "Two" {
		t.Errorf("their token: %+v", d.Running)
	}
	if _, code := getWallJSON(t, routes, "/api/wall?token="+other+"&project="+web.ID); code != http.StatusNotFound {
		t.Errorf("their token, our project: %d", code)
	}
	if err := s.Store.Accounts().RevokeWallToken(two.ID, made.Token.ID); err == nil {
		t.Error("another org revoked our token")
	}

	// Members who aren't admins can't manage them.
	member(t, s, owner.Org, "bob", store.RoleMember)
	bob := loginAs(t, routes, "bob", "long-enough-bob")
	for _, req := range []*http.Request{
		httptest.NewRequest("GET", "/api/wall/tokens", nil),
		httptest.NewRequest("POST", "/api/wall/tokens", strings.NewReader(`{}`)),
		httptest.NewRequest("DELETE", "/api/wall/tokens/1", nil),
	} {
		w = httptest.NewRecorder()
		bob.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("member %s %s: %d", req.Method, req.URL.Path, w.Code)
		}
	}
	// ...but may still look at the wall.
	if _, code := getWallJSON(t, bob, "/api/wall"); code != http.StatusOK {
		t.Errorf("member's wall: %d", code)
	}

	// Listed, then revoked: it stops working.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/wall/tokens", nil))
	var list []store.WallToken
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].ID != made.Token.ID || list[0].LastUsed.IsZero() {
		t.Errorf("list %d %+v", w.Code, list)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/wall/tokens/"+itoa(made.Token.ID), nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if _, code := getWallJSON(t, routes, "/api/wall?token="+made.Secret); code != http.StatusUnauthorized {
		t.Errorf("revoked token: %d", code)
	}
	w = httptest.NewRecorder()
	routes.ServeHTTP(w, httptest.NewRequest("GET", "/wall?token="+made.Secret, nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("page with a revoked token: %d", w.Code)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
