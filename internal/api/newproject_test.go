package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// fakeGitHub is a gh that knows one repository, me/there: viewing it works,
// cloning makes a repo with a commit, and creating another adds an origin.
func fakeGitHub(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$1 $2" in
"repo view")
  case "$3" in
  there|me/there) echo '{"nameWithOwner":"me/there","url":"https://github.com/me/there"}' ;;
  *) echo "GraphQL: Could not resolve to a Repository with the name 'me/$3'. (repository)" >&2; exit 1 ;;
  esac ;;
"repo clone")
  git init -q -b trunk "$4" && git -C "$4" -c commit.gpgsign=false -c user.name=t -c user.email=t@t commit -q --allow-empty -m cloned &&
  git -C "$4" remote add origin https://github.com/$3.git ;;
"repo create")
  git remote add origin https://github.com/me/$3.git ;;
*) echo "unexpected gh $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func newProjectServer(t *testing.T) *Server {
	s, _, _ := boardServer(t)
	s.Git = gitops.New(nil)
	s.Git.GhBin = fakeGitHub(t)
	s.ProjectRoot = t.TempDir()
	return s
}

func createProjectReq(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.createProject(w, asOwner(t, s, httptest.NewRequest("POST", "/api/projects", strings.NewReader(body))))
	return w
}

