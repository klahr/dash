package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// fakeGh is a gh that answers the calls listing and creating repositories,
// and logs them to the returned file.
func fakeGh(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "gh"), filepath.Join(dir, "calls")
	script := `#!/bin/sh
echo "$*" >> ` + log + `
case "$*" in
  "api user --jq .login") echo alice ;;
  "api user/orgs --jq .[].login") echo acme ;;
  "repo list --limit"*) echo '[{"name":"dash","nameWithOwner":"alice/dash","description":"A dashboard","isPrivate":true,"updatedAt":"2026-10-01T00:00:00Z","url":"https://github.com/alice/dash"},{"name":"bare","nameWithOwner":"alice/bare","description":null,"url":"https://github.com/alice/bare"}]' ;;
  "repo list acme --limit"*) echo '[{"name":"site","nameWithOwner":"acme/site","description":"","isArchived":true,"url":"https://github.com/acme/site"}]' ;;
  "repo view alice/new --json nameWithOwner,url")
    if [ -f ` + dir + `/created ]; then echo '{"nameWithOwner":"alice/new","url":"https://github.com/alice/new"}'; else echo "GraphQL: Could not resolve to a Repository" >&2; exit 1; fi ;;
  "repo create alice/new --private") touch ` + dir + `/created; echo https://github.com/alice/new ;;
  "repo view alice/dash --json nameWithOwner,url") echo '{"nameWithOwner":"alice/dash","url":"https://github.com/alice/dash"}' ;;
  *) echo "unexpected: $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

// A runner lists its owner's GitHub repositories (with their
// organisations') and creates new ones with its gh, only for its owner.
func TestRunnerGitHubReposForItsOwner(t *testing.T) {
	s, h, _ := runnerSide(t, 7)
	s.HubUser = 42
	gh, log := fakeGh(t)
	s.Git.GhBin = gh

	for _, uid := range []string{"", "7"} {
		if w := ownerCall(h, "GET", "/api/hub/github-repos", "", uid); w.Code != http.StatusForbidden {
			t.Errorf("listing for user %q: %d", uid, w.Code)
		}
		if w := ownerCall(h, "POST", "/api/hub/github-repos", `{"name":"alice/new"}`, uid); w.Code != http.StatusForbidden {
			t.Errorf("creating for user %q: %d", uid, w.Code)
		}
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("gh ran for someone else:\n%s", b)
	}

	w := ownerCall(h, "GET", "/api/hub/github-repos", "", "42")
	var list GitHubRepoList
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if list.User != "alice" || len(list.Repos) != 3 {
		t.Fatalf("list %+v", list)
	}
	if r := list.Repos[0]; r.NameWithOwner != "alice/dash" || r.Description != "A dashboard" || !r.IsPrivate || r.Owner != "alice" {
		t.Errorf("first %+v", r)
	}
	if r := list.Repos[1]; r.NameWithOwner != "alice/bare" || r.Description != "" {
		t.Errorf("no description %+v", r)
	}
	if r := list.Repos[2]; r.Owner != "acme" || !r.IsArchived {
		t.Errorf("org repo %+v", r)
	}
	// The browser gets the owner-qualified name to make the project from.
	if !strings.Contains(w.Body.String(), `"nameWithOwner":"alice/bare"`) {
		t.Errorf("body %s", w.Body)
	}

	w = ownerCall(h, "POST", "/api/hub/github-repos", `{"name":"alice/dash"}`, "42")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "already exists") {
		t.Errorf("creating an existing one: %d %s", w.Code, w.Body)
	}
	w = ownerCall(h, "POST", "/api/hub/github-repos", `{"name":"alice/new"}`, "42")
	var repo gitops.GitHubRepo
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &repo) != nil || repo.Name != "alice/new" || repo.URL != "https://github.com/alice/new" {
		t.Errorf("create: %d %s", w.Code, w.Body)
	}
	// Nothing was cloned: creating a repository doesn't touch the disk.
	if ents, _ := os.ReadDir(s.ProjectRoot); len(ents) != 0 {
		t.Errorf("projects folder: %v", ents)
	}

	s.Git.GhBin = filepath.Join(t.TempDir(), "no-gh")
	if w := ownerCall(h, "GET", "/api/hub/github-repos", "", "42"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "gh") {
		t.Errorf("without gh: %d %s", w.Code, w.Body)
	}
}

// The hub marks the repositories it already has a project for, whichever
// URL form the project was made with.
func TestMarkLinked(t *testing.T) {
	repos := []GitHubRepoItem{
		{GitHubListing: gitops.GitHubListing{NameWithOwner: "alice/dash", URL: "https://github.com/alice/dash"}},
		{GitHubListing: gitops.GitHubListing{NameWithOwner: "alice/site", URL: "https://github.com/alice/site"}},
		{GitHubListing: gitops.GitHubListing{NameWithOwner: "acme/api"}},
		{GitHubListing: gitops.GitHubListing{NameWithOwner: "alice/other", URL: "https://github.com/alice/other"}},
	}
	markLinked(repos, []model.Project{
		{Name: "Dash", RemoteURL: "git@github.com:alice/dash.git"},
		{Name: "Site", RemoteURL: "https://github.com/Alice/site.git"},
		{Name: "API", RemoteURL: "ssh://git@ssh.github.com:443/acme/api.git"},
		{Name: "no remote"},
	})
	for i, want := range []string{"Dash", "Site", "API", ""} {
		if repos[i].Linked != want {
			t.Errorf("%s: linked %q, want %q", repos[i].NameWithOwner, repos[i].Linked, want)
		}
	}
}
