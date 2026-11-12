package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/model"
)

// hubLinkAs links the hub's project hpid with remote to the runner h.
func hubLinkAs(t *testing.T, h http.Handler, hpid, remote string) (int, dispatch.Linked) {
	t.Helper()
	b, _ := json.Marshal(dispatch.Link{Name: "Shared App", RemoteURL: remote, DefaultBranch: "main", OrgID: 7})
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

// gitIn runs git in dir and returns its trimmed output.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false", "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func needGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
}

// A clone of the same remote already in <projects>/<org>/<repo> is adopted, not
// cloned again, and the user's uncommitted work in it stays as it was.
func TestHubLinkAdoptsAnExistingClone(t *testing.T) {
	parallel(t)

	needGit(t)
	s, h, remote := runnerSide(t, 7)
	ctx := context.Background()
	mine := filepath.Join(s.ProjectRoot, "org-7", "remote")
	if err := s.Git.Clone(ctx, "x", remote, mine); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "wip.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	head := gitIn(t, mine, "rev-parse", "HEAD")

	code, linked := hubLinkAs(t, h, "hub-p1", remote)
	if code != http.StatusCreated || !linked.Adopted || linked.Path != mine {
		t.Fatalf("link: %d %+v", code, linked)
	}
	p, _ := raw(s).GetProject(linked.ProjectID)
	if p.HubCheckout != model.HubAdopted || p.HubProjectID != "hub-p1" || !p.GuardsCheckout() {
		t.Errorf("adopted project %+v", p)
	}
	if b, err := os.ReadFile(filepath.Join(mine, "wip.txt")); err != nil || string(b) != "mine" || gitIn(t, mine, "rev-parse", "HEAD") != head {
		t.Errorf("the clone was touched: %q %v", b, err)
	}
	if ents, _ := os.ReadDir(s.ProjectRoot); len(ents) != 1 {
		t.Errorf("another folder appeared: %v", ents)
	}
	// Tasks reach it by the hub's id.
	b, _ := json.Marshal(dispatch.Task{HubTodoID: "t1", HubProjectID: "hub-p1", Title: "t", Prompt: "p"})
	if w := send(h, "POST", "/api/hub/tasks", string(b)); w.Code != http.StatusCreated {
		t.Errorf("task: %d %s", w.Code, w.Body)
	}
}

// A link finishes its clone when the hub's request goes away (the browser
// that asked reloads): a clone killed half way would leave a folder with
// origin set but no valid HEAD, adopted, and failed on, by every later link.
func TestHubLinkClonesWhenTheRequestGoesAway(t *testing.T) {
	parallel(t)

	needGit(t)
	s, h, remote := runnerSide(t, 7)
	b, _ := json.Marshal(dispatch.Link{Name: "Shared App", RemoteURL: remote, DefaultBranch: "main", OrgID: 7})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(ctx, "PUT", "/api/hub/projects/hub-p1", strings.NewReader(string(b)))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("link: %d %s", w.Code, w.Body)
	}
	path := filepath.Join(s.ProjectRoot, "org-7", "remote")
	if gitIn(t, path, "rev-parse", "HEAD") != gitIn(t, remote, "rev-parse", "HEAD") {
		t.Error("the clone isn't origin's")
	}
	if code, linked := hubLinkAs(t, h, "hub-p1", remote); code != http.StatusOK || linked.Path != path {
		t.Errorf("linking again: %d %+v", code, linked)
	}
}