func TestCreateProjectClonesExistingGitHubRepo(t *testing.T) {
	s := newProjectServer(t)
	if w := createProjectReq(t, s, `{"name":"There","github":"there"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "clone it") {
		t.Fatalf("existing repo without clone: want 400, got %d %s", w.Code, w.Body)
	}
	w := createProjectReq(t, s, `{"name":"There","github":"me/there","clone":true}`)
	var p model.Project
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &p) != nil {
		t.Fatalf("clone: %d %s", w.Code, w.Body)
	}
	if p.RemoteURL != "https://github.com/me/there.git" || p.DefaultBranch != "trunk" || p.Path != filepath.Join(s.ProjectRoot, "default", "there") {
		t.Fatalf("cloned project: %+v", p)
	}
	if w := createProjectReq(t, s, `{"name":"Gone","github":"gone","clone":true}`); w.Code != http.StatusBadRequest {
		t.Fatalf("clone of a missing repo: want 400, got %d %s", w.Code, w.Body)
	}
}

func TestCreateProjectCreatesGitHubRepo(t *testing.T) {
	s := newProjectServer(t)
	w := createProjectReq(t, s, `{"name":"Fresh","github":"fresh"}`)
	var p model.Project
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &p) != nil {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if p.RemoteURL != "https://github.com/me/fresh.git" {
		t.Fatalf("want origin set, got %+v", p)
	}
}

// gitClone makes a real repository at dir on branch, with one commit and an
// origin, as a clone made outside Dash would be.
func gitClone(t *testing.T, dir, branch string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", branch, dir},
		{"-C", dir, "-c", "commit.gpgsign=false", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "first"},
		{"-C", dir, "remote", "add", "origin", "https://github.com/me/" + filepath.Base(dir) + ".git"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

// An existing clone in the org's folder is added as a project named after
// its folder on its own branch, and then can't be added again.
func TestAddExistingClone(t *testing.T) {
	s := newProjectServer(t)
	org := filepath.Join(s.ProjectRoot, "default")
	gitClone(t, filepath.Join(org, "app"), "trunk")
	gitClone(t, filepath.Join(org, "team", "lib"), "main")
	if err := os.MkdirAll(filepath.Join(org, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(org, "app")
	w := createProjectReq(t, s, `{"existing":true,"path":`+jsonString(dir)+`}`)
	var p model.Project
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &p) != nil {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	if p.Name != "app" || p.Path != dir || p.DefaultBranch != "trunk" || p.RemoteURL != "https://github.com/me/app.git" {
		t.Fatalf("added project: %+v", p)
	}

	for name, body := range map[string]string{
		"again":       `{"existing":true,"path":` + jsonString(p.Path) + `}`,
		"not a repo":  `{"existing":true,"path":` + jsonString(filepath.Join(org, "notes")) + `}`,
		"missing":     `{"existing":true,"path":` + jsonString(filepath.Join(org, "gone")) + `}`,
		"no path":     `{"existing":true,"name":"x"}`,
		"with github": `{"existing":true,"path":` + jsonString(filepath.Join(org, "team", "lib")) + `,"github":"there"}`,
		"outside org": `{"existing":true,"path":` + jsonString(t.TempDir()) + `}`,
	} {
		if w := createProjectReq(t, s, body); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d %s", name, w.Code, w.Body)
		}
	}
	if _, err := os.Stat(filepath.Join(org, "gone")); !os.IsNotExist(err) {
		t.Fatalf("a missing clone must not be created: %v", err)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// A GitHub project lands work through pull requests unless its user chose
// otherwise; one without a GitHub remote, or on a Dash without gh, merges.
// The default follows the remote as settings are saved; a choice sticks.
func TestDefaultMergeMode(t *testing.T) {
	s := newProjectServer(t)
	org := filepath.Join(s.ProjectRoot, "default")
	add := func(body string) model.Project {
		t.Helper()
		w := createProjectReq(t, s, body)
		var p model.Project
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &p) != nil {
			t.Fatalf("create %s: %d %s", body, w.Code, w.Body)
		}
		return p
	}
	patch := func(p model.Project, body string) model.Project {
		t.Helper()
		w := httptest.NewRecorder()
		s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("PATCH", "/api/projects/"+p.ID, strings.NewReader(body))))
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &p) != nil {
			t.Fatalf("patch %s: %d %s", body, w.Code, w.Body)
		}
		return p
	}
	for _, dir := range []string{"app", "chosen", "nogh"} {
		gitClone(t, filepath.Join(org, dir), "main")
	}

	if p := add(`{"existing":true,"path":` + jsonString(filepath.Join(org, "app")) + `}`); !p.UsesPR() || p.MergeModeSet || !p.MergesGreenPRs() {
		t.Errorf("GitHub clone: %q set %v green %v", p.MergeMode, p.MergeModeSet, p.MergesGreenPRs())
	} else {
		if p = patch(p, `{"name":"app","merge_mode":"pr"}`); !p.UsesPR() || p.MergeModeSet {
			t.Errorf("saved unchanged: %q set %v", p.MergeMode, p.MergeModeSet)
		}
		if p = patch(p, `{"merge_mode":"local","merge_green_prs":false}`); p.UsesPR() || !p.MergeModeSet || p.MergesGreenPRs() {
			t.Errorf("chose to merge: %q set %v green %v", p.MergeMode, p.MergeModeSet, p.MergesGreenPRs())
		}
		if p = patch(p, `{"name":"app again"}`); p.UsesPR() {
			t.Error("the default overrode the user's choice")
		}
	}
	if p := add(`{"existing":true,"merge_mode":"local","path":` + jsonString(filepath.Join(org, "chosen")) + `}`); p.UsesPR() || !p.MergeModeSet {
		t.Errorf("GitHub clone, asked to merge: %q set %v", p.MergeMode, p.MergeModeSet)
	}

	// No remote: merge; once it has a GitHub origin, pull requests.
	p := add(`{"name":"fresh"}`)
	if p.UsesPR() || p.MergeModeSet {
		t.Errorf("no remote: %q set %v", p.MergeMode, p.MergeModeSet)
	}
	if out, err := exec.Command("git", "-C", p.Path, "remote", "add", "origin", "https://github.com/me/fresh.git").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if p = patch(p, `{"merge_mode":"local"}`); !p.UsesPR() || p.MergeModeSet {
		t.Errorf("origin added: %q set %v", p.MergeMode, p.MergeModeSet)
	}

	// No gh on this Dash: a GitHub clone merges.
	s.Git.GhBin = filepath.Join(t.TempDir(), "no-gh")
	if p := add(`{"existing":true,"path":` + jsonString(filepath.Join(org, "nogh")) + `}`); p.UsesPR() || p.MergeModeSet {
		t.Errorf("GitHub clone without gh: %q set %v", p.MergeMode, p.MergeModeSet)
	}
}
