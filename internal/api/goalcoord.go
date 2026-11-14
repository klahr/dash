package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/textutil"
	"github.com/klahr/dash/internal/usage"
)

// The goals' coordinators. A coordinator is Claude run with the chat
// machinery (s.Chat here, or the goal owner's runner linked to the project
// on a hub), read-only in the project's checkout, with the dash_board tools
// of its goal (goalBoard): it never gets a tool to approve or merge.
//
// It is woken only by events of the goal's own tasks (entering review,
// failing, being merged) and by the user (making the goal, rejecting its
// plan, resuming it), never on a timer. Each wake-up is one turn of its
// conversation (Store.GoalChat): the events are a new message, with the
// goal's state in front of it. Events that come while it can't be woken
// (a turn running, the goal paused, no runner online) wait in the goal's
// Pending for the next turn.

// goalEngine runs the coordinators' turns.
type goalEngine struct {
	s     *Server
	once  sync.Once
	queue chan model.Event
	// status is each task's last status, seen by the run loop only.
	status map[string]model.TodoStatus

	mu      sync.Mutex
	ctx     context.Context
	running map[string]bool               // a goal's turns are being run
	again   map[string]bool               // kicked while running: look again
	cancels map[string]context.CancelFunc // the running wake-up turn of a goal
	locks   map[string]*sync.Mutex        // one turn of a goal at a time, wake-up or chat
	wg      sync.WaitGroup
}

// goalQueueSize is how many task events wait for the run loop.
const goalQueueSize = 1024

// goalTurnTimeout bounds one coordinator turn.
const goalTurnTimeout = 30 * time.Minute

// goals is the server's coordinator engine.
func (s *Server) goals() *goalEngine {
	s.goalsOnce.Do(func() {
		s.goalEngine = &goalEngine{s: s, ctx: context.Background()}
	})
	e := s.goalEngine
	e.init()
	return e
}

func (e *goalEngine) init() {
	e.once.Do(func() {
		e.queue = make(chan model.Event, goalQueueSize)
		e.status = map[string]model.TodoStatus{}
		e.running, e.again = map[string]bool{}, map[string]bool{}
		e.cancels, e.locks = map[string]context.CancelFunc{}, map[string]*sync.Mutex{}
	})
}

// ObserveGoals is fed every store event (it never blocks: the store
// publishes holding its lock). Changes to tasks of goals wake their
// coordinators.
func (s *Server) ObserveGoals(ev model.Event) {
	switch ev.Type {
	case model.EvTodoUpdated, model.EvTodoDeleted:
	default:
		return
	}
	if t, ok := ev.Data.(model.Todo); ev.Type == model.EvTodoUpdated && (!ok || t.GoalID == "") {
		return
	}
	e := s.goals()
	select {
	case e.queue <- ev:
	default:
		slog.Warn("goals: queue full, task event dropped", "todo", ev.TodoID)
	}
}

// StartGoals remembers the statuses of the goals' tasks, so only changes
// from now on wake their coordinators, wakes those that have events
// waiting (from before a restart), and handles task events until ctx ends.
func (s *Server) StartGoals(ctx context.Context) {
	e := s.goals()
	e.mu.Lock()
	e.ctx = ctx
	e.mu.Unlock()
	var waiting []string
	for _, g := range s.Store.ListGoals("") {
		for _, t := range s.Store.GoalTodos(g) {
			e.status[t.ID] = t.Status
		}
		if g.Status.Active() && len(g.Pending) > 0 {
			waiting = append(waiting, g.ID)
		}
	}
	for _, id := range waiting {
		e.kick(id)
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-e.queue:
				if ev.Type == evGoalFlush {
					close(ev.Data.(chan struct{}))
					continue
				}
				e.handle(ev)
			}
		}
	}()
}

// evGoalFlush marks a place in the queue: the run loop closes the event's
// channel when it reaches it. Tests use it to wait for queued events.
const evGoalFlush model.EventType = "goal.flush"

// flush waits until the run loop has handled every event queued before.
func (e *goalEngine) flush() {
	done := make(chan struct{})
	e.queue <- model.Event{Type: evGoalFlush, Data: done}
	<-done
}

// wait waits for the coordinator turns running now (tests).
func (e *goalEngine) wait() { e.wg.Wait() }

