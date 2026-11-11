package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/model"
)

// hubLinkIn links the hub's project hpid with remote, of org with its
// folder named folder, to the runner h.
func hubLinkIn(t *testing.T, h http.Handler, hpid, remote string, org int64, folder string) (int, dispatch.Linked) {
	t.Helper()
	b, _ := json.Marshal(dispatch.Link{Name: "Shared App", RemoteURL: remote, DefaultBranch: "main", OrgID: org, OrgFolder: folder})
	w := send(h, "PUT", "/api/hub/projects/"+hpid, string(b))
	var out dispatch.Linked
	if w.Code/100 == 2 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Logf("link %s: %d %s", hpid, w.Code, w.Body)
	}
	return w.Code, out
}

// New links clone into <projects>/<org folder>/<repo>; the hub's folder
// name is made safe the way the hub's own projects' folder is, and a hub
// that sends none gets org-<id>, never the bare root.
func TestHubLinkClonesIntoTheOrgFolder(t *testing.T) {
	parallel(t)

	needGit(t)
	s, h, remote := runnerSide(t, 7)
	code, linked := hubLinkIn(t, h, "hub-p1", remote, 7, "Acme Corp")
	if want := filepath.Join(s.ProjectRoot, "acme-corp", "remote"); code != http.StatusCreated || linked.Path != want || linked.Adopted {
		t.Fatalf("link: %d %+v, want %s", code, linked, want)
	}
	if p, _ := raw(s).GetProject(linked.ProjectID); p.HubOrgFolder != "acme-corp" || p.HubCheckout != model.HubCloned {
		t.Errorf("project %+v", p)
	}
	// An older hub sends no folder.
	code, old := hubLinkAs(t, h, "hub-p2", remote)
	if want := filepath.Join(s.ProjectRoot, "org-7", "remote"); code != http.StatusCreated || old.Path != want {
		t.Errorf("link without a folder: %d %+v, want %s", code, old, want)
	}
}

// Two orgs' projects with the same repository name each get their own
// clone, in their own org's folder.
func TestHubLinkOrgsDontClash(t *testing.T) {
	parallel(t)

	needGit(t)
	s, h, remote := runnerSide(t, 0) // a runner taking any org's work
	_, a := hubLinkIn(t, h, "hub-a", remote, 7, "acme")
	_, b := hubLinkIn(t, h, "hub-b", remote, 8, "beta")
	if a.Path != filepath.Join(s.ProjectRoot, "acme", "remote") || b.Path != filepath.Join(s.ProjectRoot, "beta", "remote") ||
		a.Adopted || b.Adopted || a.ProjectID == b.ProjectID {
		t.Errorf("links: %+v %+v", a, b)
	}
}

// A checkout of the same remote at <projects>/<repo>, outside the org's
// folder, may be the user's own: a link never adopts it.
func TestHubLinkLeavesTheUsersCheckout(t *testing.T) {
	parallel(t)

	needGit(t)
	s, h, remote := runnerSide(t, 7)
	mine := filepath.Join(s.ProjectRoot, "remote")
	if err := s.Git.Clone(context.Background(), "x", remote, mine); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "wip.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, linked := hubLinkIn(t, h, "hub-p1", remote, 7, "acme")
	if code != http.StatusCreated || linked.Adopted || linked.Path != filepath.Join(s.ProjectRoot, "acme", "remote") {
		t.Fatalf("link: %d %+v", code, linked)
	}
	if b, err := os.ReadFile(filepath.Join(mine, "wip.txt")); err != nil || string(b) != "mine" {
		t.Errorf("the user's checkout was touched: %q %v", b, err)
	}
	// A plain repository in the org folder's place is in the way, not used.
	gitClone(t, filepath.Join(s.ProjectRoot, "beta"), "main")
	if code, _ := hubLinkIn(t, h, "hub-p2", remote, 7, "beta"); code != http.StatusBadRequest {
		t.Errorf("link into a repository: %d", code)
	}
}

// oldClone is a clone Dash made of remote straight in the projects root,
// before clones went in the org's folder, with a task's worktree outside
// it (where the runner keeps them) and a task branch.
func oldClone(t *testing.T, s *Server, remote, hpid, name, how, folder string) (model.Project, model.Todo) {
	t.Helper()
	path := filepath.Join(s.ProjectRoot, name)
	if err := s.Git.Clone(context.Background(), "x", remote, path); err != nil {
		t.Fatal(err)
	}
	gitIn(t, path, "commit", "--allow-empty", "-q", "-m", "first")
	wt := filepath.Join(t.TempDir(), "todo-1")
	gitIn(t, path, "worktree", "add", "-q", "-b", "dash/todo-1", wt)
	p, err := raw(s).CreateProject(model.Project{Name: "Shared App", Path: path, DefaultBranch: "main", RemoteURL: remote,
		HubProjectID: hpid, HubCheckout: how, HubOrgFolder: folder})
	if err != nil {
		t.Fatal(err)
	}
	td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Title: "t",
		Worktree: &model.Worktree{Name: "todo-1", Path: wt, Branch: "dash/todo-1"}})
	if err != nil {
		t.Fatal(err)
	}
	return p, td
}

// walkStatus takes the task id through statuses, in order.
func walkStatus(t *testing.T, s *Server, id string, statuses ...model.TodoStatus) {
	t.Helper()
	for _, st := range statuses {
		if _, err := raw(s).UpdateTodo(id, func(x *model.Todo) error { x.Status = st; return nil }); err != nil {
			t.Fatal(err)
		}
	}
}

