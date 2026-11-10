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

	"github.com/klahr/dash/internal/tunnel"
)

// fakeRepo makes dir look like a git repository on branch, with origin
// remote (if not ""), without running git.
func fakeRepo(t *testing.T, dir, branch, remote string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	head := "ref: refs/heads/" + branch + "\n"
	if branch == "" {
		head = "0123456789abcdef0123456789abcdef01234567\n"
	}
	cfg := "[core]\n\tbare = false\n"
	if remote != "" {
		cfg += "[remote \"upstream\"]\n\turl = https://example.com/not-origin.git\n[remote \"origin\"]\n\turl = " + remote + "\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	}
	for name, body := range map[string]string{"HEAD": head, "config": cfg} {
		if err := os.WriteFile(filepath.Join(dir, ".git", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The scan finds repositories at the top of the projects directory and one
// level down, and nothing else: not the hub's clones, hidden folders,
// deeper ones, existing projects, or anything a symlink leads to (inside
// or outside the directory).
func TestFindReposStaysInsideTheProjectsDirectory(t *testing.T) {
	parallel(t)

	root := filepath.Join(t.TempDir(), "code")
	outside := t.TempDir()
	fakeRepo(t, filepath.Join(root, "app"), "main", "https://alice:s3cret@github.com/x/app.git")
	fakeRepo(t, filepath.Join(root, "org", "lib"), "dev", "git@github.com:x/lib.git")
	fakeRepo(t, filepath.Join(root, "org", "detached"), "", "")
	fakeRepo(t, filepath.Join(root, "token"), "main", "https://ghp_abc@github.com/x/token.git")
	fakeRepo(t, filepath.Join(root, "hub", "clone"), "main", "https://github.com/x/clone.git")
	fakeRepo(t, filepath.Join(root, "a", "b", "too-deep"), "main", "")
	fakeRepo(t, filepath.Join(root, ".hidden"), "main", "")
	fakeRepo(t, filepath.Join(root, "known"), "main", "")
	fakeRepo(t, filepath.Join(outside, "secret"), "main", "https://github.com/x/secret.git")
	fakeRepo(t, filepath.Join(outside, "nest", "secret2"), "main", "")
	if err := os.MkdirAll(filepath.Join(root, "plain", "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		filepath.Join(root, "linked"):        filepath.Join(outside, "secret"),
		filepath.Join(root, "nest"):          filepath.Join(outside, "nest"),
		filepath.Join(root, "org", "linked"): filepath.Join(outside, "secret"),
		filepath.Join(root, "again"):         filepath.Join(root, "app"),
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	// A repository whose .git is a symlink out of the directory.
	if err := os.MkdirAll(filepath.Join(root, "gitlink"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret", ".git"), filepath.Join(root, "gitlink", ".git")); err != nil {
		t.Fatal(err)
	}

	known := filepath.Join(root, "known")
	got, err := findRepos(root, "hub", func(abs string) bool { return abs == known })
	if err != nil {
		t.Fatal(err)
	}
	want := []LocalRepo{
		{Name: "app", Path: "app", Remote: "https://github.com/x/app.git", Branch: "main"},
		{Name: "detached", Path: "org/detached"},
		{Name: "lib", Path: "org/lib", Remote: "git@github.com:x/lib.git", Branch: "dev"},
		{Name: "token", Path: "token", Remote: "https://github.com/x/token.git", Branch: "main"},
	}
	if len(got) != len(want) {
		t.Fatalf("found %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("repo %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	for _, r := range got {
		if strings.Contains(r.Remote, "s3cret") || strings.Contains(r.Remote, "ghp_") {
			t.Errorf("credentials in a remote: %+v", r)
		}
	}

	if _, err := findRepos("", "hub", func(string) bool { return false }); err == nil {
		t.Error("a runner without a projects directory listed something")
	}
}

// ownerCall is a request from the hub to its runner for the hub's user uid
// ("" says for nobody).
func ownerCall(h http.Handler, method, path, body, uid string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if uid != "" {
		r.Header.Set(tunnel.UserHeader, uid)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// A runner lists and adds its repositories only for its owner, and adds
// only what the listing has: nothing under hub/, outside -projects, or
// already a project.
func TestRunnerAddsRepositoriesOnlyForItsOwner(t *testing.T) {
	parallel(t)

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	s, h, _ := runnerSide(t, 7)
	s.HubUser = 42
	ctx := context.Background()
	for _, p := range []string{"app", "org/lib", "hub/clone"} {
		if err := s.Git.InitRepo(ctx, "x", filepath.Join(s.ProjectRoot, p), "main"); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := s.Git.InitRepo(ctx, "x", outside, "main"); err != nil {
		t.Fatal(err)
	}

	list := func(uid string) (int, []LocalRepo) {
		w := ownerCall(h, "GET", "/api/hub/repos", "", uid)
		var out struct{ Repos []LocalRepo }
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Repos
	}
	for _, uid := range []string{"", "7"} {
		if code, _ := list(uid); code != http.StatusForbidden {
			t.Errorf("listing for user %q: %d", uid, code)
		}
		if code := ownerCall(h, "POST", "/api/hub/repos", `{"paths":["app"]}`, uid); code.Code != http.StatusForbidden {
			t.Errorf("adding for user %q: %d", uid, code.Code)
		}
	}
	code, repos := list("42")
	if code != http.StatusOK || len(repos) != 2 || repos[0].Path != "app" || repos[1].Path != "org/lib" || repos[0].Branch != "main" {
		t.Fatalf("owner's listing: %d %+v", code, repos)
	}

	before := len(raw(s).ListProjects())
	w := ownerCall(h, "POST", "/api/hub/repos", `{"paths":["app","app","hub/clone","../outside","`+outside+`","org/../app"]}`, "42")
	var out AddedRepos
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	if len(out.Added) != 1 || out.Added[0].Name != "app" || out.Added[0].Path != filepath.Join(s.ProjectRoot, "app") || out.Added[0].DefaultBranch != "main" {
		t.Errorf("added %+v", out.Added)
	}
	if len(out.Failed) != 5 {
		t.Errorf("refused %+v", out.Failed)
	}
	if n := len(raw(s).ListProjects()); n != before+1 {
		t.Errorf("%d projects after adding one to %d", n, before)
	}
	// Once added, it's no longer listed.
	if _, repos := list("42"); len(repos) != 1 || repos[0].Path != "org/lib" {
		t.Errorf("listing after adding: %+v", repos)
	}
}

// On the hub, only a runner's owner asks it for its repositories: not
// another member, not an org admin, not another org.
func TestOnlyARunnersOwnerListsItsRepositories(t *testing.T) {
	parallel(t)

	f := newFleetServer(t)
	cBox := "/api/runners/" + f.ids["c-box"] + "/repos"
	for _, c := range []struct {
		who  string
		h    http.Handler
		path string
		want int
	}{
		{"another member", f.u, cBox, http.StatusForbidden},
		{"an org admin", f.d, cBox, http.StatusForbidden},
		{"another org", f.b, cBox, http.StatusNotFound},
		{"nobody signed in", f.h, cBox, http.StatusUnauthorized},
		// The owner gets through to the runner, which is offline here.
		{"the owner", f.c, cBox, http.StatusServiceUnavailable},
	} {
		if w := send(c.h, "GET", c.path, ""); w.Code != c.want {
			t.Errorf("listing as %s: %d %s, want %d", c.who, w.Code, w.Body, c.want)
		}
		if w := send(c.h, "POST", c.path, `{"paths":["app"]}`); w.Code != c.want {
			t.Errorf("adding as %s: %d %s, want %d", c.who, w.Code, w.Body, c.want)
		}
	}
	if w := send(f.c, "POST", cBox, `{"paths":[]}`); w.Code != http.StatusBadRequest {
		t.Errorf("adding nothing: %d", w.Code)
	}
}