// handle notes a goal task's new status, and wakes its coordinator when it
// entered review, failed or was merged.
func (e *goalEngine) handle(ev model.Event) {
	if ev.Type == model.EvTodoDeleted {
		// Whose task it was is gone with it: the project's goals look again
		// (a deleted draft no longer waits for the user).
		delete(e.status, ev.TodoID)
		if ev.ProjectID == "" {
			return
		}
		for _, g := range e.s.Store.ListGoals(ev.ProjectID) {
			if g.Status.Active() {
				e.refresh(g.ID)
			}
		}
		return
	}
	t := ev.Data.(model.Todo)
	prev, known := e.status[t.ID]
	e.status[t.ID] = t.Status
	if prev == t.Status {
		return
	}
	switch t.Status {
	case model.StatusReview, model.StatusFailed, model.StatusMerged:
		if known {
			e.wake(t)
			return
		}
	}
	e.refresh(t.GoalID) // e.g. a draft the user queued: the goal no longer waits for it
}

// wake records t's event on its goal and wakes the coordinator, unless the
// goal is paused (it waits there) or over (only logged).
func (e *goalEngine) wake(t model.Todo) {
	org, ok := e.s.Store.GoalOrg(t.GoalID)
	if !ok {
		return
	}
	data := e.s.Store.ForOrg(org)
	w := model.GoalWake{Event: string(t.Status), Todo: t.ID, Title: taskName(t), At: now()}
	if t.Status == model.StatusFailed {
		w.Note = t.Error
	}
	g, err := data.UpdateGoal(t.GoalID, func(g *model.Goal) error {
		text := wakeText(w)
		if g.Status.Ended() {
			text += " (the goal is " + strings.ReplaceAll(string(g.Status), "_", " ") + ": the coordinator wasn't woken)"
		} else {
			g.Pending = append(g.Pending, w)
		}
		g.AddLog(model.GoalLogEntry{Kind: model.GoalLogWake, Text: text, Todo: t.ID})
		return nil
	})
	if err != nil {
		slog.Warn("goals: record a task event", "goal", t.GoalID, "todo", t.ID, "err", err)
		return
	}
	if g.Status.Active() {
		e.kick(g.ID)
	}
}

// wakeText says what a wake-up is about, for the goal's log and the
// coordinator.
func wakeText(w model.GoalWake) string {
	switch w.Event {
	case model.GoalWakeStart:
		return "The goal was made: read the code and propose a plan with propose_plan."
	case model.GoalWakeReplan:
		if w.Note != "" {
			return "The user rejected the plan, saying: " + w.Note + "\nPropose a new plan with propose_plan."
		}
		return "The user rejected the plan. Propose a new one with propose_plan."
	case model.GoalWakeMessage:
		return "The user wrote to you: " + w.Note
	}
	name := fmt.Sprintf("Task %s (%q)", w.Todo, w.Title)
	switch model.TodoStatus(w.Event) {
	case model.StatusReview:
		return name + " entered review."
	case model.StatusFailed:
		if w.Note != "" {
			return name + " failed: " + textutil.OneLine(w.Note, 300)
		}
		return name + " failed."
	case model.StatusMerged:
		return name + " was merged."
	}
	return name + ": " + w.Event
}

// kick makes sure goal id's waiting events get a coordinator turn.
func (e *goalEngine) kick(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running[id] {
		e.again[id] = true
		return
	}
	e.running[id] = true
	e.wg.Add(1)
	go e.run(id)
}

// run gives goal id's waiting events turns, one after another, until none
// are left (or it can't run).
func (e *goalEngine) run(id string) {
	defer e.wg.Done()
	for {
		ok := e.turn(id)
		e.mu.Lock()
		if !ok || !e.again[id] {
			delete(e.running, id)
			delete(e.again, id)
			e.mu.Unlock()
			return
		}
		e.again[id] = false
		e.mu.Unlock()
	}
}

// lock is goal id's turn lock: its wake-ups and the turns of its chat take
// turns, so its conversation never forks.
func (e *goalEngine) lock(id string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	l, ok := e.locks[id]
	if !ok {
		l = &sync.Mutex{}
		e.locks[id] = l
	}
	return l
}

// cancel stops goal id's running wake-up turn, if any (pause, stop).
func (e *goalEngine) cancel(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.cancels[id]; ok {
		c()
	}
}

