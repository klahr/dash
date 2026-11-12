package gitops

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGitHubProtocol(t *testing.T) {
	for in, want := range map[string]string{"": ProtocolSSH, "ssh": ProtocolSSH, " HTTPS ": ProtocolHTTPS} {
		if got, err := ParseGitHubProtocol(in); err != nil || got != want {
			t.Errorf("ParseGitHubProtocol(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseGitHubProtocol("git"); err == nil {
		t.Error("ParseGitHubProtocol(git) succeeded")
	}
}

// A runner clones a GitHub project over its own protocol, whatever form
// the hub's remote has; a project's clone URL is used as it is, and a
// remote that isn't GitHub's is left alone.
func TestCloneRemote(t *testing.T) {
	for _, c := range []struct{ remote, override, protocol, want string }{
		{"https://github.com/klahr/dash-infra.git", "", ProtocolSSH, "git@github.com:klahr/dash-infra.git"},
		{"https://github.com/klahr/dash-infra.git", "", "", "git@github.com:klahr/dash-infra.git"}, // no setting: ssh
		{"https://github.com/klahr/dash-infra", "", ProtocolHTTPS, "https://github.com/klahr/dash-infra.git"},
		{"git@github.com:klahr/dash-infra.git", "", ProtocolHTTPS, "https://github.com/klahr/dash-infra.git"},
		{"ssh://git@ssh.github.com:443/klahr/dash-infra.git", "", ProtocolSSH, "git@github.com:klahr/dash-infra.git"},
		{"https://github.com/klahr/dash-infra.git", "git@github-deploy:klahr/dash-infra.git", ProtocolSSH, "git@github-deploy:klahr/dash-infra.git"},
		{"https://github.com/klahr/dash-infra.git", " https://github.com/klahr/dash-infra.git ", ProtocolSSH, "https://github.com/klahr/dash-infra.git"},
		{"https://gitlab.com/klahr/dash-infra.git", "", ProtocolSSH, "https://gitlab.com/klahr/dash-infra.git"},
		{"git@git.example.com:team/app.git", "", ProtocolHTTPS, "git@git.example.com:team/app.git"},
	} {
		if got := CloneRemote(c.remote, c.override, c.protocol); got != c.want {
			t.Errorf("CloneRemote(%q, %q, %q) = %q, want %q", c.remote, c.override, c.protocol, got, c.want)
		}
	}
	if got := GitHubRemote("klahr/dash-infra", ProtocolSSH); got != "git@github.com:klahr/dash-infra.git" {
		t.Errorf("GitHubRemote ssh = %q", got)
	}
	if got := GitHubRemote("klahr/dash-infra", ProtocolHTTPS); got != "https://github.com/klahr/dash-infra.git" {
		t.Errorf("GitHubRemote https = %q", got)
	}
}

// A remote the runner can't read fails before the clone, naming the runner,
// the repository and the protocol, and ssh never prompts (BatchMode).
func TestCheckAccessFailsClearly(t *testing.T) {
	dir := t.TempDir()
	ssh := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\necho \"ssh $* prompt=$GIT_TERMINAL_PROMPT\" >&2\necho 'git@github.com: Permission denied (publickey).' >&2\nexit 255\n"
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", ssh)
	err := New(nil).CheckAccess(context.Background(), "p", "rg-klarre-runner", "git@github.com:klahr/dash-infra.git")
	var ae *AccessError
	if !errors.As(err, &ae) {
		t.Fatalf("CheckAccess = %v, want an AccessError", err)
	}
	msg := err.Error()
	for _, want := range []string{
		"runner rg-klarre-runner cannot access klahr/dash-infra over ssh: ",
		"Permission denied (publickey)",
		"-o BatchMode=yes", // the user's GIT_SSH_COMMAND, in batch mode
		"-o StrictHostKeyChecking=accept-new",
		"prompt=0", // GIT_TERMINAL_PROMPT=0
		"`ssh -T git@github.com`",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "Please make sure") {
		t.Errorf("error %q keeps ssh's boilerplate", msg)
	}
	if strings.Contains(msg, "ssh-keygen -R") {
		t.Errorf("error %q gives the host key hint for a key GitHub refused", msg)
	}

	https := (&AccessError{Runner: "r1", Repo: "klahr/dash-infra", Protocol: ProtocolHTTPS,
		Detail: "fatal: could not read Username for 'https://github.com': terminal prompts disabled"}).Error()
	if !strings.HasPrefix(https, "runner r1 cannot access klahr/dash-infra over https: fatal: could not read Username") {
		t.Errorf("https error = %q", https)
	}
}

// ssh never prompts and trusts a host it has never seen, after the user's
// own GIT_SSH_COMMAND when they set one.
func TestBatchSSHEnv(t *testing.T) {
	const opts = " -o BatchMode=yes -o StrictHostKeyChecking=accept-new"
	for user, want := range map[string]string{
		"":                       "ssh" + opts,
		" ssh -i ~/.ssh/deploy ": "ssh -i ~/.ssh/deploy" + opts,
	} {
		t.Setenv("GIT_SSH_COMMAND", user)
		if got := batchSSHEnv(); len(got) != 1 || got[0] != "GIT_SSH_COMMAND="+want {
			t.Errorf("GIT_SSH_COMMAND=%q: batchSSHEnv() = %q, want %q", user, got, "GIT_SSH_COMMAND="+want)
		}
	}
}

// A github.com host key that doesn't match (an old one in known_hosts)
// gets its own hint; other ssh failures keep the SSH key hint.
func TestAccessErrorSSHHint(t *testing.T) {
	for _, c := range []struct {
		detail, want, notWant string
	}{
		{"@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@; Host key verification failed.; fatal: Could not read from remote repository.",
			"`ssh-keygen -R github.com`, then run `ssh -T git@github.com`", "check this computer's SSH key"},
		{"git@github.com: Permission denied (publickey).; fatal: Could not read from remote repository.",
			"check this computer's SSH key with `ssh -T git@github.com`", "ssh-keygen"},
	} {
		msg := (&AccessError{Runner: "r1", Repo: "klahr/dash-infra", Protocol: ProtocolSSH, Detail: c.detail}).Error()
		if !strings.Contains(msg, c.want) || !strings.Contains(msg, "-github-protocol=https") || strings.Contains(msg, c.notWant) {
			t.Errorf("error %q: want %q and -github-protocol=https, not %q", msg, c.want, c.notWant)
		}
	}
}

// localRemotes lets the test clone local paths.
func localRemotes(t *testing.T) {
	AllowLocalRemotes = true
	t.Cleanup(func() { AllowLocalRemotes = false })
}

// An empty repository (just created on GitHub) has no refs: ls-remote
// succeeds with no output, and that is no failure.
func TestCheckAccessEmptyRepo(t *testing.T) {
	localRemotes(t)
	bare := filepath.Join(t.TempDir(), "empty.git")
	if out, err := exec.Command("git", "-C", filepath.Dir(bare), "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if err := New(nil).CheckAccess(context.Background(), "p", "r1", bare); err != nil {
		t.Fatalf("CheckAccess on an empty repository: %v", err)
	}
	if err := New(nil).CheckAccess(context.Background(), "p", "r1", filepath.Join(t.TempDir(), "missing.git")); err == nil {
		t.Fatal("CheckAccess on a missing repository succeeded")
	}
}

// Seeding the clone of an empty repository makes the project's branch,
// not init.defaultBranch's (master, or git's default when unset), and
// pushes it with an upstream.
func TestSeedEmptyCloneMakesProjectBranch(t *testing.T) {
	for name, defaultBranch := range map[string]string{"unset": "", "master": "master"} {
		t.Run(name, func(t *testing.T) {
			localRemotes(t)
			cfg := filepath.Join(t.TempDir(), "gitconfig")
			body := ""
			if defaultBranch != "" {
				body = "[init]\n\tdefaultBranch = " + defaultBranch + "\n"
			}
			if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_CONFIG_GLOBAL", cfg)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

			ctx, g := context.Background(), New(nil)
			bare := filepath.Join(t.TempDir(), "empty.git")
			if out, err := exec.Command("git", "-C", filepath.Dir(bare), "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			clone := filepath.Join(t.TempDir(), "clone")
			if err := g.Clone(ctx, "p", bare, clone); err != nil {
				t.Fatal(err)
			}
			seeded, err := g.SeedRepo(ctx, "p", clone, "main")
			if err != nil || !seeded {
				t.Fatalf("SeedRepo = %v, %v", seeded, err)
			}
			if b := g.CheckedOut(ctx, "p", clone); b != "main" {
				t.Fatalf("checked out %q, want main", b)
			}
			if _, err := g.Graph(ctx, "p", clone, "main", 500); err != nil {
				t.Fatalf("Graph(main): %v", err)
			}
			if err := g.PushUpstream(ctx, "p", clone, "main"); err != nil {
				t.Fatal(err)
			}
			if out, err := g.git(ctx, "p", bare, "for-each-ref", "--format=%(refname)", "refs/heads"); err != nil || out != "refs/heads/main" {
				t.Fatalf("remote's branches = %q, %v; want only main", out, err)
			}
			if up, err := g.git(ctx, "p", clone, "rev-parse", "--abbrev-ref", "main@{upstream}"); err != nil || up != "origin/main" {
				t.Fatalf("upstream = %q, %v", up, err)
			}
			if seeded, err := g.SeedRepo(ctx, "p", clone, "main"); err != nil || seeded {
				t.Fatalf("seeding again = %v, %v", seeded, err)
			}
		})
	}
}
