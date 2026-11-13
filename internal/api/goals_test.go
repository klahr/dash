package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/store"
)

// goalPub hands store events to the server's coordinators once it is set.
type goalPub struct{ s atomic.Pointer[Server] }

func (p *goalPub) Publish(ev model.Event) {
	if s := p.s.Load(); s != nil {
		s.ObserveGoals(ev)
	}
}

// goalServer is boardServer whose goals' coordinators run (fakeCoordinator).
func goalServer(t *testing.T) (*Server, model.Project, *fakeCoordinator) {
	t.Helper()
	parallel(t)
	pub := &goalPub{}
	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), pub)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Accounts().Bootstrap("u", "p"); err != nil {
		t.Fatal(err)
	}
	a := auth.New(st.Accounts())
	a.NoFailDelayInTests()
	p, err := st.CreateProject(model.Project{Name: "web", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st, Hub: hub.New(), Auth: a, Perms: perm.NewBroker(nil), Board: &mcptools.Tokens{},
		Todos: queue.NewTodoEngine(&queue.TodoEngine{Log: &queue.TodoLog{Pub: hub.New(), Root: t.TempDir()}}), Static: fstest.MapFS{"index.html": {Data: []byte("ok")}}}
	fc := &fakeCoordinator{s: s, h: s.Routes()}
	s.Chat = fc
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	pub.s.Store(s)
	s.StartGoals(ctx)
	return s, p, fc
}

// fakeCoordinator stands in for Claude as a goal's coordinator: on each turn
// it calls act with the turn's message and a way to call its tools, through
// /mcp/dash with a token of the turn's goal scope, as Claude Code would.
type fakeCoordinator struct {
	s   *Server
	h   http.Handler
	mu  sync.Mutex
	act func(msg string, call func(tool, args string) (string, bool))
	// turns are the messages the turns were given (the goal's state in front).
	turns []string
	dirs  []string
}

func (f *fakeCoordinator) Stream(ctx context.Context, dir string, history []agent.ChatMessage, emit func(agent.ChatDelta)) error {
	c, _ := mcptools.CallerFrom(ctx)
	goal := mcptools.GoalFrom(ctx)
	g, err := f.s.Store.ForOrg(c.Org).GetGoal(goal)
	if err != nil {
		return fmt.Errorf("a turn without its goal: %w", err)
	}
	token, release := f.s.Board.RegisterGoal(c, g.ProjectID, goal)
	defer release()
	msg := history[len(history)-1].Content
	f.mu.Lock()
	f.turns, f.dirs = append(f.turns, msg), append(f.dirs, dir)
	act := f.act
	f.mu.Unlock()
	if act != nil {
		act(msg, func(tool, args string) (string, bool) { return toolCall(f.h, token, tool, args) })
	}
	emit(agent.ChatDelta{Kind: "text", Text: "Done."})
	emit(agent.ChatDelta{Kind: "done"})
	return nil
}

func (f *fakeCoordinator) setAct(act func(msg string, call func(tool, args string) (string, bool))) {
	f.mu.Lock()
	f.act = act
	f.mu.Unlock()
}

func (f *fakeCoordinator) turnCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.turns)
}

func (f *fakeCoordinator) lastTurn() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.turns[len(f.turns)-1]
}

// toolCall calls one board tool; it can't fail the test (it runs in a turn).
func toolCall(h http.Handler, token, tool, args string) (string, bool) {
	w := rpc(h, token, "tools/call", fmt.Sprintf(`{"name":%q,"arguments":%s}`, tool, args))
	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct{ Text string }
		}
		Error *mcptools.Error
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Error != nil || len(resp.Result.Content) != 1 {
		return w.Body.String(), true
	}
	return resp.Result.Content[0].Text, resp.Result.IsError
}

// settleGoals waits for the task events queued so far and the coordinator
// turns they started.
func settleGoals(s *Server) {
	e := s.goals()
	e.flush()
	e.wait()
}