// turn runs one coordinator turn for goal id's waiting events, and reports
// whether it ran (false: nothing to do, or it couldn't run; the events then
// wait for the next kick).
func (e *goalEngine) turn(id string) bool {
	l := e.lock(id)
	l.Lock()
	defer l.Unlock()
	s := e.s
	org, ok := s.Store.GoalOrg(id)
	if !ok {
		return false
	}
	data := s.Store.ForOrg(org)
	var wakes []model.GoalWake
	g, err := data.UpdateGoal(id, func(g *model.Goal) error {
		if !g.Status.Active() || len(g.Pending) == 0 {
			return errNothingToDo
		}
		wakes, g.Pending, g.Running = g.Pending, nil, true
		return nil
	})
	if err != nil {
		return false
	}
	e.mu.Lock()
	ctx, cancel := context.WithTimeout(e.ctx, goalTurnTimeout)
	e.cancels[id] = cancel
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.cancels, id)
		e.mu.Unlock()
		cancel()
	}()

	var lines []string
	for _, w := range wakes {
		lines = append(lines, wakeText(w))
	}
	msg := strings.Join(lines, "\n")
	err = s.coordinatorTurn(ctx, g, msg)
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	g, uerr := data.UpdateGoal(id, func(g *model.Goal) error {
		g.Running = false
		if err == nil {
			return nil
		}
		// It couldn't run (no runner online, Claude failed), timed out or
		// was paused: the events wait for the next wake-up, or for resume.
		if g.Status != model.GoalDone {
			g.Pending = append(wakes, g.Pending...)
		}
		switch {
		case timedOut:
			g.AddLog(model.GoalLogEntry{Kind: model.GoalLogStatus, Text: "The coordinator's turn took too long and was stopped."})
		case ctx.Err() == nil:
			g.AddLog(model.GoalLogEntry{Kind: model.GoalLogStatus, Text: "The coordinator couldn't run: " + textutil.OneLine(err.Error(), 300)})
		}
		return nil
	})
	if uerr != nil {
		slog.Warn("goals: after a coordinator turn", "goal", id, "err", uerr)
	}
	if err != nil {
		slog.Warn("goals: coordinator turn", "goal", id, "err", err)
		return false
	}
	if g.Status == model.GoalPlanning && slices.ContainsFunc(wakes, func(w model.GoalWake) bool {
		return w.Event == model.GoalWakeStart || w.Event == model.GoalWakeReplan
	}) {
		// It was asked for a plan and gave none.
		_, _ = data.UpdateGoal(id, func(g *model.Goal) error {
			if g.Status == model.GoalPlanning {
				g.Status, g.WaitingFor = model.GoalWaiting, model.GoalWaitQuestion
				g.Note = "The coordinator proposed no plan: talk to it in the goal's chat."
				g.AddLog(model.GoalLogEntry{Kind: model.GoalLogStatus, Text: g.Note})
			}
			return nil
		})
	}
	e.refresh(id)
	return true
}

var errNothingToDo = errors.New("nothing to do")

// refresh works out again what a working or waiting goal waits for: in
// "ask" mode, a goal with draft tasks waits for the user to queue them,
// and works again once none are left; a plan or a question keeps it
// waiting. A goal whose tasks are all made and finished, or whose tasks
// used their weekly share, is out of budget (and its owner told, see
// notify).
func (e *goalEngine) refresh(id string) {
	if id == "" {
		return
	}
	s := e.s
	org, ok := s.Store.GoalOrg(id)
	if !ok {
		return
	}
	data := s.Store.ForOrg(org)
	g, err := data.GetGoal(id)
	if err != nil || (g.Status != model.GoalWorking && g.Status != model.GoalWaiting) {
		return
	}
	sv := s.as(Caller{User: g.Owner, Org: org})
	over := ""
	if !g.Running && len(g.Pending) == 0 {
		// Not while the coordinator still has events to look at: the
		// last task merged may be what finishes the goal.
		over = s.goalOverBudget(sv, g)
	}
	drafts := 0
	for _, t := range data.GoalTodos(g) {
		if t.Status == model.StatusDraft {
			drafts++
		}
	}
	_, err = data.UpdateGoal(id, func(g *model.Goal) error {
		if g.Status != model.GoalWorking && g.Status != model.GoalWaiting {
			return errNothingToDo
		}
		before := *g
		switch {
		case over != "":
			g.Status, g.WaitingFor, g.Note = model.GoalOutOfBudget, "", "Out of budget: "+over+"."
			g.AddLog(model.GoalLogEntry{Kind: model.GoalLogStatus, Text: g.Note})
		case len(g.Plan) > 0 || g.WaitingFor == model.GoalWaitQuestion:
		case g.Mode == model.GoalAsk && drafts > 0:
			g.Status, g.WaitingFor = model.GoalWaiting, model.GoalWaitDrafts
			g.Note = fmt.Sprintf("%d of the goal's tasks wait for you to queue them.", drafts)
			if drafts == 1 {
				g.Note = "One of the goal's tasks waits for you to queue it."
			}
		default:
			g.Status, g.WaitingFor, g.Note = model.GoalWorking, "", ""
		}
		if g.Status == before.Status && g.WaitingFor == before.WaitingFor && g.Note == before.Note {
			return errNothingToDo
		}
		return nil
	})
	if err != nil && !errors.Is(err, errNothingToDo) {
		slog.Warn("goals: refresh", "goal", id, "err", err)
	}
}

