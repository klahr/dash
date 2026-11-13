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
	for in, want := range map[string]string{"": ProtocolAuto, "auto": ProtocolAuto, " Auto ": ProtocolAuto, "ssh": ProtocolSSH, " HTTPS ": ProtocolHTTPS} {
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

// With ProtocolAuto a GitHub repository is cloned over SSH when SSH can
// read it, else over HTTPS when gh is logged in, else fails with both
// reasons; ssh, https, a clone URL and other hosts get no fallback.
func TestPickCloneRemote(t *testing.T) {
	const (
		remote = "https://github.com/klahr/dash-infra.git"
		sshURL = "git@github.com:klahr/dash-infra.git"
		https  = "https://github.com/klahr/dash-infra.git"
	)
	denied := func(u string) error {
		return &AccessError{Repo: "klahr/dash-infra", Protocol: RemoteProtocol(u), Detail: "Host key verification failed."}
	}
	for _, c := range []struct {
		name                    string
		remote, override, proto string
		sshOK, httpsOK, ghOK    bool
		want, wantErr           string
		wantChecked             []string
		wantGhAsked             bool
	}{
		{name: "ssh works", remote: remote, proto: ProtocolAuto, sshOK: true, ghOK: true, want: sshURL, wantChecked: []string{sshURL}},
		{name: "ssh fails, gh logged in", remote: remote, proto: ProtocolAuto, httpsOK: true, ghOK: true, want: https, wantChecked: []string{sshURL, https}, wantGhAsked: true},
		{name: "ssh fails, gh logged out", remote: remote, proto: ProtocolAuto, httpsOK: true,
			wantErr:     "runner laptop cannot access klahr/dash-infra over SSH or HTTPS: SSH: Host key verification failed.; HTTPS: gh isn't logged in (run `gh auth login`)",
			wantChecked: []string{sshURL}, wantGhAsked: true},
		{name: "both fail", remote: remote, proto: ProtocolAuto, ghOK: true,
			wantErr: "SSH: Host key verification failed.; HTTPS: Host key verification failed.", wantChecked: []string{sshURL, https}, wantGhAsked: true},
		{name: "ssh forced", remote: remote, proto: ProtocolSSH, httpsOK: true, ghOK: true, wantErr: "over ssh", wantChecked: []string{sshURL}},
		{name: "https forced", remote: remote, proto: ProtocolHTTPS, httpsOK: true, want: https, wantChecked: []string{https}},
		{name: "clone URL", remote: remote, override: "git@github-deploy:klahr/dash-infra.git", proto: ProtocolAuto, httpsOK: true, ghOK: true,
			wantErr: "over ssh", wantChecked: []string{"git@github-deploy:klahr/dash-infra.git"}},
		{name: "not GitHub", remote: "git@git.example.com:team/app.git", proto: ProtocolAuto, httpsOK: true, ghOK: true,
			wantErr: "over ssh", wantChecked: []string{"git@git.example.com:team/app.git"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var checked []string
			ghAsked := false
			check := func(u string) error {
				checked = append(checked, u)
				if p := RemoteProtocol(u); (p == ProtocolSSH && c.sshOK) || (p == ProtocolHTTPS && c.httpsOK) {
					return nil
				}
				return denied(u)
			}
			ghReady := func() error {
				ghAsked = true
				if c.ghOK {
					return nil
				}
				return errors.New("gh isn't logged in (run `gh auth login`)")
			}
			got, err := pickCloneRemote(c.remote, c.override, c.proto, "laptop", check, ghReady)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("err = %v, want it to contain %q", err, c.wantErr)
				}
			} else if err != nil || got != c.want {
				t.Errorf("got %q, %v; want %q", got, err, c.want)
			}
			if strings.Join(checked, " ") != strings.Join(c.wantChecked, " ") {
				t.Errorf("checked %q, want %q", checked, c.wantChecked)
			}
			if ghAsked != c.wantGhAsked {
				t.Errorf("asked gh: %v, want %v", ghAsked, c.wantGhAsked)
			}
		})
	}
	// A cancelled check is returned as it is, without trying HTTPS.
	_, err := pickCloneRemote(remote, "", ProtocolAuto, "laptop",
		func(string) error { return context.Canceled }, func() error { t.Error("gh asked"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

// gh becomes git's credential helper for github.com after the environment's
// own GIT_CONFIG_COUNT entries, and its path is quoted for sh.
func TestGhCredentialEnv(t *testing.T) {
	const helper = "credential.https://github.com.helper"
	env := ghCredentialEnv([]string{"HOME=/h"}, "/usr/bin/gh")
	want := []string{"HOME=/h", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=" + helper, "GIT_CONFIG_VALUE_0=!'/usr/bin/gh' auth git-credential"}
	if strings.Join(env, "\n") != strings.Join(want, "\n") {
		t.Errorf("env = %q, want %q", env, want)
	}
	env = ghCredentialEnv([]string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=a.b", "GIT_CONFIG_VALUE_0=1", "GIT_CONFIG_KEY_1=c.d", "GIT_CONFIG_VALUE_1=2", "PATH=/bin"}, "/opt/it's here/gh")
	want = []string{"GIT_CONFIG_KEY_0=a.b", "GIT_CONFIG_VALUE_0=1", "GIT_CONFIG_KEY_1=c.d", "GIT_CONFIG_VALUE_1=2", "PATH=/bin",
		"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_2=" + helper, `GIT_CONFIG_VALUE_2=!'/opt/it'\''s here/gh' auth git-credential`}
	if strings.Join(env, "\n") != strings.Join(want, "\n") {
		t.Errorf("env = %q, want %q", env, want)
	}
	// A later GIT_CONFIG_COUNT wins, as for git; a bad one counts as none.
	env = ghCredentialEnv([]string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_COUNT=junk"}, "/gh")
	if got := strings.Join(env, " "); !strings.HasPrefix(got, "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=") {
		t.Errorf("env = %q", env)
	}
}

// Git really asks gh for github.com credentials, after a helper the user
// configured, and the quoted path survives sh.
func TestGhCredentialHelperRuns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "with 'quote")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls")
	gh := filepath.Join(dir, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\necho \"gh $*\" >> \""+log+"\"\necho username=x-access-token\necho password=from-gh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "gitconfig")
	userHelper := "!f() { echo user-helper >> \"" + log + "\"; }; f"
	if err := os.WriteFile(cfg, []byte("[credential]\n\thelper = "+strconvQuote(userHelper)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	g := &Git{GhBin: gh}
	out, err := g.runIn(context.Background(), false, nil, []byte("protocol=https\nhost=github.com\n\n"), "", t.TempDir(), "git", "credential", "fill")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "password=from-gh") {
		t.Errorf("credential fill = %q", out)
	}
	calls, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(calls)); got != "user-helper\ngh auth git-credential get" {
		t.Errorf("helpers ran %q", got)
	}
}

// strconvQuote quotes s as a git config value.
func strconvQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func TestRedactRemote(t *testing.T) {
	for in, want := range map[string]string{
		"https://glpat-secret@gitlab.example.com/o/r.git": "https://gitlab.example.com/o/r.git",
		"https://user:secret@gitlab.example.com/o/r.git":  "https://gitlab.example.com/o/r.git",
		"http://token@example.com/o/r.git":                "http://example.com/o/r.git",
		"ssh://git:secret@example.com/o/r.git":            "ssh://example.com/o/r.git",
		"ssh://git@example.com/o/r.git":                   "ssh://git@example.com/o/r.git",
		"git@example.com:o/r.git":                         "git@example.com:o/r.git",
		"https://example.com/o/r.git":                     "https://example.com/o/r.git",
	} {
		if got := RedactRemote(in); got != want {
			t.Errorf("RedactRemote(%q) = %q, want %q", in, got, want)
		}
	}
}