func getGoal(t *testing.T, h http.Handler, id string) goalView {
	t.Helper()
	w := do(t, h, "GET", "/api/goals/"+id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get goal: %d %s", w.Code, w.Body)
	}
	var g goalView
	if err := json.Unmarshal(w.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func hasLog(g model.Goal, text string) bool {
	return slices.ContainsFunc(g.Log, func(l model.GoalLogEntry) bool { return strings.Contains(l.Text, text) })
}

func timeline(t *testing.T, s *Server, todo string) string {
	t.Helper()
	evs, err := s.Todos.Log.Events(todo)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, ev := range evs {
		b.WriteString(ev.Text + "\n")
	}
	return b.String()
}

func setStatus(t *testing.T, s *Server, id string, to ...model.TodoStatus) {
	t.Helper()
	for _, st := range to {
		if _, err := raw(s).SetStatus(id, st, ""); err != nil {
			t.Fatal(err)
		}
	}
}

var taskRef = regexp.MustCompile(`Task ([0-9a-f]+) `)

// An "ask" goal: the coordinator plans, the plan waits for the user, its
// tasks are drafts the user queues, task events wake the coordinator, which
// sends work back but can't approve, queue or touch other tasks; pause,
// resume and stop.
func TestGoalAskMode(t *testing.T) {
	s, p, fc := goalServer(t)
	h := loggedIn(t, s.Routes())
	other, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "not the goal's"})
	if err != nil {
		t.Fatal(err)
	}
	fc.setAct(func(msg string, call func(string, string) (string, bool)) {
		if !strings.Contains(msg, "propose a plan") {
			return
		}
		call("propose_plan", `{"tasks":[{"title":"Index","prompt":"Add a search index"},{"title":"Use it","prompt":"Query the index","after":[0]}],"why":"index first"}`)
	})
	w := do(t, h, "POST", "/api/goals", `{"project":"`+p.ID+`","title":"Faster search","description":"Search under 100 ms","budget":{"max_tasks":3}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created goalView
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if created.Mode != model.GoalAsk || created.Owner == 0 {
		t.Fatalf("created %+v", created.Goal)
	}
	settleGoals(s)
	g := getGoal(t, h, created.ID)
	if g.Status != model.GoalWaiting || g.WaitingFor != model.GoalWaitPlan || len(g.Plan) != 2 || !hasLog(g.Goal, "Proposed a plan of 2 tasks") {
		t.Fatalf("after planning %+v", g.Goal)
	}
	if fc.dirs[0] != p.Path || !strings.Contains(fc.turns[0], "Search under 100 ms") {
		t.Fatalf("first turn in %q: %s", fc.dirs[0], fc.turns[0])
	}
	if chat := raw(s).GoalChat(g.ID); len(chat) != 2 || chat[1].Content != "Done." {
		t.Fatalf("the coordinator's conversation %+v", chat)
	}

	// The coordinator's tools: no approving, merging or publishing.
	token, release := s.Board.RegisterGoal(testOwner(t, s), p.ID, g.ID)
	defer release()
	var list struct {
		Result struct{ Tools []struct{ Name string } }
	}
	_ = json.Unmarshal(rpc(s.Routes(), token, "tools/list", "{}").Body.Bytes(), &list)
	var tools []string
	for _, tl := range list.Result.Tools {
		tools = append(tools, tl.Name)
	}
	want := append(slices.Clone(mcptools.GoalBoardTools), mcptools.GoalToolNames...)
	slices.Sort(tools)
	slices.Sort(want)
	if !slices.Equal(tools, want) {
		t.Fatalf("coordinator tools %v, want %v", tools, want)
	}

	// The user accepts the plan: drafts, in order.
	w = do(t, h, "POST", "/api/goals/"+g.ID+"/plan/accept", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", w.Code, w.Body)
	}
	var acc struct {
		Created []model.Todo `json:"created"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &acc)
	if len(acc.Created) != 2 || acc.Created[0].GoalID != g.ID || acc.Created[0].Status != model.StatusDraft ||
		!slices.Equal(acc.Created[1].DependsOn, []string{acc.Created[0].ID}) {
		t.Fatalf("created %+v", acc.Created)
	}
	t1, t2 := acc.Created[0], acc.Created[1]
	if tl := timeline(t, s, t1.ID); !strings.Contains(tl, model.GoalTimeline+"Faster search: created this task from its plan") {
		t.Fatalf("timeline %q", tl)
	}
	if w := do(t, h, "POST", "/api/goals/"+g.ID+"/plan/accept", `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("accepting twice: %d", w.Code)
	}
	settleGoals(s)
	if g = getGoal(t, h, g.ID); g.Status != model.GoalWaiting || g.WaitingFor != model.GoalWaitDrafts {
		t.Fatalf("with drafts %+v", g.Goal)
	}

	// The coordinator can't queue in ask mode, nor change others' tasks.
	if text, isErr := toolCall(s.Routes(), token, "queue_todo", `{"todo":"`+t1.ID+`"}`); !isErr || !strings.Contains(text, "asks before queuing") {
		t.Fatalf("queue in ask mode: %s", text)
	}
	if text, isErr := toolCall(s.Routes(), token, "update_todo", `{"todo":"`+other.ID+`","title":"mine now"}`); !isErr || !strings.Contains(text, "isn't one of this goal's tasks") {
		t.Fatalf("update another task: %s", text)
	}
	if text, isErr := toolCall(s.Routes(), token, "delete_todo", `{"todo":"`+other.ID+`"}`); !isErr {
		t.Fatalf("delete another task: %s", text)
	}

	// A draft the user deletes no longer waits; once the user queues the
	// rest, the goal works.
	extra, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "extra", GoalID: g.ID})
	if err != nil {
		t.Fatal(err)
	}
	settleGoals(s)
	if g = getGoal(t, h, g.ID); !strings.HasPrefix(g.Note, "3 of the goal's tasks") {
		t.Fatalf("with 3 drafts %+v", g.Goal)
	}
	if _, err := raw(s).DeleteTodo(extra.ID); err != nil {
		t.Fatal(err)
	}
	settleGoals(s)
	if g = getGoal(t, h, g.ID); !strings.HasPrefix(g.Note, "2 of the goal's tasks") {
		t.Fatalf("after deleting a draft %+v", g.Goal)
	}
	setStatus(t, s, t1.ID, model.StatusQueued)
	setStatus(t, s, t2.ID, model.StatusQueued)
	settleGoals(s)
	if g = getGoal(t, h, g.ID); g.Status != model.GoalWorking {
		t.Fatalf("all queued %+v", g.Goal)
	}

	// Task 1 enters review: the coordinator reviews it and sends it back.
	fc.setAct(func(msg string, call func(string, string) (string, bool)) {
		m := taskRef.FindStringSubmatch(msg)
		if m == nil || !strings.Contains(msg, "entered review") {
			return
		}
		call("post_review_note", `{"todo":"`+m[1]+`","note":"The index misses the title field."}`)
		call("send_back", `{"todo":"`+m[1]+`","notes":"Index the title too."}`)
		call("add_log", `{"text":"Sent the index back","why":"titles weren't indexed","todo":"`+m[1]+`"}`)
	})
	turns := fc.turnCount()
	setStatus(t, s, t1.ID, model.StatusInProgress, model.StatusReview)
	settleGoals(s)
	if fc.turnCount() != turns+1 || !strings.Contains(fc.lastTurn(), t1.ID) {
		t.Fatalf("review didn't wake the coordinator: %d turns, last %s", fc.turnCount(), fc.lastTurn())
	}
	got, _ := raw(s).GetTodo(t1.ID)
	if got.Status != model.StatusQueued || !slices.Contains(got.Feedback, "Index the title too.") || got.GoalNote == nil || !strings.Contains(got.GoalNote.Text, "title field") {
		t.Fatalf("sent back %+v", got)
	}
	if tl := timeline(t, s, t1.ID); !strings.Contains(tl, "sent this task back with notes") || !strings.Contains(tl, "posted a review note") {
		t.Fatalf("timeline %q", tl)
	}
	g = getGoal(t, h, g.ID)
	if !hasLog(g.Goal, "entered review") || !hasLog(g.Goal, "Sent task "+t1.ID+" back") || !hasLog(g.Goal, "Sent the index back") {
		t.Fatalf("log %+v", g.Log)
	}

	// Its budget is 3 tasks: one follow-up (a draft in ask mode), then no more.
	if text, isErr := toolCall(s.Routes(), token, "create_todo", `{"title":"Docs","prompt":"Document search","draft":false}`); isErr || !strings.Contains(text, `"goal_id": "`+g.ID+`"`) || !strings.Contains(text, `"status": "draft"`) {
		t.Fatalf("follow-up: %s", text)
	}
	if text, isErr := toolCall(s.Routes(), token, "create_todo", `{"title":"More"}`); !isErr || !strings.Contains(text, "budget is 3 tasks") {
		t.Fatalf("over budget: %s", text)
	}

	// Paused, events wait; resumed, the coordinator gets them.
	if w := do(t, h, "POST", "/api/goals/"+g.ID+"/pause", ""); w.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", w.Code, w.Body)
	}
	turns = fc.turnCount()
	setStatus(t, s, t2.ID, model.StatusInProgress, model.StatusFailed)
	settleGoals(s)
	if g = getGoal(t, h, g.ID); fc.turnCount() != turns || g.Status != model.GoalPaused || len(g.Pending) != 1 {
		t.Fatalf("paused: %d turns, %+v", fc.turnCount()-turns, g.Goal)
	}
	if w := do(t, h, "POST", "/api/goals/"+g.ID+"/resume", ""); w.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	settleGoals(s)
	if fc.turnCount() != turns+1 || !strings.Contains(fc.lastTurn(), "failed") {
		t.Fatalf("resumed: %d turns", fc.turnCount()-turns)
	}

	// Stopped, nothing wakes it.
	if w := do(t, h, "POST", "/api/goals/"+g.ID+"/stop", ""); w.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	turns = fc.turnCount()
	setStatus(t, s, t1.ID, model.StatusInProgress, model.StatusReview)
	settleGoals(s)
	if g = getGoal(t, h, g.ID); fc.turnCount() != turns || g.Status != model.GoalDone || !hasLog(g.Goal, "wasn't woken") {
		t.Fatalf("stopped: %d turns, %+v", fc.turnCount()-turns, g.Goal)
	}
	if text, isErr := toolCall(s.Routes(), token, "send_back", `{"todo":"`+t1.ID+`","notes":"x"}`); !isErr || !strings.Contains(text, "the goal is done") {
		t.Fatalf("a stopped goal's tools: %s", text)
	}
	var logEntries []model.GoalLogEntry
	_ = json.Unmarshal(do(t, h, "GET", "/api/goals/"+g.ID+"/log", "").Body.Bytes(), &logEntries)
	if len(logEntries) != len(g.Log) {
		t.Fatalf("log endpoint: %d entries, goal has %d", len(logEntries), len(g.Log))
	}
}

// A "queue on its own" goal queues its plan at once; once its tasks are used
// up and finished it is out of budget, until the budget is raised.
func TestGoalQueueModeRunsOutOfBudget(t *testing.T) {
	s, p, fc := goalServer(t)
	h := loggedIn(t, s.Routes())
	fc.setAct(func(msg string, call func(string, string) (string, bool)) {
		if strings.Contains(msg, "propose a plan") {
			call("propose_plan", `{"tasks":[{"title":"Only","prompt":"Do it all"}]}`)
		}
	})
	w := do(t, h, "POST", "/api/goals", `{"project":"`+p.ID+`","description":"Ship it","mode":"queue","budget":{"max_tasks":1}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var g goalView
	_ = json.Unmarshal(w.Body.Bytes(), &g)
	settleGoals(s)
	g = getGoal(t, h, g.ID)
	if g.Status != model.GoalWorking || len(g.Tasks) != 1 || g.Tasks[0].Status != model.StatusQueued || g.Title != "Ship it" {
		t.Fatalf("after planning %+v", g)
	}
	id := g.Tasks[0].ID
	setStatus(t, s, id, model.StatusInProgress, model.StatusReview, model.StatusMerging, model.StatusMerged)
	settleGoals(s)
	g = getGoal(t, h, g.ID)
	if g.Status != model.GoalOutOfBudget || !strings.Contains(g.Note, "used up and finished") || !hasLog(g.Goal, "was merged") {
		t.Fatalf("after its task merged %+v", g.Goal)
	}
	if w := do(t, h, "POST", "/api/goals/"+g.ID+"/resume", ""); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "raise the budget") {
		t.Fatalf("resume over budget: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "PATCH", "/api/goals/"+g.ID, `{"budget":{"max_tasks":2}}`); w.Code != http.StatusOK {
		t.Fatalf("raise budget: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "POST", "/api/goals/"+g.ID+"/resume", ""); w.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	if g = getGoal(t, h, g.ID); g.Status != model.GoalWorking || !hasLog(g.Goal, "budget") {
		t.Fatalf("resumed %+v", g.Goal)
	}
	if w := do(t, h, "POST", "/api/goals", `{"project":"`+p.ID+`","title":"x","mode":"yolo"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown mode: %d", w.Code)
	}
	var list []goalView
	_ = json.Unmarshal(do(t, h, "GET", "/api/projects/"+p.ID+"/goals", "").Body.Bytes(), &list)
	if len(list) != 1 || list[0].Log != nil {
		t.Fatalf("list %+v", list)
	}
}

// The user talks to a goal's coordinator over the chat's WebSocket: the
// turn continues the goal's conversation, as its coordinator, and answers
// the question it asked.
func TestGoalChat(t *testing.T) {
	s, p, fc := goalServer(t)
	fc.setAct(func(msg string, call func(string, string) (string, bool)) {
		if strings.Contains(msg, "propose a plan") {
			call("ask_user", `{"question":"Postgres or SQLite?"}`)
		}
	})
	g, err := s.as(ownerCaller(t, s)).createGoal(goalRequest{Project: p.ID, Description: ptr("Add storage")})
	if err != nil {
		t.Fatal(err)
	}
	settleGoals(s)
	if g, _ = raw(s).GetGoal(g.ID); g.Status != model.GoalWaiting || g.WaitingFor != model.GoalWaitQuestion || g.Note != "Postgres or SQLite?" {
		t.Fatalf("asked %+v", g)
	}
	var proposed atomic.Bool
	fc.setAct(func(msg string, call func(string, string) (string, bool)) {
		if strings.Contains(msg, "SQLite, please") && strings.Contains(msg, "<goal ") {
			_, isErr := call("propose_plan", `{"tasks":[{"title":"SQLite","prompt":"Store it in SQLite"}]}`)
			proposed.Store(!isErr)
		}
	})
	conn := chatSocket(t, s)
	// The client's history is for display: the goal keeps the conversation.
	msgs := []agent.ChatMessage{{Role: "user", Content: "stale"}, {Role: "user", Content: "SQLite, please"}}
	if err := conn.WriteJSON(map[string]any{"type": "send", "goal": g.ID, "messages": msgs}); err != nil {
		t.Fatal(err)
	}
	for {
		var d agent.ChatDelta
		if err := conn.ReadJSON(&d); err != nil {
			t.Fatal(err)
		}
		if d.Kind == "error" {
			t.Fatalf("chat: %s", d.Text)
		}
		if d.Kind == "done" {
			break
		}
	}
	waitFor(t, "the turn to be saved", func() bool { return len(raw(s).GoalChat(g.ID)) == 4 })
	chat := raw(s).GoalChat(g.ID)
	if chat[2].Content != "SQLite, please" || chat[2].CreatedBy == 0 || !proposed.Load() {
		t.Fatalf("conversation %+v, proposed %v", chat, proposed.Load())
	}
	if g, _ = raw(s).GetGoal(g.ID); g.WaitingFor != model.GoalWaitPlan || len(g.Plan) != 1 {
		t.Fatalf("after the chat %+v", g)
	}
	if !strings.Contains(fc.turns[0], "<goal ") {
		t.Fatal("the wake-up lacked the goal's state")
	}
}
