package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/klahr/dash/internal/testbin"
)

func TestSameRemote(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"https://github.com/klahr/dash.git", "git@github.com:klahr/dash.git", true},
		{"https://github.com/klahr/dash", "ssh://git@ssh.github.com:443/Klahr/Dash.git", true},
		{"https://token@github.com/klahr/dash.git", "https://github.com/klahr/dash/", true},
		{"https://github.com/klahr/dash.git", "https://github.com/klahr/dash-2.git", false},
		{"https://github.com/klahr/dash.git", "https://github.com/other/dash.git", false},
		{"https://github.com/klahr/dash.git", "https://gitlab.com/klahr/dash.git", false},
		{"/srv/git/dash.git", "/srv/git/dash", true},
		{"/srv/git/dash.git", "/srv/git/other.git", false},
		{"/srv/git/dash.git", "https://github.com/srv/dash.git", false},
		{"", "", false},
	} {
		if got := SameRemote(c.a, c.b); got != c.want {
			t.Errorf("SameRemote(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestRemoteRepo(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"https://github.com/klahr/dash.git": "dash",
		"git@github.com:klahr/Dash.git":     "Dash",
		"https://github.com/klahr/dash/":    "dash",
		"/srv/git/remote":                   "remote",
		"..":                                "",
	} {
		if got := RemoteRepo(in); got != want {
			t.Errorf("RemoteRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

// gh's JSON comes with null or empty descriptions, and sometimes a warning
// before it.
func TestParseGitHubRepos(t *testing.T) {
	t.Parallel()

	out := `warning: something gh says first
[{"name":"dash","nameWithOwner":"klahr/dash","description":"Dashboard for Claude","isPrivate":false,"isFork":false,"isArchived":false,"updatedAt":"2026-11-01T10:00:00Z","url":"https://github.com/klahr/dash"},
 {"name":"old","nameWithOwner":"acme/old","description":null,"isPrivate":true,"isFork":true,"isArchived":true,"updatedAt":"2020-01-01T00:00:00Z","url":"https://github.com/acme/old"},
 {"name":"blank","nameWithOwner":"klahr/blank","description":"  ","isPrivate":true,"updatedAt":"2026-01-01T00:00:00Z","url":"https://github.com/klahr/blank"},
 {"name":"broken","nameWithOwner":""}]`
	repos, err := ParseGitHubRepos(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 3 {
		t.Fatalf("got %d repos: %+v", len(repos), repos)
	}
	if r := repos[0]; r.Owner != "klahr" || r.Description != "Dashboard for Claude" || r.IsPrivate || r.UpdatedAt.Year() != 2026 {
		t.Errorf("first: %+v", r)
	}
	if r := repos[1]; r.Owner != "acme" || r.Description != "" || !r.IsPrivate || !r.IsFork || !r.IsArchived {
		t.Errorf("null description: %+v", r)
	}
	if r := repos[2]; r.Description != "" {
		t.Errorf("blank description: %q", r.Description)
	}
	if repos, err := ParseGitHubRepos(""); err != nil || len(repos) != 0 || repos == nil {
		t.Errorf("no output: %v %v", repos, err)
	}
	if _, err := ParseGitHubRepos("not json"); err == nil {
		t.Error("garbage parsed")
	}
}

// GitHubRepos asks gh once per owner: the user's repositories, then each
// organisation's.
func TestGitHubReposOneCallPerOwner(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	gh := filepath.Join(dir, "gh")
	script := `#!/bin/sh
echo "$*" >> ` + log + `
case "$*" in
  "api user --jq .login") echo klahr ;;
  "api user/orgs --jq .[].login") printf 'acme\nbad;org\n' ;;
  "repo list --limit"*) echo '[{"name":"a","nameWithOwner":"klahr/a","updatedAt":"2026-01-01T00:00:00Z"},{"name":"b","nameWithOwner":"klahr/b","updatedAt":"2026-06-01T00:00:00Z"}]' ;;
  "repo list acme --limit"*) echo '[{"name":"c","nameWithOwner":"acme/c","description":null}]' ;;
  *) exit 1 ;;
esac
`
	testbin.Write(t, gh, script)
	g := New(nil)
	g.GhBin = gh
	user, repos, err := g.GitHubRepos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range repos {
		names = append(names, r.NameWithOwner)
	}
	if user != "klahr" || !slices.Equal(names, []string{"klahr/b", "klahr/a", "acme/c"}) {
		t.Errorf("user %q repos %v", user, names)
	}
	calls, _ := os.ReadFile(log)
	if n := len(slices.DeleteFunc(splitLines(string(calls)), func(s string) bool { return s == "" })); n != 4 {
		t.Errorf("%d gh calls:\n%s", n, calls)
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func TestCheckoutChanges(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	ctx := context.Background()
	g := New(nil)
	dir := t.TempDir()
	if err := g.InitRepo(ctx, "p", dir, "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := g.git(ctx, "p", dir, g.committing(ctx, dir, "commit", "-q", "--allow-empty", "-m", "x")...); err != nil {
		t.Fatal(err)
	}
	if _, err := g.git(ctx, "p", dir, "add", "a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.git(ctx, "p", dir, g.committing(ctx, dir, "commit", "-q", "-m", "a")...); err != nil {
		t.Fatal(err)
	}
	if files, err := g.CheckoutChanges(ctx, "p", dir); err != nil || len(files) != 0 {
		t.Fatalf("clean: %v %v", files, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if files, _ := g.CheckoutChanges(ctx, "p", dir); len(files) != 0 {
		t.Errorf("untracked counted: %v", files)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if files, _ := g.CheckoutChanges(ctx, "p", dir); !slices.Equal(files, []string{"a.txt"}) {
		t.Errorf("modified: %v", files)
	}
	if b := g.CheckedOut(ctx, "p", dir); b != "main" {
		t.Errorf("checked out %q", b)
	}
}
