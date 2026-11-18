package api

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	args = append([]string{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A task's active_work lists the project's other queued, running and
// in-review tasks with the files their worktrees change, uncommitted ones
// included, but not the task itself.
func TestActiveWorkTool(t *testing.T) {
	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := s.Git.InitRepo(context.Background(), p.ID, repo, "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "shared.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "add", "-A")
	gitCmd(t, repo, "commit", "-q", "-m", "base")
	if _, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.Path, x.DefaultBranch = repo, "main"; return nil }); err != nil {
		t.Fatal(err)
	}

	wt := filepath.Join(root, "wt-other")
	gitCmd(t, repo, "worktree", "add", "-q", "-b", "dash/other", wt)
	if err := os.WriteFile(filepath.Join(wt, "shared.go"), []byte("package x // changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	self := withStatus(t, s, p.ID, model.StatusInProgress)
	other := withStatus(t, s, p.ID, model.StatusInProgress)
	if _, err := raw(s).UpdateTodo(other.ID, func(x *model.Todo) error {
		x.Title, x.Prompt = "Refactor shared", "Refactor shared\nMove helpers into shared.go."
		x.Worktree = &model.Worktree{Path: wt, Branch: "dash/other", BaseBranch: "main"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	queued := withStatus(t, s, p.ID, model.StatusQueued)

	token, release := s.Board.RegisterTask(testOwner(t, s), p.ID, self.ID)
	defer release()
	h := s.Routes()
	text, isErr := call(t, h, token, "active_work", `{}`)
	if isErr || strings.Contains(text, self.ID) {
		t.Fatalf("active_work: %s", text)
	}
	want := "- " + other.ID + " [running] Refactor shared\n  intent: Move helpers into shared.go.\n  files: shared.go"
	if !strings.HasPrefix(text, want) || !strings.Contains(text, "- "+queued.ID+" [queued]") {
		t.Fatalf("active_work:\n%s\nwant to start with:\n%s", text, want)
	}

	if text, _ := call(t, h, token, "active_work", `{"paths":["docs"]}`); !strings.HasPrefix(text, "No other active task") {
		t.Fatalf("active_work on untouched paths: %s", text)
	}
	if text, _ := call(t, h, token, "active_work", `{"paths":["shared.go"]}`); !strings.HasPrefix(text, "- "+other.ID) || strings.Contains(text, queued.ID) {
		t.Fatalf("active_work on shared.go: %s", text)
	}
}

// filesFleet stands in for a hub's runners when asked for a task's files:
// "new" knows /files, "old" (from before it) only /diff.
type filesFleet struct{ hubFleet }

func (f *filesFleet) Call(ctx context.Context, id, method, path string, in, out any) error {
	b, _ := out.(*[]byte)
	switch {
	case id == "new" && strings.HasSuffix(path, "/files"):
		*b = []byte("web/app.js\nweb/style.css\n")
	case id == "old" && strings.HasSuffix(path, "/diff"):
		*b = []byte(" web/app.js | 1 +\n\ndiff --git a/web/app.js b/web/app.js\n")
	default:
		return errors.New("404 page not found")
	}
	return nil
}

// On a hub, a task running on one runner sees the project's tasks on every
// runner: its call goes to the hub, which asks each task's runner for its
// files.
func TestActiveWorkOnHubCoversTheProject(t *testing.T) {
	parallel(t)

	hub, runner, creds, linked, _ := hubWithRunner(t)
	hub.Dispatch = &dispatch.Dispatcher{Store: raw(hub), Fleet: &filesFleet{hubFleet{hub: hub}}}
	rp, err := raw(runner).CreateProject(model.Project{Name: "clone", HubProjectID: linked.ID, HubCheckout: model.HubCloned})
	if err != nil {
		t.Fatal(err)
	}
	onRunner := func(runnerID, title string, st model.TodoStatus) model.Todo {
		td := withStatus(t, hub, linked.ID, st)
		td, err := raw(hub).UpdateTodo(td.ID, func(x *model.Todo) error { x.Runner, x.Title, x.Prompt = runnerID, title, title; return nil })
		if err != nil {
			t.Fatal(err)
		}
		return td
	}
	self := onRunner(creds.ID, "this one", model.StatusInProgress)
	onNew := onRunner("new", "on a new runner", model.StatusInProgress)
	onOld := onRunner("old", "on an old runner", model.StatusReview)
	gone := onRunner("gone", "on a runner that's away", model.StatusReview)

	local, err := raw(runner).CreateTodo(model.Todo{ProjectID: rp.ID, Title: "this one", Prompt: "p", HubTodoID: self.ID})
	if err != nil {
		t.Fatal(err)
	}
	token, release := runner.Board.RegisterTask(chatCaller(t, runner), rp.ID, local.ID)
	defer release()
	text, isErr := call(t, runner.BoardMCP(), token, "active_work", `{}`)
	if isErr {
		t.Fatalf("active_work: %s", text)
	}
	for _, want := range []string{
		"- " + onNew.ID + " [running] on a new runner\n  files: web/app.js, web/style.css",
		"- " + onOld.ID + " [review] on an old runner\n  files: web/app.js",
		"- " + gone.ID + " [review] on a runner that's away\n  files: unknown (404 page not found)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("active_work lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, self.ID) || strings.Contains(text, local.ID) {
		t.Errorf("active_work lists the calling task:\n%s", text)
	}
}
