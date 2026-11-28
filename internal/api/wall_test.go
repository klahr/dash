package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/usage"
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

// middayTZ is a ?tz= value (as getTimezoneOffset() gives it) for a zone where
// it is now about midday, so tasks a few hours old fall on the wall's "today"
// whatever the time of day the test runs.
func middayTZ() string {
	u := time.Now().UTC()
	east := 12*60 - (u.Hour()*60 + u.Minute()) // minutes east of UTC, within ±12h
	return strconv.Itoa(-east)
}

func TestWallAggregates(t *testing.T) {
	parallel(t)

	s, web := wallServer(t)
	h := loginAs(t, s.Routes(), "u", "p")

	dep := setTodo(t, s, web, func(x *model.Todo) { x.Title, x.Status = "dep", model.StatusDraft })
	setTodo(t, s, web, func(x *model.Todo) { x.Status = model.StatusQueued })
	setTodo(t, s, web, func(x *model.Todo) { x.Status, x.DependsOn = model.StatusQueued, []string{dep.ID} })
	setTodo(t, s, web, func(x *model.Todo) { x.Status, x.Waiting = model.StatusQueued, "all runners offline" })
	setTodo(t, s, web, func(x *model.Todo) {
		x.Title, x.Status, x.StartedAt, x.Runs, x.CostUSD, x.DiffAdd, x.DiffDel = "Building it", model.StatusInProgress, at(-10*time.Minute), 2, 1.5, 20, 3
		x.SentBack = "conflicts with main in secret/a.go"
		// Only today's run counts towards today's spend.
		x.RunStats = []model.TodoRun{{At: *at(-49 * time.Hour), Kind: model.RunFirst, CostUSD: 1}, {At: *at(-10 * time.Minute), Kind: model.RunNotes, CostUSD: 0.5}}
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
	if _, err := raw(s).ReportFinding(model.Issue{ProjectID: web.ID, Title: "XSS in form", Type: model.IssueVuln, Severity: model.SevHigh, Details: "d"}); err != nil {
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

	d, code := getWallJSON(t, h, "/api/wall?tz="+middayTZ())
	if code != http.StatusOK {
		t.Fatalf("GET /api/wall: %d", code)
	}
	if d.Today.Hour != 12 {
		t.Errorf("tz: the wall's hour is %d, not 12", d.Today.Hour)
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
		*d.Running[0].CostUSD != 1.5 || d.Running[0].SentBack != "conflicts with main in secret/a.go" || d.Running[0].Runner != "this computer" {
		t.Errorf("running %+v", d.Running)
	}
	if len(d.Needs) != 3 || d.Needs[0].Title != "old review" || !d.Needs[0].Old || d.Needs[0].Tests != "passed" ||
		d.Needs[1].Title != "new review" || d.Needs[1].Old || d.Needs[2].Kind != "failed" || d.Needs[2].Reason != "merge conflict in a.go" || !d.Needs[2].Old {
		t.Errorf("needs %+v", d.Needs)
	}
	if d.Issues.Open != 1 || d.Issues.Severity["high"] != 1 || len(d.Issues.Top) != 1 {
		t.Errorf("issues %+v", d.Issues)
	}
	if d.Today.Merged != 1 || d.Today.FirstTry == nil || *d.Today.FirstTry != 1 || d.Today.SpendUSD == nil || *d.Today.SpendUSD != 2.5 {
		t.Errorf("today %+v", d.Today)
	}
	if len(d.Runners) != 1 || d.Queue.Busy != 1 || d.Queue.Slots < 1 {
		t.Errorf("runners %+v queue %+v", d.Runners, d.Queue)
	}
	body, _ := json.Marshal(d)
	if strings.Contains(string(body), "their task") || strings.Contains(string(body), "secret prompt") {
		t.Errorf("leaked: %s", body)
	}

	// Public mode: no titles, prompts, names, reasons or costs.
	d, _ = getWallJSON(t, h, "/api/wall?privacy=1")
	body, _ = json.Marshal(d)
	for _, secret := range []string{"Building it", "old review", `"title":"landed"`, "XSS in form", "secret", "a.go", `"` + web.Name + `"`, "· " + web.Name} {
		if strings.Contains(string(body), secret) {
			t.Errorf("public mode shows %q", secret)
		}
	}
	if d.Running[0].CostUSD != nil || d.Today.SpendUSD != nil || !d.Privacy || d.Locked {
		t.Errorf("public mode shows costs: %+v %+v", d.Running[0], d.Today)
	}
	if !strings.HasPrefix(d.Running[0].Project, "Project ") || d.Running[0].SentBack != "merge conflict" || d.Needs[2].Reason != "" {
		t.Errorf("public mode: %+v %+v", d.Running[0], d.Needs[2])
	}
	// ...and projects are found by id only, not by guessing names.
	if _, code := getWallJSON(t, h, "/api/wall?privacy=1&project="+web.Name); code != http.StatusNotFound {
		t.Errorf("public mode, project by name: %d", code)
	}
	if d, code := getWallJSON(t, h, "/api/wall?privacy=1&project="+web.ID); code != http.StatusOK || d.Project != d.Running[0].Project {
		t.Errorf("public mode, project by id: %d %q", code, d.Project)
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

// Review tasks with an open pull request are listed only while a person
// has to act on GitHub; the Review stage still counts them all.
func TestWallNeedsPRReviewState(t *testing.T) {
	parallel(t)

	s, web := wallServer(t)
	h := loginAs(t, s.Routes(), "u", "p")

	pr := func(title string, n int, waits string, since *time.Time, fn func(*model.PullRequest)) {
		setTodo(t, s, web, func(x *model.Todo) {
			x.Title, x.Status, x.FinishedAt = title, model.StatusReview, at(-3*time.Hour)
			x.PR = &model.PullRequest{Number: n, State: "OPEN", Waits: waits, WaitsSince: since}
			if fn != nil {
				fn(x.PR)
			}
		})
	}
	pr("draft", 1, "", nil, func(p *model.PullRequest) { p.Draft, p.Status = true, "Waiting for checks: Go (0/1 passed)" })
	pr("changes requested", 2, "", nil, func(p *model.PullRequest) { p.Review = model.PRChangesRequested })
	pr("approved, checks running", 3, "", nil, func(p *model.PullRequest) { p.Review = model.PRApproved })
	pr("awaiting review", 4, model.PRWaitReview, at(-10*time.Minute), func(p *model.PullRequest) { p.Review = model.PRReviewPending })
	pr("refused merge", 5, model.PRWaitMerge, at(-2*time.Hour), func(p *model.PullRequest) { p.Review = model.PRApproved; p.HandledSHA = "abc" })
	pr("not noted when", 6, model.PRWaitReview, nil, nil)
	setTodo(t, s, web, func(x *model.Todo) {
		x.Title, x.Status, x.FinishedAt = "no PR", model.StatusReview, at(-5*time.Minute)
	})

	d, code := getWallJSON(t, h, "/api/wall")
	if code != http.StatusOK {
		t.Fatalf("GET /api/wall: %d", code)
	}
	if d.Stages.Review != 7 || d.Stages.ReviewGitHub != 3 {
		t.Errorf("stages %+v", d.Stages)
	}
	got := map[string]wallNeed{}
	for _, n := range d.Needs {
		got[n.Title] = n
	}
	if len(d.Needs) != 4 {
		t.Errorf("needs %+v", d.Needs)
	}
	// Oldest first: by when the PR started waiting, else when Claude finished.
	if len(d.Needs) == 4 && (d.Needs[0].Title != "not noted when" || d.Needs[1].Title != "refused merge" ||
		d.Needs[2].Title != "awaiting review" || d.Needs[3].Title != "no PR") {
		t.Errorf("order %+v", d.Needs)
	}
	if n := got["awaiting review"]; n.PR != 4 || n.Waits != model.PRWaitReview || n.Old || time.Since(n.Since) > time.Hour {
		t.Errorf("awaiting review %+v", n)
	}
	if n := got["refused merge"]; n.PR != 5 || n.Waits != model.PRWaitMerge || !n.Old || time.Since(n.Since) > 3*time.Hour-time.Minute {
		t.Errorf("refused merge %+v", n)
	}
	if n := got["not noted when"]; !n.Old || time.Since(n.Since) < 3*time.Hour-time.Minute {
		t.Errorf("finish time fallback %+v", n)
	}
	if n := got["no PR"]; n.PR != 0 || n.Waits != "" || n.Old {
		t.Errorf("no PR %+v", n)
	}
	if d.Stages.ReviewOldest == nil || time.Since(*d.Stages.ReviewOldest) < 3*time.Hour-time.Minute {
		t.Errorf("oldest review %v", d.Stages.ReviewOldest)
	}
}

func TestWallActivity(t *testing.T) {
	parallel(t)

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
	parallel(t)

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
	parallel(t)

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
	other, _, err := s.Store.Accounts().CreateWallToken(two.ID, owner.User, "their TV", false, time.Now())
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

	// A public token's wall is always in public mode.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/wall/tokens", strings.NewReader(`{"public":true}`)))
	var pub struct {
		Token  store.WallToken `json:"token"`
		Secret string          `json:"secret"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &pub)
	if w.Code != http.StatusCreated || !pub.Token.Public || pub.Token.Name != "Public wall" {
		t.Fatalf("public token: %d %s", w.Code, w.Body)
	}
	for _, url := range []string{"/api/wall?token=" + pub.Secret, "/api/wall?privacy=0&token=" + pub.Secret} {
		d, code := getWallJSON(t, routes, url)
		if code != http.StatusOK || !d.Privacy || !d.Locked || len(d.Running) != 1 || d.Running[0].Title == "ours" || d.Running[0].Project == web.Name {
			t.Errorf("%s: %d %+v", url, code, d.Running)
		}
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/wall/tokens/"+itoa(pub.Token.ID), nil))

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

// A running card carries its project's icon, at a URL a wall token may load;
// public mode has none, and a public token can't load it.
func TestWallProjectIcon(t *testing.T) {
	parallel(t)

	s, web := wallServer(t)
	s.UploadRoot = t.TempDir()
	routes := s.Routes()
	h := loginAs(t, routes, "u", "p")
	w := do(t, h, "POST", "/api/uploads?name=icon.svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`)
	var up model.Attachment
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &up) != nil {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	if _, err := raw(s).UpdateProject(web.ID, func(x *model.Project) error { x.Icon = up.ID + "/" + up.Name; return nil }); err != nil {
		t.Fatal(err)
	}
	setTodo(t, s, web, func(x *model.Todo) { x.Status, x.StartedAt = model.StatusInProgress, at(-time.Minute) })

	d, _ := getWallJSON(t, h, "/api/wall")
	if len(d.Running) != 1 || !strings.HasPrefix(d.Running[0].Icon, "/api/wall/icon?") {
		t.Fatalf("running: %+v", d.Running)
	}
	icon := d.Running[0].Icon
	if d, _ := getWallJSON(t, h, "/api/wall?privacy=1"); d.Running[0].Icon != "" {
		t.Errorf("public mode's icon: %q", d.Running[0].Icon)
	}
	if w := do(t, h, "GET", icon, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<svg") {
		t.Errorf("signed in: %d %s", w.Code, w.Body)
	}
	if w := do(t, routes, "GET", icon, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("signed out: %d", w.Code)
	}

	owner := testOwner(t, s)
	tok, _, err := s.Store.Accounts().CreateWallToken(owner.Org, owner.User, "TV", false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if w := do(t, routes, "GET", icon+"&token="+tok, ""); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/svg+xml" {
		t.Errorf("with a wall token: %d %s", w.Code, w.Body)
	}
	pub, _, err := s.Store.Accounts().CreateWallToken(owner.Org, owner.User, "lobby", true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if w := do(t, routes, "GET", icon+"&token="+pub, ""); w.Code != http.StatusNotFound {
		t.Errorf("with a public wall token: %d", w.Code)
	}
	// Another org's token doesn't reach our project.
	two, err := s.Auth.Accounts().CreateOrg(owner.User, "Two")
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := s.Store.Accounts().CreateWallToken(two.ID, owner.User, "their TV", false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if w := do(t, routes, "GET", icon+"&token="+other, ""); w.Code != http.StatusNotFound {
		t.Errorf("another org's token: %d", w.Code)
	}
}

// A row under "Needs a human" names who should act, with their avatar from
// a path a wall token may load; public mode names no one.
func TestWallNeedsPerson(t *testing.T) {
	parallel(t)

	s, web := wallServer(t)
	routes := s.Routes()
	h := loginAs(t, routes, "u", "p")
	owner := testOwner(t, s)
	bob := member(t, s, owner.Org, "bob", store.RoleMember)
	var png bytes.Buffer
	if err := pngEncode(&png); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Accounts().SetAvatar(bob, png.Bytes(), time.Now()); err != nil {
		t.Fatal(err)
	}
	setTodo(t, s, web, func(x *model.Todo) {
		x.Title, x.Status, x.FinishedAt, x.CreatedBy = "bob's review", model.StatusReview, at(-5*time.Minute), bob
	})
	// A failure is for whoever started its latest run.
	setTodo(t, s, web, func(x *model.Todo) {
		x.Title, x.Status, x.FinishedAt, x.CreatedBy, x.RunBy = "rerun by owner", model.StatusFailed, at(-time.Minute), bob, owner.User
	})
	setTodo(t, s, web, func(x *model.Todo) { x.Title, x.Status, x.FinishedAt = "by Dash", model.StatusFailed, at(-time.Minute) })

	d, _ := getWallJSON(t, h, "/api/wall")
	got := map[string]wallNeed{}
	for _, n := range d.Needs {
		got[n.Title] = n
	}
	ownerName := ""
	if u, err := s.Store.Accounts().User(owner.User); err == nil {
		ownerName = u.Username
	}
	if n := got["bob's review"]; n.Person != "bob" || !strings.HasPrefix(n.Avatar, "/api/wall/avatar?") {
		t.Errorf("review %+v", n)
	}
	if n := got["rerun by owner"]; n.Person != ownerName || ownerName == "" || n.Avatar != "" {
		t.Errorf("failure %+v, want %q without an avatar", n, ownerName)
	}
	if n := got["by Dash"]; n.Person != "" || n.Avatar != "" {
		t.Errorf("Dash's task %+v", n)
	}
	avatar := got["bob's review"].Avatar
	w := do(t, h, "GET", avatar, "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("avatar signed in: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	// A browser that has the picture isn't sent it again.
	if etag := w.Header().Get("ETag"); etag == "" {
		t.Error("avatar without an ETag")
	} else {
		r := httptest.NewRequest("GET", avatar, nil)
		r.Header.Set("If-None-Match", etag)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
			t.Errorf("avatar with If-None-Match: %d, %d bytes", w.Code, w.Body.Len())
		}
	}
	if w := do(t, routes, "GET", avatar, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("avatar signed out: %d", w.Code)
	}
	tok, _, _ := s.Store.Accounts().CreateWallToken(owner.Org, owner.User, "TV", false, time.Now())
	if w := do(t, routes, "GET", avatar+"&token="+tok, ""); w.Code != http.StatusOK {
		t.Errorf("avatar with a wall token: %d", w.Code)
	}
	pub, _, _ := s.Store.Accounts().CreateWallToken(owner.Org, owner.User, "lobby", true, time.Now())
	if w := do(t, routes, "GET", avatar+"&token="+pub, ""); w.Code != http.StatusNotFound {
		t.Errorf("avatar with a public wall token: %d", w.Code)
	}
	// Someone outside the org isn't served, even by id.
	two, _ := s.Auth.Accounts().CreateOrg(owner.User, "Two")
	other, _, _ := s.Store.Accounts().CreateWallToken(two.ID, owner.User, "their TV", false, time.Now())
	if w := do(t, routes, "GET", avatar+"&token="+other, ""); w.Code != http.StatusNotFound {
		t.Errorf("avatar with another org's token: %d", w.Code)
	}

	d, _ = getWallJSON(t, h, "/api/wall?privacy=1")
	body, _ := json.Marshal(d)
	if strings.Contains(string(body), `"person"`) || strings.Contains(string(body), "avatar?") {
		t.Errorf("public mode names people: %s", body)
	}
}

func pngEncode(w *bytes.Buffer) error {
	return png.Encode(w, image.NewRGBA(image.Rect(0, 0, 2, 2)))
}

// ?nospend=1 leaves every cost out of the data, not only off the screen.
func TestWallNoSpend(t *testing.T) {
	parallel(t)

	s, web := wallServer(t)
	h := loginAs(t, s.Routes(), "u", "p")
	setTodo(t, s, web, func(x *model.Todo) {
		x.Title, x.Status, x.StartedAt, x.CostUSD = "spending", model.StatusInProgress, at(-time.Minute), 4.25
		x.RunStats = []model.TodoRun{{At: *at(-25 * time.Hour), CostUSD: 3}}
	})
	d, _ := getWallJSON(t, h, "/api/wall")
	if d.Running[0].CostUSD == nil || d.Today.SpendUSD == nil || d.Today.Avg.SpendUSD == nil || d.HideSpend {
		t.Fatalf("costs missing on a signed-in wall: %+v %+v", d.Running[0], d.Today)
	}
	d, _ = getWallJSON(t, h, "/api/wall?nospend=1")
	body, _ := json.Marshal(d)
	if !d.HideSpend || strings.Contains(string(body), "spend_usd") || strings.Contains(string(body), "cost_usd") {
		t.Errorf("nospend: %s", body)
	}
	if d.Running[0].Title != "spending" {
		t.Errorf("nospend hides more than costs: %+v", d.Running[0])
	}
}

// Just landed: the newest merges with something in them, newest first;
// Today's averages: the last 7 days up to this time of day.
func TestWallLandedAndAverages(t *testing.T) {
	parallel(t)

	s, web := wallServer(t)
	h := loginAs(t, s.Routes(), "u", "p")
	merged := func(title string, ago time.Duration, runs int, fn func(*model.Todo)) {
		setTodo(t, s, web, func(x *model.Todo) {
			x.Title, x.Status, x.FinishedAt, x.Runs = title, model.StatusMerged, at(-ago), runs
			if fn != nil {
				fn(x)
			}
		})
	}
	merged("today", time.Minute, 1, nil)
	merged("closed, nothing landed", 2*time.Minute, 1, func(x *model.Todo) { x.NoChanges = true })
	merged("yesterday, earlier", 25*time.Hour, 1, nil)
	merged("yesterday, later", 23*time.Hour, 1, nil) // later in the day than now: not in the average
	merged("two days ago", 49*time.Hour, 2, nil)
	merged("old", 9*24*time.Hour, 1, nil)
	merged("older", 10*24*time.Hour, 1, nil)
	setTodo(t, s, web, func(x *model.Todo) {
		x.Status, x.StartedAt = model.StatusInProgress, at(-time.Minute)
		x.RunStats = []model.TodoRun{{At: *at(-26 * time.Hour), CostUSD: 7}, {At: *at(-22 * time.Hour), CostUSD: 100}}
	})

	d, _ := getWallJSON(t, h, "/api/wall?tz="+middayTZ())
	var titles []string
	for _, l := range d.Landed {
		titles = append(titles, l.Title)
	}
	if strings.Join(titles, ",") != "today,yesterday, later,yesterday, earlier,two days ago,old" {
		t.Errorf("landed %q", titles)
	}
	if d.Landed[0].Project != web.Name || time.Since(d.Landed[0].At) > 2*time.Minute {
		t.Errorf("landed %+v", d.Landed[0])
	}
	if a := d.Today.Avg; a.Merged != 2.0/7 || a.FirstTry == nil || *a.FirstTry != 0.5 || a.SpendUSD == nil || *a.SpendUSD != 1 {
		t.Errorf("avg %+v", a)
	}
	if d.Week.Since.Weekday() != time.Monday || d.Week.Merged < d.Today.Merged {
		t.Errorf("week %+v", d.Week)
	}
	d, _ = getWallJSON(t, h, "/api/wall?privacy=1")
	if len(d.Landed) != 5 || strings.Contains(d.Landed[0].Title, "today") || !strings.HasPrefix(d.Landed[0].Project, "Project ") {
		t.Errorf("public mode's landed %+v", d.Landed)
	}
}

// A running card gets how long the project's comparable runs took.
func TestWallRunTimes(t *testing.T) {
	parallel(t)

	s, web := wallServer(t)
	h := loginAs(t, s.Routes(), "u", "p")
	setTodo(t, s, web, func(x *model.Todo) {
		x.Status, x.FinishedAt, x.Runs = model.StatusMerged, at(-time.Hour), 2
		x.RunStats = []model.TodoRun{{At: *at(-3 * time.Hour), Kind: model.RunFirst, Seconds: 600}, {At: *at(-2 * time.Hour), Kind: model.RunNotes, Seconds: 120}}
	})
	setTodo(t, s, web, func(x *model.Todo) {
		x.Status, x.FinishedAt, x.Runs = model.StatusMerged, at(-time.Hour), 1
		x.RunStats = []model.TodoRun{{At: *at(-90 * time.Minute), Kind: model.RunFirst, Seconds: 900}}
	})
	// From before run times were kept: a task in Review's last run.
	setTodo(t, s, web, func(x *model.Todo) {
		x.Status, x.Runs, x.StartedAt, x.FinishedAt = model.StatusReview, 1, at(-5*time.Hour), at(-5*time.Hour+300*time.Second)
	})
	setTodo(t, s, web, func(x *model.Todo) {
		x.Title, x.Status, x.StartedAt = "first", model.StatusInProgress, at(-time.Minute)
	})
	setTodo(t, s, web, func(x *model.Todo) {
		x.Title, x.Status, x.StartedAt, x.Runs = "again", model.StatusInProgress, at(-2*time.Minute), 1
	})
	d, _ := getWallJSON(t, h, "/api/wall")
	got := map[string][]int{}
	for _, r := range d.Running {
		got[r.Title] = r.Took
	}
	if fmt.Sprint(got["first"]) != "[900 600 300]" || fmt.Sprint(got["again"]) != "[120]" {
		t.Errorf("took %v", got)
	}
}

func TestWallWindows(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	w := func(u float64, resetsIn time.Duration) usage.Window {
		return usage.Window{Name: "five_hour", Utilization: u, ResetsAt: now.Add(resetsIn)}
	}
	got := wallWindows([]usage.Window{
		w(0.5, 3*time.Hour),     // 2 h in, half used: full in 2 h, before it resets
		w(0.2, 3*time.Hour),     // full in 8 h: resets first
		w(0.5, 290*time.Minute), // 10 min in: too early to tell
		w(1, time.Hour),         // at its limit
		{Name: "seven_day_opus", Utilization: 0.9, ResetsAt: now.Add(time.Hour)}, // unknown length
	}, now)
	if f := got[0].FullAt; f == nil || !f.Equal(now.Add(2*time.Hour)) || !got[0].RunsOut {
		t.Errorf("half used %+v", got[0])
	}
	if got[1].FullAt == nil || got[1].RunsOut {
		t.Errorf("slow %+v", got[1])
	}
	if got[2].FullAt != nil || got[2].RunsOut || got[4].FullAt != nil {
		t.Errorf("too early or unknown %+v %+v", got[2], got[4])
	}
	if !got[3].RunsOut {
		t.Errorf("at the limit %+v", got[3])
	}
	if got[0].Utilization != 0.5 || got[0].Name != "five_hour" {
		t.Errorf("the window itself %+v", got[0])
	}
}

func TestWallStatus(t *testing.T) {
	now := time.Now()
	recent, longAgo := now.Add(-time.Hour), now.Add(-72*time.Hour)
	cases := []struct {
		name   string
		d      wallData
		level  string
		label  string
		reason string
	}{
		{"nothing waits", wallData{Stages: wallStage{Running: 3}}, "ok", "All good", ""},
		{"a review waits", wallData{Stages: wallStage{Review: 2, ReviewGitHub: 1}, Needs: []wallNeed{{Kind: "review"}}}, "warn", "Waiting on people", ""},
		{"only on GitHub", wallData{Stages: wallStage{Review: 1, ReviewGitHub: 1}}, "ok", "All good", ""},
		{"a failure waits", wallData{Stages: wallStage{Failed: 1}}, "warn", "Waiting on people", ""},
		{"open issues", wallData{Issues: wallIssues{Open: 3}}, "warn", "Heads up", "3 open issues"},
		{"an old review", wallData{Stages: wallStage{Review: 1}, Needs: []wallNeed{{Kind: "review", Old: true}}}, "bad", "Needs action", "1 review waiting over 1 h"},
		{"an old failure", wallData{Stages: wallStage{Failed: 1}, Needs: []wallNeed{{Kind: "failed", Old: true}}}, "bad", "Needs action", "1 failure unseen over 30 min"},
		{"paused", wallData{Queue: wallQueue{Paused: true}}, "bad", "Needs action", "queue paused"},
		{"runner just went offline", wallData{Runners: []wallRunner{{Name: "tower", LastSeen: &recent}}}, "bad", "Needs action", "tower offline"},
		{"runner long gone", wallData{Runners: []wallRunner{{Name: "old", LastSeen: &longAgo}}}, "ok", "All good", ""},
		{"long gone, tasks wait for it", wallData{Runners: []wallRunner{{Name: "old", LastSeen: &longAgo, Waiting: 1}}}, "bad", "Needs action", "old offline"},
		{"usage high", wallData{Usage: wallUsage{Known: true, Windows: []wallWindow{{Window: usage.Window{Name: "seven_day", Utilization: 0.93}}}}}, "warn", "Heads up", "Claude 7d at 93%"},
		{"usage runs out", wallData{Usage: wallUsage{Known: true, Windows: []wallWindow{{Window: usage.Window{Name: "five_hour", Utilization: 0.6}, RunsOut: true, FullAt: ptr(now.Add(2 * time.Hour))}}}}, "warn", "Heads up", "Claude 5h 100% in ~2 h"},
		{"per account", wallData{Usage: wallUsage{Known: true, Accounts: []wallAccount{{Label: "a"}, {Label: "b", Windows: []wallWindow{{Window: usage.Window{Name: "five_hour", Utilization: 1}}}}}}}, "warn", "Heads up", "b: Claude 5h at its limit"},
	}
	for _, c := range cases {
		st := wallStatusOf(&c.d, now)
		if st.Level != c.level || st.Label != c.label || (c.reason != "" && (len(st.Reasons) == 0 || st.Reasons[0] != c.reason)) || (c.reason == "" && len(st.Reasons) > 0) {
			t.Errorf("%s: %+v, want %s %q %q", c.name, st, c.level, c.label, c.reason)
		}
	}
	// Counts for the headline, and red reasons before amber ones.
	st := wallStatusOf(&wallData{Stages: wallStage{Running: 5, Review: 1, Failed: 1}, Issues: wallIssues{Open: 1}, Queue: wallQueue{Paused: true}}, now)
	if st.Running != 5 || st.Waiting != 2 || strings.Join(st.Reasons, "|") != "queue paused|1 open issue" {
		t.Errorf("headline %+v", st)
	}
}
