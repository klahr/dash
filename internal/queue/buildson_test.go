package queue

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

// goneExec is a fakeExec whose Claude Code has no session named gone.
type goneExec struct {
	*fakeExec
	gone string
}

func (g goneExec) Run(ctx context.Context, req agent.ExecRequest, emit func(model.RunEvent)) (agent.ExecResult, error) {
	if req.Resume != "" && req.Resume == g.gone {
		g.mu.Lock()
		g.calls = append(g.calls, req)
		g.mu.Unlock()
		return agent.ExecResult{ResumeFailed: true}, errors.New("claude exited: exit status 1")
	}
	return g.fakeExec.Run(ctx, req, emit)
}

// queueBuildingOn adds a queued task building on parent.
func (h *harness) queueBuildingOn(title string, parent model.Todo) model.Todo {
	h.t.Helper()
	td, err := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: title, Prompt: "do " + title, BuildsOn: parent.ID})
	if err != nil {
		h.t.Fatal(err)
	}
	if td, err = h.st.SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		h.t.Fatal(err)
	}
	h.engine.Kick()
	return td
}

func TestBuildsOnForksTheEarlierTasksSessionOnTheFirstRun(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		// The parent passes at once; the follow-up needs a fix.
		if n == 2 {
			write(t, req.Dir, "b.txt", "broken\n")
		} else {
			write(t, req.Dir, map[bool]string{true: "a.txt", false: "b.txt"}[n == 1], "pass\n")
		}
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p", TestCommand: "! grep -q broken *.txt", MaxAutoFixes: 1})

	// A draft parent holds the follow-up back, like a dependency.
	parent, err := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: "first", Prompt: "do first"})
	if err != nil {
		t.Fatal(err)
	}
	child := h.queueBuildingOn("second", parent)
	time.Sleep(300 * time.Millisecond)
	if n := len(exe.Calls()); n != 0 {
		t.Fatalf("the follow-up started before the task it builds on: %d runs", n)
	}
	if _, err := h.st.SetStatus(parent.ID, model.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	h.engine.Kick()
	parent = h.waitFor(parent.ID, model.StatusReview)
	h.approve(parent.ID)
	parent = h.waitFor(parent.ID, model.StatusMerged)
	if parent.LastSessionID == "" {
		t.Fatal("a merged task keeps its last session")
	}

	child = h.waitFor(child.ID, model.StatusReview)
	calls := exe.Calls()
	if len(calls) != 3 {
		t.Fatalf("want the parent's run and two of the follow-up, got %d", len(calls))
	}
	first := calls[1]
	if first.Resume != parent.LastSessionID || !first.ForkSession {
		t.Fatalf("first run: want a fork of %q, got resume %q fork %v", parent.LastSessionID, first.Resume, first.ForkSession)
	}
	if !strings.Contains(first.Prompt, "follow-up task") || !strings.Contains(first.Prompt, `"first"`) ||
		!strings.Contains(first.Prompt, "do second") || first.Dir == calls[0].Dir {
		t.Fatalf("first run's prompt should close the earlier task and give the new one:\n%s", first.Prompt)
	}
	if child.SessionID == "" || child.SessionID == parent.LastSessionID || child.LastSessionID != child.SessionID {
		t.Fatalf("the follow-up keeps its own session: %q (parent's %q, last %q)", child.SessionID, parent.LastSessionID, child.LastSessionID)
	}
	if second := calls[2]; second.Resume != child.SessionID || second.ForkSession || strings.Contains(second.Prompt, "follow-up task") {
		t.Fatalf("later runs resume the task's own session: resume %q fork %v\n%s", second.Resume, second.ForkSession, second.Prompt)
	}
}

func TestBuildsOnStartsFreshWhenTheEarlierSessionIsGone(t *testing.T) {
	parallel(t)

	fe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "a\n")
		return nil
	}}
	h := newHarness(t, goneExec{fakeExec: fe, gone: "gone"}, model.Project{Name: "p"})
	parent := h.waitFor(h.add("first").ID, model.StatusReview)
	// Not merged, and its session is on another computer.
	if _, err := h.st.SetStatus(parent.ID, model.StatusFailed, "rejected"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.UpdateTodo(parent.ID, func(x *model.Todo) error { x.SessionID, x.LastSessionID = "", "gone"; return nil }); err != nil {
		t.Fatal(err)
	}

	child := h.waitFor(h.queueBuildingOn("second", parent).ID, model.StatusReview)
	calls := fe.Calls()
	if len(calls) != 3 {
		t.Fatalf("want the parent's run, the failed fork and a fresh start, got %d", len(calls))
	}
	if f := calls[1]; f.Resume != "gone" || !f.ForkSession || !strings.Contains(f.Prompt, "not merged") {
		t.Fatalf("the follow-up first tries the earlier session: resume %q fork %v\n%s", f.Resume, f.ForkSession, f.Prompt)
	}
	if f := calls[2]; f.Resume != "" || f.ForkSession || f.Prompt != agent.BuildPrompt(child) {
		t.Fatalf("then starts fresh with the full prompt: resume %q fork %v\n%s", f.Resume, f.ForkSession, f.Prompt)
	}
	if child.SessionID == "" || child.SessionID == "gone" {
		t.Fatalf("the fresh session is the task's own: %q", child.SessionID)
	}
	log, _ := h.engine.Log.Read(child.ID)
	if !strings.Contains(log, "isn't on this computer") {
		t.Fatalf("the log says why it started fresh:\n%s", log)
	}
}

func TestBuildsOnWithoutAnEarlierSessionStartsFresh(t *testing.T) {
	parallel(t)

	exe := &fakeExec{fn: func(n int, req agent.ExecRequest) error {
		write(t, req.Dir, "a.txt", "a\n")
		return nil
	}}
	h := newHarness(t, exe, model.Project{Name: "p"})
	// A failed task that never ran: finished, but with no session. Paused,
	// so the engine leaves it alone while it is queued.
	parent, err := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: "first", Prompt: "do first", Paused: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress, model.StatusFailed} {
		if _, err := h.st.SetStatus(parent.ID, s, ""); err != nil {
			t.Fatal(err)
		}
	}
	child := h.waitFor(h.queueBuildingOn("second", parent).ID, model.StatusReview)
	calls := exe.Calls()
	if len(calls) != 1 || calls[0].Resume != "" || calls[0].ForkSession || calls[0].Prompt != agent.BuildPrompt(child) {
		t.Fatalf("want one fresh run with the full prompt, got %+v", calls)
	}
	log, _ := h.engine.Log.Read(child.ID)
	if !strings.Contains(log, "has no Claude session") {
		t.Fatalf("the log says why it started fresh:\n%s", log)
	}
}
