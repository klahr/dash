// Package gitops wraps the host's git and gh binaries. Every command is
// broadcast as a git.op event so the dashboard can show live git activity.
package gitops

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
)

type Publisher interface{ Publish(model.Event) }

type Git struct {
	pub Publisher
	// SignCommits keeps the user's commit.gpgsign setting for commits Dash
	// makes. Off by default: a headless server cannot answer a pinentry
	// prompt, so signing would hang until it times out.
	SignCommits bool
}

func New(pub Publisher) *Git { return &Git{pub: pub} }

// CmdError carries combined output of a failed command.
type CmdError struct {
	Args   []string
	Output string
	Err    error
}

func (e *CmdError) Error() string {
	return fmt.Sprintf("%s: %v: %s", strings.Join(e.Args, " "), e.Err, strings.TrimSpace(e.Output))
}
func (e *CmdError) Unwrap() error { return e.Err }

func (g *Git) exec(ctx context.Context, projectID, dir, bin string, args ...string) (string, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if g.pub != nil {
		g.pub.Publish(model.Event{Type: model.EvGitOp, ProjectID: projectID, Time: time.Now(), Data: map[string]any{
			"cmd": bin + " " + strings.Join(args, " "), "dir": dir, "ok": err == nil,
			"ms": time.Since(start).Milliseconds(),
		}})
	}
	if err != nil {
		return out.String(), &CmdError{Args: append([]string{bin}, args...), Output: out.String(), Err: err}
	}
	return strings.TrimSpace(out.String()), nil
}

func (g *Git) git(ctx context.Context, projectID, dir string, args ...string) (string, error) {
	return g.exec(ctx, projectID, dir, "git", args...)
}

// InitRepo makes sure path is a repository with at least one commit
// (worktrees need a commit to branch from). Existing history is untouched.
func (g *Git) InitRepo(ctx context.Context, projectID, path, branch string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	if _, err := g.git(ctx, projectID, path, "rev-parse", "--git-dir"); err != nil {
		if _, err := g.git(ctx, projectID, path, "init", "-b", branch); err != nil {
			return err
		}
	}
	if _, err := g.git(ctx, projectID, path, "rev-parse", "--verify", "HEAD"); err == nil {
		return nil
	}
	_, err := g.git(ctx, projectID, path, g.committing(ctx, path, "commit", "--allow-empty", "-m", "Initial commit (Dash)")...)
	return err
}

// committing prefixes args for commands that create commits: it disables
// signing (unless SignCommits) and supplies a fallback identity when none is
// configured, so Dash works unattended and on a fresh machine.
func (g *Git) committing(ctx context.Context, dir string, args ...string) []string {
	var pre []string
	if !g.SignCommits {
		pre = append(pre, "-c", "commit.gpgsign=false")
	}
	if out, err := g.git(ctx, "", dir, "config", "user.email"); err != nil || out == "" {
		pre = append(pre, "-c", "user.name=Dash", "-c", "user.email=dash@localhost")
	}
	return append(pre, args...)
}

func (g *Git) Head(ctx context.Context, projectID, dir, ref string) (string, error) {
	return g.git(ctx, projectID, dir, "rev-parse", ref)
}

// AddWorktree creates (or reuses, if it already exists) a worktree on a new branch.
func (g *Git) AddWorktree(ctx context.Context, projectID, repo string, wt model.Worktree) error {
	if _, err := os.Stat(wt.Path); err == nil {
		return nil
	}
	_, err := g.git(ctx, projectID, repo, "worktree", "add", "-b", wt.Branch, wt.Path, wt.BaseCommit)
	return err
}

// RemoveWorktree deletes the worktree directory and its branch.
func (g *Git) RemoveWorktree(ctx context.Context, projectID, repo string, wt model.Worktree) error {
	if _, err := g.git(ctx, projectID, repo, "worktree", "remove", "--force", wt.Path); err != nil {
		// Directory may already be gone; prune stale metadata and carry on.
		_, _ = g.git(ctx, projectID, repo, "worktree", "prune")
	}
	_, err := g.git(ctx, projectID, repo, "branch", "-D", wt.Branch)
	return err
}

// CommitAll stages everything in dir and commits it. Returns false if there
// was nothing to commit.
func (g *Git) CommitAll(ctx context.Context, projectID, dir, msg string) (bool, error) {
	if _, err := g.git(ctx, projectID, dir, "add", "-A"); err != nil {
		return false, err
	}
	status, err := g.git(ctx, projectID, dir, "status", "--porcelain")
	if err != nil || status == "" {
		return false, err
	}
	_, err = g.git(ctx, projectID, dir, g.committing(ctx, dir, "commit", "-m", msg)...)
	return err == nil, err
}

// Diff returns the patch a branch introduces relative to its merge base with base.
func (g *Git) Diff(ctx context.Context, projectID, repo, base, branch string) (string, error) {
	return g.git(ctx, projectID, repo, "diff", "--stat", "--patch", base+"..."+branch)
}

// Rebase replays the worktree's branch onto onto. On conflict the rebase is
// aborted and the error returned, leaving the worktree as it was.
func (g *Git) Rebase(ctx context.Context, projectID, worktree, onto string) error {
	if _, err := g.git(ctx, projectID, worktree, g.committing(ctx, worktree, "rebase", onto)...); err != nil {
		_, _ = g.git(ctx, projectID, worktree, "rebase", "--abort")
		return err
	}
	return nil
}

// FastForward advances the checked-out branch in repo to branch.
func (g *Git) FastForward(ctx context.Context, projectID, repo, branch string) error {
	_, err := g.git(ctx, projectID, repo, "merge", "--ff-only", branch)
	return err
}

func (g *Git) Push(ctx context.Context, projectID, repo, branch string) error {
	_, err := g.git(ctx, projectID, repo, "push", "origin", branch)
	return err
}

// CreateGitHubRepo uses gh to create a remote, set it as origin and push.
func (g *Git) CreateGitHubRepo(ctx context.Context, projectID, repo, name string, private bool) (string, error) {
	vis := "--public"
	if private {
		vis = "--private"
	}
	if _, err := g.exec(ctx, projectID, repo, "gh", "repo", "create", name, vis, "--source=.", "--remote=origin", "--push"); err != nil {
		return "", err
	}
	return g.git(ctx, projectID, repo, "remote", "get-url", "origin")
}

// CurrentBranch returns the branch checked out in dir.
func (g *Git) CurrentBranch(ctx context.Context, projectID, dir string) (string, error) {
	return g.git(ctx, projectID, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// RemoteURL returns origin's URL, or "" if there is no origin.
func (g *Git) RemoteURL(ctx context.Context, projectID, dir string) string {
	out, err := g.git(ctx, projectID, dir, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return out
}