// answered notes that the user wrote to goal id's coordinator: a question
// it asked is answered.
func (e *goalEngine) answered(data interface {
	UpdateGoal(string, func(*model.Goal) error) (model.Goal, error)
}, id string) {
	_, _ = data.UpdateGoal(id, func(g *model.Goal) error {
		if g.WaitingFor != model.GoalWaitQuestion {
			return errNothingToDo
		}
		g.WaitingFor, g.Note = "", ""
		g.Status = model.GoalWorking
		return nil
	})
	e.refresh(id)
}

// coordinatorTurn runs one turn of g's coordinator with msg, a wake-up, as
// the goal's owner, and adds it and the reply to the goal's conversation.
func (s *Server) coordinatorTurn(ctx context.Context, g model.Goal, msg string) error {
	org, ok := s.Store.GoalOrg(g.ID)
	if !ok {
		return errors.New("the goal is gone")
	}
	owner := model.Owner{User: g.Owner, Org: org}
	if !s.Member(owner) {
		return errors.New("the goal's owner no longer belongs to its organization")
	}
	sv := s.as(Caller{User: owner.User, Org: owner.Org})
	p, err := sv.data.GetProject(g.ProjectID)
	if err != nil {
		return err
	}
	chatter, dir, err := s.coordinatorChatter(sv, p, g, "", &chatRelay{})
	if err != nil {
		return err
	}
	ctx = mcptools.WithCaller(ctx, owner)
	ctx = agent.WithProject(ctx, p.ID)
	ctx = mcptools.WithGoal(ctx, g.ID)
	ctx = agent.AsCoordinator(ctx)
	history, _ := sv.data.GoalChat(g.ID)
	asked := agent.ChatMessage{Role: "user", Content: msg, At: ptr(now())}
	prompt := s.coordinatorPrompt(sv, g, history, asked)
	var reply strings.Builder
	var usage *model.ChatUsage
	var failed error
	started := now()
	err = chatter.Stream(ctx, dir, prompt, func(d agent.ChatDelta) {
		switch d.Kind {
		case "text":
			reply.WriteString(d.Text)
		case "usage":
			usage = d.Usage
		case "error":
			failed = errors.New(d.Text)
		}
	})
	if err == nil {
		err = failed
	}
	if err != nil {
		return err
	}
	answer := agent.ChatMessage{Role: "assistant", Content: reply.String(), At: &started, Usage: usage}
	return sv.data.AppendGoalChat(g.ID, asked, answer)
}

func ptr[T any](v T) *T { return &v }

// coordinatorChatter is where g's coordinator runs for sv's caller: here,
// in p's checkout (dir), or on a hub on the caller's runner linked to p
// (want, if set and online), in its clone, relaying through relay.
func (s *Server) coordinatorChatter(sv *service, p model.Project, g model.Goal, want string, relay *chatRelay) (agent.Chatter, string, error) {
	if s.projectChatOnRunner(p) {
		x, err := s.projectChatRunner(sv.c, p, want)
		if err != nil {
			return nil, "", err
		}
		return runnerChat{fleet: s.fleet(), runner: x, user: sv.c.User, relay: relay, project: p.ID, goal: g.ID}, "", nil
	}
	if s.Chat == nil {
		return nil, "", errChatUnavailable
	}
	return s.Chat, p.Path, nil
}

// coordinatorHistory is how many of a coordinator's earlier messages it
// is given each turn: the goal's state, given with every turn, carries
// what is older.
const coordinatorHistory = 40

// coordinatorPrompt is what g's coordinator is given for a new message:
// its latest conversation, and the message with the goal's state in front.
func (s *Server) coordinatorPrompt(sv *service, g model.Goal, history []agent.ChatMessage, msg agent.ChatMessage) []agent.ChatMessage {
	if n := len(history) - coordinatorHistory; n > 0 {
		history = history[n:]
	}
	out := append(slices.Clip(history), msg)
	last := &out[len(out)-1]
	last.Content = s.goalContext(sv, g) + "\n\n" + last.Content
	return agent.WithShellText(out)
}

