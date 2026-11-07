package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestLookupGitHubRepo(t *testing.T) {
	s := newProjectServer(t)
	for name, want := range map[string]bool{"there": true, "nowhere": false} {
		w := httptest.NewRecorder()
		s.lookupGitHubRepo(w, httptest.NewRequest("GET", "/api/github/repo?name="+name, nil))
		var got struct {
			Exists bool   `json:"exists"`
			Name   string `json:"name"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Exists != want {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		if want && got.Name != "me/there" {
			t.Fatalf("want me/there, got %q", got.Name)
		}
	}
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
