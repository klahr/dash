package gitops

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestGist(t *testing.T) {
	cmd := func(out string) error {
		return &CmdError{Args: []string{"git", "fetch", "origin"}, Output: out, Err: errors.New("exit status 128")}
	}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"no access", cmd("ssh: Could not resolve hostname example.invalid: nodename nor servname provided, or not known\n" +
			"fatal: Could not read from remote repository.\n\n" +
			"Please make sure you have the correct access rights\nand the repository exists.\n"),
			"fatal: Could not read from remote repository."},
		{"not a repository", cmd("fatal: '/nonexistent/repo' does not appear to be a git repository\n" +
			"fatal: Could not read from remote repository.\n\n" +
			"Please make sure you have the correct access rights\nand the repository exists.\n"),
			"fatal: '/nonexistent/repo' does not appear to be a git repository"},
		{"pathspec", cmd("error: pathspec 'nope' did not match any file(s) known to git\n"),
			"error: pathspec 'nope' did not match any file(s) known to git"},
		{"push rejected", cmd("To /tmp/r\n ! [rejected]        HEAD -> main (fetch first)\n" +
			"error: failed to push some refs to '/tmp/r'\n" +
			"hint: Updates were rejected because the remote contains work that you do not\n"),
			"! [rejected]        HEAD -> main (fetch first)"},
		{"unmarked output", cmd("HTTP 404: Not Found (https://api.github.com/repos/o/r)\nsomething else\n"),
			"something else"},
		{"no output", cmd("  \n"), "git fetch origin: exit status 128:"},
		{"wrapped", fmt.Errorf("can't fetch: %w", cmd("fatal: unable to access 'https://example.com/r.git/': Could not resolve host: example.com\n")),
			"fatal: unable to access 'https://example.com/r.git/': Could not resolve host: example.com"},
		{"rate limit", fmt.Errorf("open pull request: %w", &RateLimitError{Kind: "secondary", Message: "You have exceeded a secondary rate limit.",
			Until: time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC), Err: cmd("gh: You have exceeded a secondary rate limit. (HTTP 403)")}),
			"GitHub rate limit (secondary), paused until 2026-01-02 03:04 UTC: You have exceeded a secondary rate limit."},
		{"plain error", errors.New("context deadline exceeded\nmore"), "context deadline exceeded"},
		{"plain error with marker", errors.New("sync failed\nerror: cannot lock ref 'refs/heads/main'"), "error: cannot lock ref 'refs/heads/main'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Gist(tt.err); got != tt.want {
				t.Errorf("Gist() = %q, want %q", got, tt.want)
			}
		})
	}
}