// goalContext describes g as it stands, for its coordinator.
func (s *Server) goalContext(sv *service, g model.Goal) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<goal id=%q status=%q mode=%q>\n<title>%s</title>\n<description>\n%s\n</description>\n", g.ID, g.Status, g.Mode, g.Title, g.Description)
	switch g.Mode {
	case model.GoalAsk:
		b.WriteString("<mode>Ask before queuing: your plan waits for the user to accept it, and tasks you create stay drafts for the user to queue. " +
			"You may send a task back with notes.</mode>\n")
	default:
		b.WriteString("<mode>Queue on its own: your plan and follow-ups are queued right away.</mode>\n")
	}
	ts := sv.data.GoalTodos(g)
	var budget []string
	if m := g.Budget.MaxTasks; m > 0 {
		budget = append(budget, fmt.Sprintf("%d tasks at most, %d made", m, len(ts)))
	}
	if sh := g.Budget.WeeklyShare; sh > 0 {
		line := fmt.Sprintf("%d%% of the weekly usage window", sh)
		if used, ok := s.goalWeekUsed(sv, g); ok {
			line += fmt.Sprintf(", %.1f%% used", used)
		}
		budget = append(budget, line)
	}
	if len(budget) > 0 {
		fmt.Fprintf(&b, "<budget>%s</budget>\n", strings.Join(budget, "; "))
	}
	if len(g.Plan) > 0 {
		fmt.Fprintf(&b, "<plan waiting_for_user=\"true\">%d tasks</plan>\n", len(g.Plan))
	}
	if g.Note != "" {
		fmt.Fprintf(&b, "<note>%s</note>\n", g.Note)
	}
	b.WriteString("<tasks>\n")
	for _, t := range ts {
		fmt.Fprintf(&b, "<task id=%q status=%q", t.ID, t.Status)
		if len(t.DependsOn) > 0 {
			fmt.Fprintf(&b, " depends_on=%q", strings.Join(t.DependsOn, ","))
		}
		fmt.Fprintf(&b, ">%s", taskName(t))
		if t.Error != "" {
			fmt.Fprintf(&b, " (error: %s)", textutil.OneLine(t.Error, 200))
		}
		b.WriteString("</task>\n")
	}
	b.WriteString("</tasks>\n")
	log := g.Log
	if n := len(log) - 10; n > 0 {
		log = log[n:]
	}
	if len(log) > 0 {
		b.WriteString("<recent_log>\n")
		for _, l := range log {
			fmt.Fprintf(&b, "%s %s: %s\n", l.At.Format(time.DateTime), l.Kind, textutil.OneLine(l.Text, 300))
		}
		b.WriteString("</recent_log>\n")
	}
	b.WriteString("</goal>")
	return b.String()
}

// goalWeekUsed estimates how much of the weekly usage window g's tasks used,
// in percent: the window's utilization times the goal's tasks' part of the
// cost of all the org's tasks since the window started. ok is false when
// the window isn't known (no usage reported yet).
func (s *Server) goalWeekUsed(sv *service, g model.Goal) (float64, bool) {
	p, err := sv.data.GetProject(g.ProjectID)
	if err != nil {
		return 0, false
	}
	week := s.weekWindow(p)
	if week == nil {
		return 0, false
	}
	since := week.ResetsAt.Add(-usage.WeekSpan)
	var mine, all float64
	for _, t := range sv.data.ListTodos("") {
		var c float64
		for _, r := range t.RunStats {
			if !r.At.Before(since) {
				c += r.CostUSD
			}
		}
		all += c
		if t.GoalID == g.ID {
			mine += c
		}
	}
	if all == 0 {
		return 0, true
	}
	return week.Utilization * 100 * mine / all, true
}

// weekWindow is the weekly usage window p's tasks count against: this
// Dash's, or on a hub that of a runner p is linked to that reports one.
func (s *Server) weekWindow(p model.Project) *usage.Window {
	t := time.Now()
	if !s.hub() {
		if s.Usage == nil {
			return nil
		}
		return usage.WeekOf(s.Usage.Snapshot().Windows, t)
	}
	for _, r := range s.fleet().List() {
		if !slices.Contains(p.Runners, r.ID) || r.Reported == nil || r.Reported.Usage == nil || !r.Reported.Usage.Known {
			continue
		}
		if w := usage.WeekOf(r.Reported.Usage.Windows, t); w != nil {
			return w
		}
	}
	return nil
}
