package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/peer"
)

// A runner without access to a hub's GitHub project says which runner,
// which repository and which protocol, instead of git's exit 128; with no
// -github-protocol it uses ssh, even when the hub's remote is https://.
func TestHubLinkReportsNoAccess(t *testing.T) {
	s, h, _ := runnerSide(t, 7)
	s.Peers = &peer.Hub{Self: "rg-klarre-runner"}
	ssh := filepath.Join(t.TempDir(), "ssh")
	log := filepath.Join(t.TempDir(), "ssh.log")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\necho 'git@github.com: Permission denied (publickey).' >&2\nexit 255\n"
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", ssh)
	b, _ := json.Marshal(dispatch.Link{Name: "dash-infra", RemoteURL: "https://github.com/klahr/dash-infra.git", DefaultBranch: "main", OrgID: 7})
	w := send(h, "PUT", "/api/hub/projects/hub-p1", string(b))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("link: %d %s", w.Code, w.Body)
	}
	if want := "runner rg-klarre-runner cannot access klahr/dash-infra over ssh: git@github.com: Permission denied (publickey)"; !strings.Contains(w.Body.String(), want) {
		t.Errorf("error %s, want %q", w.Body, want)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "BatchMode=yes") || !strings.Contains(string(calls), "git@github.com") || !strings.Contains(string(calls), "klahr/dash-infra.git") {
		t.Errorf("ssh was called with %q", calls)
	}
	if ents, _ := os.ReadDir(s.ProjectRoot); len(ents) != 0 {
		t.Errorf("the failed link left %v", ents)
	}
}

// The clone of an empty remote gets its first commit on the hub project's
// branch, pushed with an upstream, whatever init.defaultBranch says here;
// a project's clone URL is what the runner clones.
func TestHubLinkSeedsEmptyRemote(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[init]\n\tdefaultBranch = master\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	s, h, _ := runnerSide(t, 7)
	bare := filepath.Join(t.TempDir(), "dash-infra.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	b, _ := json.Marshal(dispatch.Link{Name: "dash-infra", RemoteURL: "https://github.com/klahr/dash-infra.git", CloneURL: bare, DefaultBranch: "main", OrgID: 7})
	w := send(h, "PUT", "/api/hub/projects/hub-p1", string(b))
	var linked dispatch.Linked
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &linked) != nil {
		t.Fatalf("link: %d %s", w.Code, w.Body)
	}
	if linked.DefaultBranch != "main" {
		t.Errorf("default branch %q", linked.DefaultBranch)
	}
	if got := gitIn(t, bare, "for-each-ref", "--format=%(refname)", "refs/heads"); got != "refs/heads/main" {
		t.Errorf("the remote's branches: %q, want main only", got)
	}
	if got := gitIn(t, linked.Path, "rev-parse", "--abbrev-ref", "main@{upstream}"); got != "origin/main" {
		t.Errorf("upstream %q", got)
	}
	if _, err := s.Git.Graph(context.Background(), linked.ProjectID, linked.Path, "main", 500); err != nil {
		t.Errorf("graph of main: %v", err)
	}
	// Linking again adopts the clone (its origin is the clone URL).
	if w := send(h, "PUT", "/api/hub/projects/hub-p1", string(b)); w.Code != http.StatusOK {
		t.Errorf("linking again: %d %s", w.Code, w.Body)
	}
}

// A project's clone URL holds no credentials.
func TestCheckCloneURL(t *testing.T) {
	for _, ok := range []string{"", "git@github-deploy:klahr/dash-infra.git", "https://git.example.com/team/app.git", "ssh://git@git.example.com/team/app.git"} {
		if _, err := checkCloneURL(ok); err != nil {
			t.Errorf("checkCloneURL(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"https://x-access-token:ghp_secret@github.com/klahr/dash-infra.git", "https://ghp_secret@github.com/klahr/dash-infra.git", "ext::sh -c id"} {
		if _, err := checkCloneURL(bad); err == nil {
			t.Errorf("checkCloneURL(%q) accepted it", bad)
		}
	}
}