// The upgrade moves a clone Dash made into the org's folder; the tasks'
// worktrees keep working, the store has the new path, and running it
// again changes nothing.
func TestMoveHubClones(t *testing.T) {
	parallel(t)

	needGit(t)
	s, h, remote := runnerSide(t, 7)
	p, td := oldClone(t, s, remote, "hub-p1", "remote", model.HubCloned, "acme")
	gitIn(t, td.Worktree.Path, "status")
	s.MoveHubClones(context.Background())
	want := filepath.Join(s.ProjectRoot, "acme", "remote")
	moved, _ := raw(s).GetProject(p.ID)
	if moved.Path != want {
		t.Fatalf("path %s, want %s", moved.Path, want)
	}
	if _, err := os.Stat(p.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the old folder is still there: %v", err)
	}
	gitIn(t, td.Worktree.Path, "status")
	if got := gitIn(t, td.Worktree.Path, "rev-parse", "--path-format=absolute", "--git-common-dir"); got != filepath.Join(want, ".git") {
		t.Errorf("the worktree's repository is %s", got)
	}
	if !s.Git.HasBranch(context.Background(), p.ID, want, "dash/todo-1") {
		t.Error("the task branch is gone")
	}
	if x, _ := raw(s).GetTodo(td.ID); x.Worktree == nil || x.Worktree.Path != td.Worktree.Path {
		t.Errorf("the task's worktree moved: %+v", x.Worktree)
	}
	// Again: nothing changes.
	s.MoveHubClones(context.Background())
	if again, _ := raw(s).GetProject(p.ID); again.Path != want {
		t.Errorf("second run: %s", again.Path)
	}
	if ents, _ := os.ReadDir(filepath.Join(s.ProjectRoot, "acme")); len(ents) != 1 {
		t.Errorf("acme holds %v", ents)
	}
	// The hub's next link gets the new path.
	if code, linked := hubLinkIn(t, h, "hub-p1", remote, 7, "acme"); code != http.StatusOK || linked.Path != want || linked.ProjectID != p.ID {
		t.Errorf("link: %d %+v", code, linked)
	}
}

// A clone from before the hub sent its org's folder moves at the hub's
// first link after the upgrade, which answers with the new path.
func TestHubLinkMovesAnOldClone(t *testing.T) {
	parallel(t)

	needGit(t)
	s, h, remote := runnerSide(t, 7)
	p, td := oldClone(t, s, remote, "hub-p1", "remote", model.HubCloned, "")
	s.MoveHubClones(context.Background()) // the org's folder isn't known yet
	if x, _ := raw(s).GetProject(p.ID); x.Path != p.Path {
		t.Fatalf("moved without a folder: %s", x.Path)
	}
	code, linked := hubLinkIn(t, h, "hub-p1", remote, 7, "acme")
	want := filepath.Join(s.ProjectRoot, "acme", "remote")
	if code != http.StatusOK || linked.Path != want || linked.ProjectID != p.ID {
		t.Fatalf("link: %d %+v", code, linked)
	}
	gitIn(t, td.Worktree.Path, "status")
}

// Adopted and legacy checkouts may be the user's own and stay where they
// are; a busy clone waits until it is idle.
func TestMoveHubClonesSkips(t *testing.T) {
	parallel(t)

	needGit(t)
	s, _, remote := runnerSide(t, 7)
	adopted, _ := oldClone(t, s, remote, "hub-a", "adopted", model.HubAdopted, "acme")
	legacy, _ := oldClone(t, s, remote, "hub-l", "legacy", model.HubLegacy, "acme")
	busy, td := oldClone(t, s, remote, "hub-b", "busy", model.HubCloned, "acme")
	walkStatus(t, s, td.ID, model.StatusQueued, model.StatusInProgress)
	s.MoveHubClones(context.Background())
	for _, p := range []model.Project{adopted, legacy, busy} {
		if x, _ := raw(s).GetProject(p.ID); x.Path != p.Path {
			t.Errorf("%s moved to %s", p.Path, x.Path)
		}
	}
	// Merging is busy too.
	walkStatus(t, s, td.ID, model.StatusReview, model.StatusMerging)
	s.MoveHubClones(context.Background())
	if x, _ := raw(s).GetProject(busy.ID); x.Path != busy.Path {
		t.Errorf("moved while merging: %s", x.Path)
	}
	// Once idle, it moves.
	walkStatus(t, s, td.ID, model.StatusMerged)
	s.MoveHubClones(context.Background())
	if x, _ := raw(s).GetProject(busy.ID); x.Path != filepath.Join(s.ProjectRoot, "acme", "remote") {
		t.Errorf("idle clone at %s", x.Path)
	}
	gitIn(t, td.Worktree.Path, "status")
}

// A move that fails part way is undone: the clone is back where it was,
// its worktrees work, and the store still has the old path.
func TestMoveHubCloneRollsBack(t *testing.T) {
	serial(t) // sets afterHubMove

	needGit(t)
	s, _, remote := runnerSide(t, 7)
	p, td := oldClone(t, s, remote, "hub-p1", "remote", model.HubCloned, "acme")
	afterHubMove = func(from, to string) error { return errors.New("injected") }
	defer func() { afterHubMove = nil }()
	s.MoveHubClones(context.Background())
	if x, _ := raw(s).GetProject(p.ID); x.Path != p.Path {
		t.Errorf("store has %s", x.Path)
	}
	if !isRepo(p.Path) {
		t.Fatal("the clone isn't back")
	}
	if _, err := os.Stat(filepath.Join(s.ProjectRoot, "acme")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the org folder made for the move stayed: %v", err)
	}
	gitIn(t, td.Worktree.Path, "status")
	if got := gitIn(t, td.Worktree.Path, "rev-parse", "--path-format=absolute", "--git-common-dir"); got != filepath.Join(p.Path, ".git") {
		t.Errorf("the worktree's repository is %s", got)
	}
}
