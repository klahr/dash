package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

func decodeCard(t *testing.T, w *httptest.ResponseRecorder) cardView {
	t.Helper()
	var v cardView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body, err)
	}
	return v
}

// A planner can't own a runner, so the work their Start makes is a
// developer's (whom they pick, else the card's first developer, else
// whoever Dash's own work goes to), and runs on that developer's runners.
// A member's Start stays theirs. Work no runner may take says so on the card.
func TestPlannerStartRunsForADeveloper(t *testing.T) {
	s, _, p := boardServer(t)
	h := s.Routes()
	a := loggedIn(t, h)
	ph, pat := plannerOf(t, s, h)
	joinAs(t, s, h, "dev", store.RoleMember)
	owner := ownerSession(t, s)
	ms, err := raw(s).Accounts().Members(owner.Org.ID)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(ms, func(m store.Member) bool { return m.Username == "dev" })
	if i < 0 {
		t.Fatalf("no dev in %+v", ms)
	}
	dev := ms[i]

	start := func(as http.Handler, c cardView, body string) (*httptest.ResponseRecorder, model.Todo) {
		t.Helper()
		w := do(t, as, "POST", "/api/planning/cards/"+c.ID+"/start", body)
		if w.Code != http.StatusOK {
			return w, model.Todo{}
		}
		var v struct {
			Started struct{ ID string } `json:"started"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		td, err := raw(s).GetTodo(v.Started.ID)
		if err != nil {
			t.Fatalf("the task: %v", err)
		}
		return w, td
	}
	card := func(body string) cardView {
		t.Helper()
		return decodeCard(t, do(t, ph, "POST", "/api/planning/cards", body))
	}
	proj := `"project":"` + p.ID + `"`

	// Picked by the planner.
	// It is a draft: nothing runs as the developer until they queue it.
	w, td := start(ph, card(`{"title":"One"}`), `{`+proj+`,"run_by":`+strconv.FormatInt(dev.ID, 10)+`}`)
	if w.Code != http.StatusOK || td.CreatedBy != dev.ID || !slices.Equal(td.WorkFor(), []int64{dev.ID}) || td.Status != model.StatusDraft {
		t.Fatalf("picked: %d %s; task %+v", w.Code, w.Body, td)
	}
	// Not a planner, nor someone outside the org.
	for _, bad := range []int64{pat.ID, 9999} {
		if w, _ := start(ph, card(`{"title":"Bad"}`), `{`+proj+`,"run_by":`+strconv.FormatInt(bad, 10)+`}`); w.Code != http.StatusBadRequest {
			t.Errorf("run_by %d: %d %s", bad, w.Code, w.Body)
		}
	}
	// The card's first developer (pat is a member of it too, but a planner).
	c := card(`{"title":"Two","members":[` + strconv.FormatInt(pat.ID, 10) + `,` + strconv.FormatInt(dev.ID, 10) + `]}`)
	if _, td := start(ph, c, `{`+proj+`}`); td.CreatedBy != dev.ID {
		t.Errorf("the card's developer: task %+v", td)
	}
	// Else whoever Dash's own work goes to: here the org's owner.
	if _, td := start(ph, card(`{"title":"Three"}`), `{`+proj+`}`); td.CreatedBy != owner.User.ID || slices.Contains(td.WorkFor(), pat.ID) {
		t.Errorf("no developer on the card: task %+v", td)
	}
	// A member's own Start is theirs; they don't pick someone else.
	if _, td := start(a, card(`{"title":"Four"}`), `{`+proj+`}`); td.CreatedBy != owner.User.ID || td.Status != model.StatusQueued {
		t.Errorf("a member's start: task %+v", td)
	}
	if w, _ := start(a, card(`{"title":"Five"}`), `{`+proj+`,"run_by":`+strconv.FormatInt(dev.ID, 10)+`}`); w.Code != http.StatusBadRequest {
		t.Errorf("a member picked who runs it: %d %s", w.Code, w.Body)
	}
	// A member's draft, with the prompt edited in the Start sheet: the task
	// waits as a draft and the card stays where it is until it is queued.
	c = card(`{"title":"Seven","description":"From the card"}`)
	w, td = start(a, c, `{`+proj+`,"prompt":"  Edited text  ","draft":true}`)
	if w.Code != http.StatusOK || td.Status != model.StatusDraft || td.Prompt != "Edited text" || td.Title != "Seven" {
		t.Errorf("a member's draft: %d %s; task %+v", w.Code, w.Body, td)
	}
	if got := decodeCard(t, w); got.Column != model.CardIdeas {
		t.Errorf("a draft's card moved to %s", got.Column)
	}
	// No prompt: the card's title and description.
	if _, td := start(a, card(`{"title":"Eight","description":"Why"}`), `{`+proj+`}`); td.Prompt != "Eight\n\nWhy" {
		t.Errorf("the card's prompt: %q", td.Prompt)
	}

	// No runner may take it: the card says what to do, not which runner.
	c = card(`{"title":"Six"}`)
	_, td = start(ph, c, `{`+proj+`}`)
	if _, err := raw(s).SetStatus(td.ID, model.StatusQueued, ""); err != nil { // the developer queued it
		t.Fatal(err)
	}
	for _, step := range []struct{ waiting, text string }{
		{"waiting: no runner of dev's has the labels any; laptop is u's runner and only takes u's tasks", "Waiting for a runner: ask a developer to take it"},
		{"no runner online: laptop is offline", "Waiting for a runner to come online"},
		{"waiting for a runner: laptop is busy (2 of 2 tasks)", "Waiting to start"},
	} {
		if _, err := raw(s).UpdateTodo(td.ID, func(x *model.Todo) error { x.Waiting = step.waiting; return nil }); err != nil {
			t.Fatal(err)
		}
		got := decodeCard(t, do(t, ph, "GET", "/api/planning/cards/"+c.ID, ""))
		if got.Status.Text != step.text || strings.Contains(got.Links[0].State, "laptop") {
			t.Errorf("waiting %q: card %+v, link %+v", step.waiting, got.Status, got.Links[0])
		}
	}
}

// A deleted task or goal is neither done nor counted: a card whose only
// work was deleted doesn't say "All done" or suggest Done, and one with a
// merged and a deleted task counts just the merged one.
func TestPlanningRemovedWorkIsNotDone(t *testing.T) {
	s, _, p := boardServer(t)
	h := s.Routes()
	a := loggedIn(t, h)
	ph, _ := plannerOf(t, s, h)

	c := decodeCard(t, do(t, ph, "POST", "/api/planning/cards", `{"title":"Email reminders"}`))
	gone, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "Reminder job", Prompt: "job"})
	if w := do(t, a, "POST", "/api/planning/cards/"+c.ID+"/links", `{"kind":"task","id":"`+gone.ID+`"}`); w.Code != http.StatusOK {
		t.Fatalf("link: %d %s", w.Code, w.Body)
	}
	if _, err := raw(s).DeleteTodo(gone.ID); err != nil {
		t.Fatal(err)
	}
	got := decodeCard(t, do(t, ph, "GET", "/api/planning/cards/"+c.ID, ""))
	if st := got.Status; st.Text != "Work was deleted" || got.Column == model.CardDone || st.Done != 0 || st.Total != 0 || st.Removed != 1 {
		t.Errorf("only work deleted: %+v", st)
	}
	if len(got.Links) != 1 || !got.Links[0].Removed || got.Links[0].State != "Removed (work was deleted)" {
		t.Errorf("removed link: %+v", got.Links)
	}

	merged, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "Reminder email", Prompt: "email"})
	for _, to := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress, model.StatusReview, model.StatusMerged} {
		if _, err := raw(s).UpdateTodo(merged.ID, func(x *model.Todo) error { x.Status = to; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if w := do(t, a, "POST", "/api/planning/cards/"+c.ID+"/links", `{"kind":"task","id":"`+merged.ID+`"}`); w.Code != http.StatusOK {
		t.Fatalf("link: %d %s", w.Code, w.Body)
	}
	got = decodeCard(t, do(t, ph, "GET", "/api/planning/cards/"+c.ID, ""))
	if st := got.Status; st.Text != "All done" || got.Column != model.CardDone || st.Done != 1 || st.Total != 1 || st.Removed != 1 || st.Progress != "" {
		t.Errorf("merged and deleted: %s %+v", got.Column, st)
	}
}

// A card started as a task is linked to it and moves to In progress; its
// status follows the task in an owner's words, and it moves to Done once
// the work is merged. A planner sees the same card without the task's id.
func TestPlanningCardFollowsItsWork(t *testing.T) {
	s, _, p := boardServer(t)
	h := s.Routes()
	a := loggedIn(t, h)
	ph, _ := plannerOf(t, s, h)

	w := do(t, ph, "POST", "/api/planning/cards", `{"title":"Export as PDF","description":"Reports, as **PDF**.","priority":"high","target":"1.4"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	c := decodeCard(t, w)
	if c.Column != model.CardIdeas || c.Status.Text != "Nothing started yet" || c.Priority != "high" {
		t.Fatalf("new card %+v", c)
	}
	if w := do(t, ph, "POST", "/api/planning/cards/"+c.ID+"/start", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("start without a project: %d %s", w.Code, w.Body)
	}
	w = do(t, ph, "POST", "/api/planning/cards/"+c.ID+"/start", `{"project":"`+p.ID+`","kind":"task"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	c = decodeCard(t, w)
	// A planner's task is a draft the developer queues: the card waits.
	if len(c.Links) != 1 || c.Links[0].ID != "" || c.Links[0].Title != "Export as PDF" || c.Column != model.CardIdeas || c.ProjectID != p.ID || c.Comments != 1 {
		t.Fatalf("started card, as the planner: %+v", c)
	}
	// The member sees the task's id; the task's prompt is the card.
	c = decodeCard(t, do(t, a, "GET", "/api/planning/cards/"+c.ID, ""))
	if len(c.Links) != 1 || c.Links[0].ID == "" {
		t.Fatalf("started card, as a member: %+v", c)
	}
	td, err := raw(s).GetTodo(c.Links[0].ID)
	if err != nil || td.Status != model.StatusDraft || !strings.Contains(td.Prompt, "Reports, as **PDF**.") || td.CreatedBy == 0 {
		t.Fatalf("the task: %+v %v", td, err)
	}
	// The developer queues it; the card moves to In progress.
	if w := do(t, a, "POST", "/api/todos/"+td.ID+"/enqueue", ""); w.Code != http.StatusOK {
		t.Fatalf("enqueue: %d %s", w.Code, w.Body)
	}
	td, _ = raw(s).GetTodo(td.ID)
	s.ObservePlanning(model.Event{Type: model.EvTodoUpdated, TodoID: td.ID, Data: td})
	deadline := time.Now().Add(5 * time.Second)
	for decodeCard(t, do(t, ph, "GET", "/api/planning/cards/"+c.ID, "")).Column != model.CardInProgress && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	for _, step := range []struct {
		to     model.TodoStatus
		text   string
		column model.CardColumn
	}{
		{model.StatusInProgress, "In progress", model.CardInProgress},
		{model.StatusReview, "Waiting for review", model.CardInProgress},
		{model.StatusFailed, "Failed, needs a developer", model.CardInProgress},
		{model.StatusMerged, "All done", model.CardDone},
	} {
		if _, err := raw(s).UpdateTodo(td.ID, func(x *model.Todo) error { x.Status = step.to; return nil }); err != nil {
			t.Fatal(err)
		}
		got := decodeCard(t, do(t, ph, "GET", "/api/planning/cards/"+c.ID, ""))
		if got.Status.Text != step.text || got.Column != step.column {
			t.Errorf("task %s: card %s %+v", step.to, got.Column, got.Status)
		}
	}

	// A second, unstarted task linked: progress counts both.
	td2, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "PDF fonts", Prompt: "fonts"})
	if w := do(t, a, "POST", "/api/planning/cards/"+c.ID+"/links", `{"kind":"task","id":"`+td2.ID+`"}`); w.Code != http.StatusOK {
		t.Fatalf("link: %d %s", w.Code, w.Body)
	}
	var list []cardView
	if err := json.Unmarshal(do(t, ph, "GET", "/api/planning/cards", "").Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if st := list[0].Status; st.Progress != "1 of 2 done" {
		t.Errorf("two tasks: %+v", st)
	}

	// Moves and comments.
	if w := do(t, ph, "POST", "/api/planning/cards/"+c.ID+"/move", `{"column":"done","position":0}`); w.Code != http.StatusOK || decodeCard(t, w).Column != model.CardDone {
		t.Errorf("move: %d %s", w.Code, w.Body)
	}
	if w := do(t, ph, "POST", "/api/planning/cards/"+c.ID+"/move", `{"column":"later"}`); w.Code != http.StatusBadRequest {
		t.Errorf("move to no column: %d", w.Code)
	}
	if w := do(t, ph, "POST", "/api/planning/cards/"+c.ID+"/comments", `{"text":"Customers ask weekly"}`); w.Code != http.StatusCreated {
		t.Fatalf("comment: %d %s", w.Code, w.Body)
	}
	got := decodeCard(t, do(t, a, "GET", "/api/planning/cards/"+c.ID, ""))
	// The first is the note pat's Start left: who made the task, for whom;
	// then Dash's, on why it moved the card to Done (not counted).
	if len(got.Discussion) != 3 || !strings.HasPrefix(got.Discussion[0].Text, "Started a task for u") || !got.Discussion[1].Dash ||
		got.Discussion[2].AuthorName != "pat" || got.Comments != 2 {
		t.Errorf("discussion %+v", got.Discussion)
	}
	// Only its author (or an admin) deletes a comment; pat is neither for u's.
	w = do(t, a, "POST", "/api/planning/cards/"+c.ID+"/comments", `{"text":"On it"}`)
	var mine model.CardComment
	_ = json.Unmarshal(w.Body.Bytes(), &mine)
	if w := do(t, ph, "DELETE", "/api/planning/cards/"+c.ID+"/comments/"+strconv.FormatInt(mine.ID, 10), ""); w.Code != http.StatusForbidden {
		t.Errorf("a planner deleted someone's comment: %d", w.Code)
	}
}

// Only a card's creator, its owner or an admin deletes it: another planner
// can't, so its linked work and the cards that depend on it keep their link.
func TestPlanningCardDeleteIsTheirs(t *testing.T) {
	s, _, _ := boardServer(t)
	h := s.Routes()
	a := loggedIn(t, h) // an owner of the org
	ph, pat := plannerOf(t, s, h)

	mine := decodeCard(t, do(t, a, "POST", "/api/planning/cards", `{"title":"Patient sign-up"}`))
	if w := do(t, ph, "DELETE", "/api/planning/cards/"+mine.ID, ""); w.Code != http.StatusForbidden {
		t.Errorf("a planner deleted someone's card: %d %s", w.Code, w.Body)
	}
	// Nor by making themselves its owner first, or taking its owner off.
	if w := do(t, ph, "PATCH", "/api/planning/cards/"+mine.ID, fmt.Sprintf(`{"owner":%d}`, pat.ID)); w.Code != http.StatusForbidden {
		t.Errorf("a planner made themselves owner of someone's card: %d %s", w.Code, w.Body)
	}
	if w := do(t, ph, "DELETE", "/api/planning/cards/"+mine.ID, ""); w.Code != http.StatusForbidden {
		t.Errorf("a planner deleted someone's card: %d %s", w.Code, w.Body)
	}
	owned := decodeCard(t, do(t, a, "POST", "/api/planning/cards", `{"title":"Cancel an appointment","owner":`+strconv.FormatInt(ownerSession(t, s).User.ID, 10)+`}`))
	if w := do(t, ph, "PATCH", "/api/planning/cards/"+owned.ID, `{"members":[]}`); w.Code != http.StatusForbidden {
		t.Errorf("a planner took the owner off someone's card: %d %s", w.Code, w.Body)
	}
	if w := do(t, ph, "PATCH", "/api/planning/cards/"+owned.ID, `{"title":"Cancel a booking"}`); w.Code != http.StatusOK {
		t.Errorf("a planner edits someone's card: %d %s", w.Code, w.Body)
	}
	if w := do(t, a, "PATCH", "/api/planning/cards/"+owned.ID, `{"owner":0}`); w.Code != http.StatusOK {
		t.Errorf("the owner gives up the card: %d %s", w.Code, w.Body)
	}
	if w := do(t, ph, "DELETE", "/api/planning/cards/nope", ""); w.Code != http.StatusNotFound {
		t.Errorf("delete no card: %d", w.Code)
	}

	theirs := decodeCard(t, do(t, ph, "POST", "/api/planning/cards", `{"title":"Book an appointment"}`))
	if w := do(t, ph, "DELETE", "/api/planning/cards/"+theirs.ID, ""); w.Code != http.StatusNoContent {
		t.Errorf("the creator deletes their card: %d %s", w.Code, w.Body)
	}
	if w := do(t, a, "DELETE", "/api/planning/cards/"+mine.ID, ""); w.Code != http.StatusNoContent {
		t.Errorf("an admin deletes a card: %d %s", w.Code, w.Body)
	}
}

// A planner's event stream carries Planning's events only: a linked task's
// change reaches them as planning.work, never as the task's own event.
func TestPlannerEventsArePlanningOnly(t *testing.T) {
	s, evs, p := boardServer(t)
	h := s.Routes()
	_, _ = plannerOf(t, s, h)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"pat","password":"pat-password"}`)))
	hdr := cookieHeader(srv.URL, w.Result().Cookies())
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/events", hdr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	waitFor(t, "the planner's dashboard", func() bool { return s.Hub.Clients() == 1 })

	// The test's store publishes to evs: what it published from here on
	// goes on to the hub as a running Dash's would.
	evs.mu.Lock()
	from := len(evs.evs)
	evs.mu.Unlock()
	org := ownerSession(t, s).Org.ID
	c, err := s.Store.ForOrg(org).CreateCard(model.Card{Title: "A", Column: model.CardIdeas})
	if err != nil {
		t.Fatal(err)
	}
	td, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "secret task", Prompt: "x"})
	if err := s.Store.ForOrg(org).LinkCard(c.ID, model.LinkTask, td.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := raw(s).SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	evs.mu.Lock()
	sent := append([]model.Event{}, evs.evs[from:]...)
	evs.mu.Unlock()
	if len(sent) < 4 {
		t.Fatalf("published %+v", sent)
	}
	for _, ev := range sent {
		s.Hub.Publish(ev)
	}
	want := []model.EventType{model.EvPlanningCard, model.EvPlanningCard, model.EvPlanningWork}
	for _, typ := range want {
		var ev struct {
			Type model.EventType `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := conn.ReadJSON(&ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type != typ || strings.Contains(string(ev.Data), "secret") {
			t.Fatalf("got %s %s, want %s", ev.Type, ev.Data, typ)
		}
	}
}

// A card's members are the org's members, added by a planner too: the
// people to choose from are the org's members, and the card carries its
// members' names and pictures. Someone outside the org is refused.
func TestPlanningCardMembers(t *testing.T) {
	s, _, _ := boardServer(t)
	h := s.Routes()
	a := loggedIn(t, h)
	ph, pat := plannerOf(t, s, h)
	admin, err := s.Auth.Accounts().EnvUser()
	if err != nil {
		t.Fatal(err)
	}
	me := admin.ID

	var people []struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Email    string `json:"email"`
	}
	w := do(t, ph, "GET", "/api/planning/people", "")
	if err := json.Unmarshal(w.Body.Bytes(), &people); err != nil || len(people) < 2 {
		t.Fatalf("people: %d %s", w.Code, w.Body)
	}
	found := false
	for _, p := range people {
		found = found || p.ID == pat.ID && p.Username == "pat"
		if p.Email != "" {
			t.Errorf("people show emails: %+v", p)
		}
	}
	if !found {
		t.Fatalf("pat isn't among the people: %s", w.Body)
	}

	c := decodeCard(t, do(t, a, "POST", "/api/planning/cards", `{"title":"Dark mode","members":[`+strconv.FormatInt(pat.ID, 10)+`]}`))
	if len(c.People) != 1 || c.People[0].ID != pat.ID || c.People[0].Username != "pat" {
		t.Fatalf("card with a member %+v", c)
	}
	if _, err := raw(s).Accounts().SetAvatar(pat.ID, tinyPNG(t), time.Now()); err != nil {
		t.Fatal(err)
	}
	c = decodeCard(t, do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, fmt.Sprintf(`{"members":[%d,%d]}`, pat.ID, me)))
	if len(c.People) != 2 || c.People[0].Avatar == 0 || c.People[1].ID != me {
		t.Errorf("two members, pat's picture: %+v", c.People)
	}
	if w := do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, `{"members":[999999]}`); w.Code != http.StatusBadRequest {
		t.Errorf("a stranger as a member: %d %s", w.Code, w.Body)
	}
	c = decodeCard(t, do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, `{"members":[]}`))
	if len(c.Members) != 0 || len(c.People) != 0 {
		t.Errorf("card without members %+v", c)
	}
}

// A card names the cards it depends on and those that depend on it, and
// counts those not done yet; a cycle is refused.
func TestPlanningCardDeps(t *testing.T) {
	s, _, _ := boardServer(t)
	h := s.Routes()
	ph, _ := plannerOf(t, s, h)

	a := decodeCard(t, do(t, ph, "POST", "/api/planning/cards", `{"title":"Accounts"}`))
	b := decodeCard(t, do(t, ph, "POST", "/api/planning/cards", `{"title":"Billing","depends_on":["`+a.ID+`"]}`))
	if len(b.Deps) != 1 || b.Deps[0].ID != a.ID || b.Deps[0].Title != "Accounts" || b.Deps[0].Done || b.Waiting != 1 {
		t.Fatalf("Billing depends on Accounts: %+v", b)
	}
	if w := do(t, ph, "PATCH", "/api/planning/cards/"+a.ID, `{"depends_on":["`+b.ID+`"]}`); w.Code != http.StatusBadRequest {
		t.Errorf("a cycle: %d %s", w.Code, w.Body)
	}
	a = decodeCard(t, do(t, ph, "GET", "/api/planning/cards/"+a.ID, ""))
	if len(a.NeededBy) != 1 || a.NeededBy[0].ID != b.ID || len(a.Deps) != 0 {
		t.Errorf("Accounts is needed by Billing: %+v", a)
	}
	if w := do(t, ph, "POST", "/api/planning/cards/"+a.ID+"/move", `{"column":"done"}`); w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body)
	}
	b = decodeCard(t, do(t, ph, "GET", "/api/planning/cards/"+b.ID, ""))
	if b.Waiting != 0 || !b.Deps[0].Done {
		t.Errorf("Accounts is done: %+v", b)
	}
	b = decodeCard(t, do(t, ph, "PATCH", "/api/planning/cards/"+b.ID, `{"depends_on":[]}`))
	if len(b.Deps) != 0 || len(b.DependsOn) != 0 {
		t.Errorf("no dependencies: %+v", b)
	}
}

// A card takes tags when it is made and when it changes, cleaned; a
// planner can tag cards too, and list_cards finds them by tag.
func TestPlanningCardTags(t *testing.T) {
	s, _, _ := boardServer(t)
	h := s.Routes()
	a := loggedIn(t, h)
	ph, _ := plannerOf(t, s, h)

	c := decodeCard(t, do(t, a, "POST", "/api/planning/cards", `{"title":"Dark mode","tags":["ux"," Q3 ","UX"]}`))
	if strings.Join(c.Tags, ",") != "ux,Q3" {
		t.Fatalf("tags of a new card %q", c.Tags)
	}
	c = decodeCard(t, do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, `{"tags":["Q3","backend"]}`))
	if strings.Join(c.Tags, ",") != "Q3,backend" {
		t.Errorf("tags after a planner's change %q", c.Tags)
	}
	c = decodeCard(t, do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, `{"title":"Dark theme"}`))
	if strings.Join(c.Tags, ",") != "Q3,backend" {
		t.Errorf("a change without tags changed them: %q", c.Tags)
	}
	if w := do(t, a, "PATCH", "/api/planning/cards/"+c.ID, `{"tags":["`+strings.Repeat("x", model.MaxCardTag+1)+`"]}`); w.Code != http.StatusBadRequest {
		t.Errorf("a tag that is too long: %d %s", w.Code, w.Body)
	}
	decodeCard(t, do(t, a, "POST", "/api/planning/cards", `{"title":"Untagged"}`))

	rows, err := board{s.as(ownerCaller(t, s))}.Cards("", "")
	if err != nil {
		t.Fatal(err)
	}
	tagged := 0
	for _, r := range rows {
		if r.ID == c.ID && strings.Join(r.Tags, ",") == "Q3,backend" {
			tagged++
		}
	}
	if tagged != 1 {
		t.Errorf("list_cards rows %+v", rows)
	}

	c = decodeCard(t, do(t, a, "PATCH", "/api/planning/cards/"+c.ID, `{"tags":[]}`))
	if c.Tags == nil || len(c.Tags) != 0 {
		t.Errorf("tags taken off: %#v", c.Tags)
	}
}

// Two planners editing one card: the second edit, made on the version both
// opened, is refused (409) instead of replacing the first; it goes through
// with the new version, or with none.
func TestPlanningCardVersionConflict(t *testing.T) {
	s, _, _ := boardServer(t)
	h := s.Routes()
	a := loggedIn(t, h)
	ph, _ := plannerOf(t, s, h)

	c := decodeCard(t, do(t, a, "POST", "/api/planning/cards", `{"title":"Export","description":"First draft"}`))
	if c.Version == "" {
		t.Fatal("a card without a version")
	}
	pia := decodeCard(t, do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, `{"version":"`+c.Version+`","description":"Pia's criteria"}`))
	if pia.Description != "Pia's criteria" || pia.Version == c.Version {
		t.Fatalf("Pia's edit: %q %q", pia.Description, pia.Version)
	}
	w := do(t, a, "PATCH", "/api/planning/cards/"+c.ID, `{"version":"`+c.Version+`","description":"Paul's criteria"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "changed the card") {
		t.Fatalf("Paul's stale edit: %d %s", w.Code, w.Body)
	}
	if got := decodeCard(t, do(t, a, "GET", "/api/planning/cards/"+c.ID, "")); got.Description != "Pia's criteria" || got.Version != pia.Version {
		t.Errorf("a refused edit changed the card: %q %q", got.Description, got.Version)
	}
	// A move doesn't change the version: an edit made before it still goes through.
	do(t, a, "POST", "/api/planning/cards/"+c.ID+"/move", `{"column":"planned"}`)
	paul := decodeCard(t, do(t, a, "PATCH", "/api/planning/cards/"+c.ID, `{"version":"`+pia.Version+`","description":"Both"}`))
	if paul.Description != "Both" {
		t.Errorf("an edit on the current version: %q", paul.Description)
	}
	if got := decodeCard(t, do(t, a, "PATCH", "/api/planning/cards/"+c.ID, `{"priority":"high"}`)); got.Priority != "high" {
		t.Errorf("an edit without a version: %+v", got)
	}
}

// tinyPNG is a small picture, for a profile picture.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Planning's projects carry their icons, at a URL a planner may load; a
// project without one has none.
func TestPlanningProjectIcons(t *testing.T) {
	s, _, p := boardServer(t)
	s.UploadRoot = t.TempDir()
	h := s.Routes()
	a := loggedIn(t, h)
	ph, _ := plannerOf(t, s, h)

	w := do(t, a, "POST", "/api/uploads?name=icon.svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`)
	var up model.Attachment
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &up) != nil {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	if _, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.Icon = up.ID + "/" + up.Name; x.Color = "#336699"; return nil }); err != nil {
		t.Fatal(err)
	}

	var projects []struct{ ID, Name, Color, Icon string }
	if w := do(t, ph, "GET", "/api/planning/projects", ""); json.Unmarshal(w.Body.Bytes(), &projects) != nil {
		t.Fatalf("projects: %d %s", w.Code, w.Body)
	}
	icon := ""
	for _, x := range projects {
		if x.ID == p.ID {
			icon = x.Icon
			if x.Color != "#336699" {
				t.Errorf("color: %q", x.Color)
			}
		} else if x.Icon != "" {
			t.Errorf("%s has no icon but got %q", x.Name, x.Icon)
		}
	}
	if !strings.HasPrefix(icon, "/api/planning/projects/"+p.ID+"/icon?") {
		t.Fatalf("icon URL: %q", icon)
	}
	w = do(t, ph, "GET", icon, "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(w.Body.String(), "<svg") {
		t.Fatalf("a planner's icon: %d %q %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	for _, x := range projects {
		if x.ID != p.ID {
			if w := do(t, ph, "GET", "/api/planning/projects/"+x.ID+"/icon", ""); w.Code != http.StatusNotFound {
				t.Errorf("no icon: %d", w.Code)
			}
		}
	}
}

// A card's files: a planner attaches one and sees it only through the card
// (never at /api/uploads), a task started from the card gets them for
// Claude, and taking one off the card stops serving it there.
func TestPlanningCardFiles(t *testing.T) {
	s, _, p := boardServer(t)
	s.UploadRoot = t.TempDir()
	h := s.Routes()
	a := loggedIn(t, h)
	ph, _ := plannerOf(t, s, h)

	c := decodeCard(t, do(t, ph, "POST", "/api/planning/cards", `{"title":"New logo"}`))
	if c.Files == nil || len(c.Files) != 0 {
		t.Fatalf("a new card's files: %#v", c.Files)
	}
	other := decodeCard(t, do(t, ph, "POST", "/api/planning/cards", `{"title":"Other"}`))

	w := do(t, ph, "POST", "/api/planning/cards/"+c.ID+"/files?name=sketch.png", string(tinyPNG(t)))
	if w.Code != http.StatusCreated {
		t.Fatalf("attach: %d %s", w.Code, w.Body)
	}
	c = decodeCard(t, w)
	if len(c.Files) != 1 || c.Files[0].Name != "sketch.png" || c.Files[0].Type != "image/png" || c.Files[0].Size == 0 || c.Files[0].AddedBy == 0 {
		t.Fatalf("attached: %+v", c.Files)
	}
	f := c.Files[0]
	if strings.Contains(w.Body.String(), s.UploadRoot) {
		t.Errorf("the card shows where its file is stored: %s", w.Body)
	}
	if w := do(t, ph, "POST", "/api/planning/cards/"+c.ID+"/files?name=run.exe", "MZ"); w.Code != http.StatusBadRequest {
		t.Errorf("an .exe: %d %s", w.Code, w.Body)
	}

	url := "/api/planning/cards/" + c.ID + "/files/" + f.ID + "/" + f.Name
	if w := do(t, ph, "GET", url, ""); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Content-Security-Policy") == "" {
		t.Errorf("a planner's file: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, bad := range []string{
		"/api/planning/cards/" + c.ID + "/files/" + f.ID + "/other.png",     // not its name
		"/api/planning/cards/" + other.ID + "/files/" + f.ID + "/" + f.Name, // not that card's
	} {
		if w := do(t, ph, "GET", bad, ""); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	if w := do(t, ph, "GET", "/api/uploads/"+f.ID+"/"+f.Name, ""); w.Code != http.StatusForbidden {
		t.Errorf("a planner read /api/uploads: %d", w.Code)
	}

	// Listed with the card, and handed to the task Start makes.
	var list []cardView
	if err := json.Unmarshal(do(t, a, "GET", "/api/planning/cards", "").Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, x := range list {
		if x.ID == c.ID && (len(x.Files) != 1 || x.Files[0].ID != f.ID) {
			t.Errorf("listed: %+v", x.Files)
		}
	}
	c = decodeCard(t, do(t, ph, "POST", "/api/planning/cards/"+c.ID+"/start", `{"project":"`+p.ID+`","kind":"task"}`))
	c = decodeCard(t, do(t, a, "GET", "/api/planning/cards/"+c.ID, ""))
	td, err := raw(s).GetTodo(c.Links[0].ID)
	if err != nil || len(td.Attachments) != 1 || td.Attachments[0].ID != f.ID || td.Attachments[0].Path == "" {
		t.Fatalf("the task's attachments: %+v %v", td.Attachments, err)
	}

	if w := do(t, ph, "DELETE", "/api/planning/cards/"+c.ID+"/files/"+f.ID, ""); w.Code != http.StatusOK || len(decodeCard(t, w).Files) != 0 {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
	if w := do(t, ph, "GET", url, ""); w.Code != http.StatusNotFound {
		t.Errorf("a removed file: %d", w.Code)
	}
	if w := do(t, ph, "DELETE", "/api/planning/cards/"+c.ID+"/files/"+f.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("remove again: %d", w.Code)
	}
}

// A card's status names what its work is waiting on: queued work is
// "Waiting to start" on its line and on the card alike; a stopped goal or a
// task stopped back to a draft is "Stopped", never done; a goal out of
// budget says so. A draft linked to a planned card moves the card to In
// progress once it is queued.
func TestPlanningCardStatusWords(t *testing.T) {
	s, _, p := boardServer(t)
	h := s.Routes()
	a := loggedIn(t, h)
	card := func(title string) cardView {
		t.Helper()
		w := do(t, a, "POST", "/api/planning/cards", `{"title":"`+title+`"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body)
		}
		c := decodeCard(t, w)
		if w := do(t, a, "POST", "/api/planning/cards/"+c.ID+"/move", `{"column":"planned","position":0}`); w.Code != http.StatusOK {
			t.Fatalf("move: %d %s", w.Code, w.Body)
		}
		return c
	}
	link := func(c cardView, kind, id string) cardView {
		t.Helper()
		w := do(t, a, "POST", "/api/planning/cards/"+c.ID+"/links", `{"kind":"`+kind+`","id":"`+id+`"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("link: %d %s", w.Code, w.Body)
		}
		return decodeCard(t, w)
	}
	get := func(c cardView) cardView {
		return decodeCard(t, do(t, a, "GET", "/api/planning/cards/"+c.ID, ""))
	}

	// A draft linked later, then queued: the card moves, and says the same
	// as its line.
	c := card("Dark mode")
	td, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "Dark mode", Prompt: "dark"})
	if got := link(c, model.LinkTask, td.ID); got.Column != model.CardPlanned || got.Status.Text != "Not started" {
		t.Fatalf("draft linked: %s %+v", got.Column, got.Status)
	}
	td, err := raw(s).SetStatus(td.ID, model.StatusQueued, "")
	if err != nil {
		t.Fatal(err)
	}
	s.ObservePlanning(model.Event{Type: model.EvTodoUpdated, TodoID: td.ID, Data: td})
	deadline := time.Now().Add(5 * time.Second)
	for get(c).Column != model.CardInProgress && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := get(c)
	if got.Column != model.CardInProgress || got.Status.Text != "Waiting to start" || got.Links[0].State != "Waiting to start" {
		t.Errorf("queued: %s %+v %+v", got.Column, got.Status, got.Links)
	}
	// Ran, then stopped back to a draft.
	if _, err := raw(s).UpdateTodo(td.ID, func(x *model.Todo) error { x.Status, x.Runs = model.StatusDraft, 1; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := get(c); got.Status.Text != "Stopped" || got.Links[0].State != "Stopped" {
		t.Errorf("stopped task: %+v %+v", got.Status, got.Links)
	}

	// Goals: stopped by the user, out of budget, finished.
	g, err := raw(s).CreateGoal(model.Goal{ProjectID: p.ID, Owner: testOwner(t, s).User, Title: "Faster search", Mode: model.GoalQueue, Status: model.GoalWorking})
	if err != nil {
		t.Fatal(err)
	}
	c = card("Faster search")
	link(c, model.LinkGoal, g.ID)
	for _, step := range []struct {
		set  func(*model.Goal)
		text string
		done bool
	}{
		{func(x *model.Goal) { x.Status = model.GoalOutOfBudget }, "Out of budget, needs its owner", false},
		{func(x *model.Goal) { x.Status, x.Note, x.Stopped = model.GoalDone, model.GoalStoppedNote, true }, "Stopped", false},
		{func(x *model.Goal) { x.Status, x.Note, x.Stopped = model.GoalDone, "All shipped.", false }, "All done", true},
	} {
		if _, err := raw(s).UpdateGoal(g.ID, func(x *model.Goal) error { step.set(x); return nil }); err != nil {
			t.Fatal(err)
		}
		got := get(c)
		if got.Status.Text != step.text || (got.Column == model.CardDone) != step.done {
			t.Errorf("want %q: %s %+v %+v", step.text, got.Column, got.Status, got.Links)
		}
	}
}

// The card calls tell an API user what they left out or got wrong instead of
// answering a success: a field the call doesn't take is refused, PATCH
// refuses column (moves are …/move), priority "none" clears the priority,
// and columns match in any case.
func TestPlanningCardRequestsAreStrict(t *testing.T) {
	s, _, _ := boardServer(t)
	h := s.Routes()
	ph, _ := plannerOf(t, s, h)

	w := do(t, ph, "POST", "/api/planning/cards", `{"title":"Export","estimate":3,"assignee":"ann"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "estimate") {
		t.Errorf("unknown field on create: %d %s", w.Code, w.Body)
	}
	w = do(t, ph, "POST", "/api/planning/cards", `{"title":"Export","column":" Planned ","priority":"High"}`)
	c := decodeCard(t, w)
	if w.Code != http.StatusCreated || c.Column != model.CardPlanned || c.Priority != model.CardHigh {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}

	if w := do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, `{"labels":["x"]}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown field on PATCH: %d %s", w.Code, w.Body)
	}
	w = do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, `{"column":"done"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "/move") {
		t.Errorf("column on PATCH: %d %s", w.Code, w.Body)
	}
	if w := do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, `{"priority":"none"}`); w.Code != http.StatusOK || decodeCard(t, w).Priority != "" {
		t.Errorf("priority none: %d %s", w.Code, w.Body)
	}
	if w := do(t, ph, "POST", "/api/planning/cards/"+c.ID+"/move", `{"column":"In_Progress"}`); w.Code != http.StatusOK || decodeCard(t, w).Column != model.CardInProgress {
		t.Errorf("move in any case: %d %s", w.Code, w.Body)
	}
}

// A card moves to Done by itself once all its linked work is done, from the
// event of the work finishing, and says why in its comments. Moved back out
// of Done, it stays where it is put until new work is linked; once that is
// done too, it moves to Done again.
func TestPlanningCardAutoDone(t *testing.T) {
	s, _, p := boardServer(t)
	h := s.Routes()
	a := loggedIn(t, h)
	get := func(id string) cardView {
		t.Helper()
		return decodeCard(t, do(t, a, "GET", "/api/planning/cards/"+id, ""))
	}
	link := func(id, kind, work string) {
		t.Helper()
		if w := do(t, a, "POST", "/api/planning/cards/"+id+"/links", `{"kind":"`+kind+`","id":"`+work+`"}`); w.Code != http.StatusOK {
			t.Fatalf("link: %d %s", w.Code, w.Body)
		}
	}
	// waitDone waits for the card to be in Done, without reading it through
	// the API (which would move it too).
	waitDone := func(id string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			c, err := s.Store.ForOrg(ownerSession(t, s).Org.ID).GetCard(id)
			if err != nil {
				t.Fatal(err)
			}
			if c.Column == model.CardDone {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("card still in %s", c.Column)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	setTask := func(id string, to model.TodoStatus) model.Todo {
		t.Helper()
		td, err := raw(s).UpdateTodo(id, func(x *model.Todo) error { x.Status = to; return nil })
		if err != nil {
			t.Fatal(err)
		}
		return td
	}

	c := decodeCard(t, do(t, a, "POST", "/api/planning/cards", `{"title":"Invoices","column":"planned"}`))
	one, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "Invoice PDF", Prompt: "pdf"})
	two, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "Invoice email", Prompt: "email"})
	link(c.ID, model.LinkTask, one.ID)
	link(c.ID, model.LinkTask, two.ID)
	for _, td := range []model.Todo{one, two} {
		for _, to := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress, model.StatusReview} {
			setTask(td.ID, to)
		}
	}
	// One of two merged: not yet.
	s.ObservePlanning(model.Event{Type: model.EvTodoUpdated, TodoID: one.ID, Data: setTask(one.ID, model.StatusMerged)})
	if got := get(c.ID); got.Column == model.CardDone {
		t.Fatalf("half done, in Done: %+v", got.Status)
	}
	// Both merged: the event moves it.
	s.ObservePlanning(model.Event{Type: model.EvTodoUpdated, TodoID: two.ID, Data: setTask(two.ID, model.StatusMerged)})
	waitDone(c.ID)
	got := get(c.ID)
	if len(got.Discussion) != 1 || !got.Discussion[0].Dash || got.Discussion[0].Author != 0 ||
		got.Discussion[0].Text != "Moved to Done: all 2 pieces of its linked work are done." || got.Comments != 0 {
		t.Errorf("Dash's note: %+v (comments %d)", got.Discussion, got.Comments)
	}

	// Moved back out by someone: it stays, read or observed.
	if w := do(t, a, "POST", "/api/planning/cards/"+c.ID+"/move", `{"column":"in_progress"}`); w.Code != http.StatusOK || decodeCard(t, w).Column != model.CardInProgress {
		t.Fatalf("move back: %d %s", w.Code, w.Body)
	}
	s.ObservePlanning(model.Event{Type: model.EvTodoUpdated, TodoID: two.ID, Data: setTask(two.ID, model.StatusMerged)})
	time.Sleep(50 * time.Millisecond)
	if got := get(c.ID); got.Column != model.CardInProgress {
		t.Fatalf("moved back, then: %s", got.Column)
	}
	// Linking work that is linked already is nothing new.
	link(c.ID, model.LinkTask, one.ID)
	if got := get(c.ID); got.Column != model.CardInProgress {
		t.Fatalf("linked again: %s", got.Column)
	}

	// New work linked: once it is done, the card moves to Done again.
	g, err := raw(s).CreateGoal(model.Goal{ProjectID: p.ID, Owner: testOwner(t, s).User, Title: "Invoice reminders", Mode: model.GoalQueue, Status: model.GoalWorking})
	if err != nil {
		t.Fatal(err)
	}
	link(c.ID, model.LinkGoal, g.ID)
	if got := get(c.ID); got.Column != model.CardInProgress {
		t.Fatalf("new work linked: %s", got.Column)
	}
	g, err = raw(s).UpdateGoal(g.ID, func(x *model.Goal) error { x.Status = model.GoalDone; return nil })
	if err != nil {
		t.Fatal(err)
	}
	s.ObservePlanning(model.Event{Type: model.EvGoalUpdated, Data: g})
	waitDone(c.ID)
	if got := get(c.ID); len(got.Discussion) != 2 || got.Discussion[1].Text != "Moved to Done: all 3 pieces of its linked work are done." {
		t.Errorf("second note: %+v", got.Discussion)
	}

	// A card with nothing linked never moves on its own.
	empty := decodeCard(t, do(t, a, "POST", "/api/planning/cards", `{"title":"Someday"}`))
	if got := get(empty.ID); got.Column != model.CardIdeas {
		t.Errorf("nothing linked: %s", got.Column)
	}
}
