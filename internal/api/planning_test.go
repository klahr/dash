package api

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/model"
)

func decodeCard(t *testing.T, w *httptest.ResponseRecorder) cardView {
	t.Helper()
	var v cardView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body, err)
	}
	return v
}

// A card started as a task is linked to it and moves to In progress; its
// status follows the task in an owner's words, and suggests Done (without
// making the move) once the work is merged. A planner sees the same card
// without the task's id.
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
	if len(c.Links) != 1 || c.Links[0].ID != "" || c.Links[0].Title != "Export as PDF" || c.Column != model.CardInProgress || c.ProjectID != p.ID {
		t.Fatalf("started card, as the planner: %+v", c)
	}
	// The member sees the task's id; the task's prompt is the card.
	c = decodeCard(t, do(t, a, "GET", "/api/planning/cards/"+c.ID, ""))
	if len(c.Links) != 1 || c.Links[0].ID == "" {
		t.Fatalf("started card, as a member: %+v", c)
	}
	td, err := raw(s).GetTodo(c.Links[0].ID)
	if err != nil || td.Status != model.StatusQueued || !strings.Contains(td.Prompt, "Reports, as **PDF**.") || td.CreatedBy == 0 {
		t.Fatalf("the task: %+v %v", td, err)
	}

	for _, step := range []struct {
		to      model.TodoStatus
		text    string
		suggest bool
	}{
		{model.StatusInProgress, "In progress", false},
		{model.StatusReview, "Waiting for review", false},
		{model.StatusFailed, "Failed, needs a developer", false},
		{model.StatusMerged, "All done", true},
	} {
		if _, err := raw(s).UpdateTodo(td.ID, func(x *model.Todo) error { x.Status = step.to; return nil }); err != nil {
			t.Fatal(err)
		}
		got := decodeCard(t, do(t, ph, "GET", "/api/planning/cards/"+c.ID, ""))
		if got.Status.Text != step.text || got.Status.SuggestDone != step.suggest || got.Column != model.CardInProgress {
			t.Errorf("task %s: card %+v", step.to, got.Status)
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
	if st := list[0].Status; st.Progress != "1 of 2 done" || st.SuggestDone {
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
	if len(got.Discussion) != 1 || got.Discussion[0].AuthorName != "pat" || got.Comments != 1 {
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
	hdr := http.Header{}
	for _, c := range w.Result().Cookies() {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
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

// A card is assigned to one of the org's members, by a planner too: the
// people to choose from are the org's members, and the card carries its
// assignee's name and picture. Someone outside the org is refused.
func TestPlanningCardAssignee(t *testing.T) {
	s, _, _ := boardServer(t)
	h := s.Routes()
	a := loggedIn(t, h)
	ph, pat := plannerOf(t, s, h)

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

	c := decodeCard(t, do(t, a, "POST", "/api/planning/cards", `{"title":"Dark mode","assignee":`+strconv.FormatInt(pat.ID, 10)+`}`))
	if c.Assignee != pat.ID || c.AssigneeName != "pat" {
		t.Fatalf("assigned card %+v", c)
	}
	if _, err := raw(s).Accounts().SetAvatar(pat.ID, tinyPNG(t), time.Now()); err != nil {
		t.Fatal(err)
	}
	c = decodeCard(t, do(t, ph, "GET", "/api/planning/cards/"+c.ID, ""))
	if c.AssigneeAvatar == 0 {
		t.Errorf("no picture version for the assignee: %+v", c)
	}
	if w := do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, `{"assignee":999999}`); w.Code != http.StatusBadRequest {
		t.Errorf("assigning a stranger: %d %s", w.Code, w.Body)
	}
	c = decodeCard(t, do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, `{"assignee":0}`))
	if c.Assignee != 0 || c.AssigneeName != "" {
		t.Errorf("unassigned card %+v", c)
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
