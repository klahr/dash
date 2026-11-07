package ci

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

type nopPub struct{}

func (nopPub) Publish(model.Event) {}

// newWatcher returns a watcher whose gh prints runs.json for `run list`
// and a fixed log for `run view --log-failed`.
func newWatcher(t *testing.T, investigate bool) (*Watcher, model.Project, func(string)) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "dash.db"), nopPub{})
	if err != nil {
		t.Fatal(err)
	}
	runsFile := filepath.Join(dir, "runs.json")
	gh := filepath.Join(dir, "gh")
	script := "#!/bin/sh\ncase \"$1 $2\" in\n\"run list\") cat " + runsFile + " ;;\n" +
		"\"run view\") echo 'FAIL TestLogin: expected 200, got 500' ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g := gitops.New(nopPub{})
	g.GhBin = gh
	p, err := st.CreateProject(model.Project{Name: "p", Path: dir, DefaultBranch: "main", RemoteURL: "git@github.com:o/r.git", InvestigateCI: &investigate})
	if err != nil {
		t.Fatal(err)
	}
	setRuns := func(js string) {
		if err := os.WriteFile(runsFile, []byte(js), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Watcher{Store: st, Git: g, Pub: nopPub{}}, p, setRuns
}

func run(id int, wf, sha, status, conclusion string) string {
	return `{"databaseId":` + itoa(id) + `,"workflowName":"` + wf + `","displayTitle":"t","status":"` + status +
		`","conclusion":"` + conclusion + `","headSha":"` + sha + `","url":"https://github.com/o/r/actions/runs/` + itoa(id) +
		`","event":"push","createdAt":"2026-11-06T10:00:00Z"}`
}

func itoa(n int) string { return strconv.Itoa(n) }

func runs(rs ...string) string { return "[" + strings.Join(rs, ",") + "]" }

func TestFailedBuildIsInvestigatedOnce(t *testing.T) {
	w, p, setRuns := newWatcher(t, true)
	ctx := context.Background()

	setRuns(runs(run(10, "CI", "aaa", "completed", "success")))
	if st, _ := w.Refresh(ctx, p.ID); st.State != Passing {
		t.Fatalf("want passing, got %+v", st)
	}

	setRuns(runs(run(12, "CI", "bbb", "completed", "failure"), run(11, "Lint", "bbb", "completed", "success"), run(10, "CI", "aaa", "completed", "success")))
	st, _ := w.Refresh(ctx, p.ID)
	if st.State != Failing || st.TodoID == "" {
		t.Fatalf("want failing with a fix todo, got %+v", st)
	}
	fix, _ := w.Store.GetTodo(st.TodoID)
	if fix.Status != model.StatusQueued || fix.CIRun != 12 || !strings.Contains(fix.Prompt, "TestLogin") || fix.Title != "Fix failing build: CI" {
		t.Fatalf("unexpected fix todo: %+v", fix)
	}

	// Polling again, or a newer failure while the fix is open, adds nothing.
	w.Refresh(ctx, p.ID)
	setRuns(runs(run(13, "CI", "ccc", "completed", "failure"), run(12, "CI", "bbb", "completed", "failure")))
	w.Refresh(ctx, p.ID)
	if n := len(w.Store.ListTodos(p.ID)); n != 1 {
		t.Fatalf("want exactly one fix todo, got %d", n)
	}
}

func TestRunningAndDisabled(t *testing.T) {
	w, p, setRuns := newWatcher(t, false)
	ctx := context.Background()
	setRuns(runs(run(21, "CI", "bbb", "in_progress", ""), run(20, "CI", "aaa", "completed", "failure")))
	st, _ := w.Refresh(ctx, p.ID)
	if st.State != Running || st.SHA != "bbb" {
		t.Fatalf("want running for the newest commit, got %+v", st)
	}
	if n := len(w.Store.ListTodos(p.ID)); n != 0 {
		t.Fatalf("investigation is off; got %d todos", n)
	}
	// Marked as seen, so turning investigation on later doesn't dig up old failures…
	if p2, _ := w.Store.GetProject(p.ID); p2.CIHandledRun != 20 {
		t.Fatalf("want run 20 marked handled, got %d", p2.CIHandledRun)
	}
	// …but the button still works for the current failure.
	fix, err := w.Investigate(ctx, p.ID)
	if err != nil || fix.CIRun != 20 {
		t.Fatalf("manual investigate: %v %+v", err, fix)
	}
}

func TestGhErrorsAreReported(t *testing.T) {
	w, p, _ := newWatcher(t, true) // runs.json missing → gh fails
	st, err := w.Refresh(context.Background(), p.ID)
	if err == nil || st.State != Unknown || st.Error == "" {
		t.Fatalf("want an unknown state with an error, got %+v (%v)", st, err)
	}
}