// The runner's own project in that folder becomes the hub's, keeping its id.
func TestHubLinkAdoptsTheRunnersOwnProject(t *testing.T) {
	parallel(t)

	needGit(t)
	s, h, remote := runnerSide(t, 7)
	ctx := context.Background()
	mine := filepath.Join(s.ProjectRoot, "org-7", "remote")
	if err := s.Git.Clone(ctx, "x", remote, mine); err != nil {
		t.Fatal(err)
	}
	own, err := raw(s).CreateProject(model.Project{Name: "mine", Path: mine, DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	before := len(raw(s).ListProjects())
	code, linked := hubLinkAs(t, h, "hub-p1", remote)
	if code != http.StatusOK || linked.ProjectID != own.ID || !linked.Adopted {
		t.Fatalf("link: %d %+v", code, linked)
	}
	if n := len(raw(s).ListProjects()); n != before {
		t.Errorf("%d projects, want %d", n, before)
	}
	// Unlinked, it is the runner's own again, in place.
	if w := send(h, "DELETE", "/api/hub/projects/hub-p1", ""); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	if p, _ := raw(s).GetProject(own.ID); p.HubProjectID != "" || p.HubCheckout != "" || p.Path != mine {
		t.Errorf("after unlinking %+v", p)
	}
}

// A folder holding another repository, files, or a clone already linked to
// another of the hub's projects is never used: the next free name is.
func TestHubLinkNameClashes(t *testing.T) {
	parallel(t)

	needGit(t)
	s, h, remote := runnerSide(t, 7)
	ctx := context.Background()
	org := filepath.Join(s.ProjectRoot, "org-7")
	other := filepath.Join(org, "remote")
	gitClone(t, other, "main") // another repository (origin github.com/me/remote)
	otherHead := gitIn(t, other, "rev-parse", "HEAD")
	plain := filepath.Join(org, "remote-2")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, "notes.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(org, "remote-3")); err != nil {
		t.Fatal(err)
	}

	code, linked := hubLinkAs(t, h, "hub-p1", remote)
	want := filepath.Join(org, "remote-4")
	if code != http.StatusCreated || linked.Path != want || linked.Adopted {
		t.Fatalf("link: %d %+v", code, linked)
	}
	if gitIn(t, other, "rev-parse", "HEAD") != otherHead || s.Git.RemoteURL(ctx, "", other) != "https://github.com/me/remote.git" {
		t.Error("the other repository was touched")
	}
	if ents, _ := os.ReadDir(plain); len(ents) != 1 {
		t.Errorf("the plain folder was touched: %v", ents)
	}
	if ents, _ := os.ReadDir(filepath.Join(org, "remote-3")); len(ents) != 0 {
		t.Errorf("something was written through the symlink: %v", ents)
	}

	// Another of the hub's projects with the same remote gets its own clone.
	code, second := hubLinkAs(t, h, "hub-p2", remote)
	if code != http.StatusCreated || second.Path != filepath.Join(org, "remote-5") || second.Adopted {
		t.Fatalf("second link: %d %+v", code, second)
	}
	// Linking again finds each by its id.
	if code, again := hubLinkAs(t, h, "hub-p1", remote); code != http.StatusOK || again.ProjectID != linked.ProjectID {
		t.Errorf("again: %d %+v", code, again)
	}
}

// The folder is named after the remote, cleaned of anything a path could
// use to leave the projects folder; the hub never sends a path.
func TestHubPlaceStaysInTheProjectsFolder(t *testing.T) {
	parallel(t)

	for in, want := range map[string]string{
		"dash":        "dash",
		"../../etc":   "etc",
		".hidden":     "hidden",
		"a b/c":       "a-b-c",
		"--x":         "x",
		"Dash.v2_ok":  "Dash.v2_ok",
		"..":          "",
		"日本":          "",
		"with/slash/": "with-slash",
	} {
		if got := folderName(in); got != want {
			t.Errorf("folderName(%q) = %q, want %q", in, got, want)
		}
	}
	s, _, _ := runnerSide(t, 7)
	sv := s.as(ownerCaller(t, s))
	for _, remote := range []string{"https://github.com/x/..git", "https://github.com/x/...git", "git@github.com:x/.git"} {
		place, err := s.hubPlace(context.Background(), sv, "hp", "org-7", "", remote)
		if err != nil || filepath.Dir(place.Path) != filepath.Join(s.ProjectRoot, "org-7", "x") {
			t.Errorf("%s: %+v %v", remote, place, err)
		}
	}
	for remote, want := range map[string]string{
		"https://github.com/klahr/dash.git": filepath.Join(s.ProjectRoot, "org-7", "klahr", "dash"),
		"git@github.com:klahr/dash.git":     filepath.Join(s.ProjectRoot, "org-7", "klahr", "dash"),
		"https://github.com/../dash.git":    filepath.Join(s.ProjectRoot, "org-7", "dash"),
	} {
		if place, err := s.hubPlace(context.Background(), sv, "hp", "org-7", "", remote); err != nil || place.Path != want {
			t.Errorf("%s: %+v %v, want %s", remote, place, err, want)
		}
	}
	for _, folder := range []string{"", "..", "a/b", "Org"} {
		if place, err := s.hubPlace(context.Background(), sv, "hp", folder, "x", "https://github.com/x/y.git"); err == nil {
			t.Errorf("placed in org folder %q: %+v", folder, place)
		}
	}
	s.ProjectRoot = ""
	if _, err := s.hubPlace(context.Background(), sv, "hp", "org-7", "x", "https://github.com/x/y.git"); err == nil {
		t.Error("placed without a projects folder")
	}
}

// Clones under <projects>/hub/ from before keep working where they are:
// the upgrade marks them as the hub's once, and tasks find them by id.
func TestHubLegacyClonesKeepWorking(t *testing.T) {
	parallel(t)

	needGit(t)
	s, h, remote := runnerSide(t, 7)
	ctx := context.Background()
	old := filepath.Join(s.ProjectRoot, "hub", "shared-app")
	if err := s.Git.Clone(ctx, "x", remote, old); err != nil {
		t.Fatal(err)
	}
	gitIn(t, old, "branch", "dash/todo-1") // a task branch
	legacy, err := raw(s).CreateProject(model.Project{Name: "Shared App", Path: old, DefaultBranch: "main", HubProjectID: "hub-p1", RemoteURL: remote})
	if err != nil {
		t.Fatal(err)
	}
	// Outside hub/, a HubProjectID never made a project the hub's.
	stray, _ := raw(s).CreateProject(model.Project{Name: "stray", Path: filepath.Join(s.ProjectRoot, "stray"), HubProjectID: "hub-p9"})

	task := func(hp, ht string) int {
		b, _ := json.Marshal(dispatch.Task{HubTodoID: ht, HubProjectID: hp, Title: "t", Prompt: "p"})
		return send(h, "POST", "/api/hub/tasks", string(b)).Code
	}
	marked, err := raw(s).BackfillHubCheckouts(LegacyHubClone(s.ProjectRoot))
	if err != nil || len(marked) != 1 || marked[0] != "Shared App" {
		t.Fatalf("backfill: %v %v", marked, err)
	}
	if again, _ := raw(s).BackfillHubCheckouts(LegacyHubClone(s.ProjectRoot)); len(again) != 0 {
		t.Errorf("second backfill marked %v", again)
	}
	if p, _ := raw(s).GetProject(legacy.ID); p.HubCheckout != model.HubLegacy || p.Path != old {
		t.Errorf("legacy project %+v", p)
	}
	if p, _ := raw(s).GetProject(stray.ID); p.HubCheckout != "" || p.HubProjectID != "hub-p9" {
		t.Errorf("stray project changed: %+v", p)
	}
	if code := task("hub-p1", "t1"); code != http.StatusCreated {
		t.Errorf("task for the old clone: %d", code)
	}
	if code := task("hub-p9", "t2"); code != http.StatusForbidden {
		t.Errorf("task for the stray project: %d", code)
	}
	// Linking again (as the hub does before every task) keeps it where it is.
	if code, linked := hubLinkAs(t, h, "hub-p1", remote); code != http.StatusOK || linked.ProjectID != legacy.ID {
		t.Errorf("link: %d %+v", code, linked)
	}
	if _, err := os.Stat(filepath.Join(s.ProjectRoot, "org-7", "remote")); !os.IsNotExist(err) {
		t.Errorf("cloned again: %v", err)
	}
	if !s.Git.HasBranch(ctx, "x", old, "dash/todo-1") {
		t.Error("the task branch is gone")
	}
	// A runner started without a projects folder marks nothing.
	if marked, _ := raw(s).BackfillHubCheckouts(LegacyHubClone("")); len(marked) != 0 {
		t.Errorf("marked without a root: %v", marked)
	}
}

// A run command in a hub project's checkout with uncommitted changes runs
// with a warning; a deploy is refused. Other projects aren't checked.
func TestCheckoutWarning(t *testing.T) {
	parallel(t)

	needGit(t)
	s, h, remote := runnerSide(t, 7)
	_, linked := hubLinkAs(t, h, "hub-p1", remote)
	p, _ := raw(s).GetProject(linked.ProjectID)
	ctx := context.Background()
	if w, err := s.checkoutWarning(ctx, p, true); w != "" || err != nil {
		t.Errorf("clean: %q %v", w, err)
	}
	gitIn(t, p.Path, "commit", "--allow-empty", "-q", "-m", "x")
	if err := os.WriteFile(filepath.Join(p.Path, "f.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, p.Path, "add", "f.txt")
	gitIn(t, p.Path, "commit", "-q", "-m", "f")
	if err := os.WriteFile(filepath.Join(p.Path, "f.txt"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w, err := s.checkoutWarning(ctx, p, false); !strings.Contains(w, "f.txt") || err != nil {
		t.Errorf("run: %q %v", w, err)
	}
	if _, err := s.checkoutWarning(ctx, p, true); err == nil || !strings.Contains(err.Error(), "commit or stash") {
		t.Errorf("deploy: %v", err)
	}
	own := p
	own.HubProjectID, own.HubCheckout = "", ""
	if w, err := s.checkoutWarning(ctx, own, true); w != "" || err != nil {
		t.Errorf("the runner's own project: %q %v", w, err)
	}
}
