// Package gitops wraps the host's git and gh binaries. Every command is
// broadcast as a git.op event so the dashboard can show live git activity.
package gitops

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
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
	if err != nil {
		return false, err
	}
	// A merge in progress must be concluded even when the resolution matches HEAD.
	_, mergeErr := g.git(ctx, projectID, dir, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	if status == "" && mergeErr != nil {
		return false, nil
	}
	_, err = g.git(ctx, projectID, dir, g.committing(ctx, dir, "commit", "-m", msg)...)
	return err == nil, err
}

// Diff returns the patch a branch introduces relative to its merge base with base.
func (g *Git) Diff(ctx context.Context, projectID, repo, base, branch string) (string, error) {
	return g.git(ctx, projectID, repo, "diff", "--stat", "--patch", base+"..."+branch)
}

// ErrConflict is returned when merging hits conflicts.
type ErrConflict struct{ Files []string }

func (e *ErrConflict) Error() string {
	return "merge conflict in " + strings.Join(e.Files, ", ")
}

// MergeIn merges branch into the branch checked out in dir and commits.
// On conflict the merge is aborted (dir is left as it was) and an
// *ErrConflict lists the conflicting files. changed reports whether HEAD moved.
func (g *Git) MergeIn(ctx context.Context, projectID, dir, branch string) (changed bool, err error) {
	before, _ := g.Head(ctx, projectID, dir, "HEAD")
	if _, err := g.git(ctx, projectID, dir, g.committing(ctx, dir, "merge", "--no-edit", branch)...); err != nil {
		files := g.conflictedFiles(ctx, projectID, dir)
		_, _ = g.git(ctx, projectID, dir, "merge", "--abort")
		if len(files) > 0 {
			return false, &ErrConflict{Files: files}
		}
		return false, err
	}
	after, _ := g.Head(ctx, projectID, dir, "HEAD")
	return before != after, nil
}

// StartMerge begins merging branch into dir without committing. Conflicts
// are left in the working tree (with markers) for someone to resolve; the
// next CommitAll completes the merge. Returns the conflicting files.
func (g *Git) StartMerge(ctx context.Context, projectID, dir, branch string) ([]string, error) {
	_, err := g.git(ctx, projectID, dir, g.committing(ctx, dir, "merge", "--no-commit", "--no-ff", branch)...)
	files := g.conflictedFiles(ctx, projectID, dir)
	if err != nil && len(files) == 0 {
		// "Already up to date" exits 0; anything else without conflicts is a real error.
		_, _ = g.git(ctx, projectID, dir, "merge", "--abort")
		return nil, err
	}
	return files, nil
}

func (g *Git) conflictedFiles(ctx context.Context, projectID, dir string) []string {
	out, err := g.git(ctx, projectID, dir, "diff", "--name-only", "--diff-filter=U")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// LeftoverMarkers stages everything and reports files that still contain
// conflict markers (empty when the merge is fully resolved).
func (g *Git) LeftoverMarkers(ctx context.Context, projectID, dir string) (string, error) {
	if _, err := g.git(ctx, projectID, dir, "add", "-A"); err != nil {
		return "", err
	}
	out, err := g.git(ctx, projectID, dir, "diff", "--cached", "--check")
	if err == nil {
		return "", nil
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "conflict marker") {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n"), nil
}

// SquashInto lands branch on the branch checked out in repo as one commit.
// Returns false if branch had nothing new. On failure repo is reset.
func (g *Git) SquashInto(ctx context.Context, projectID, repo, branch, msg string) (bool, error) {
	if _, err := g.git(ctx, projectID, repo, "merge", "--squash", branch); err != nil {
		_, _ = g.git(ctx, projectID, repo, "reset", "--merge")
		return false, err
	}
	if _, err := g.git(ctx, projectID, repo, "diff", "--cached", "--quiet"); err == nil {
		return false, nil // nothing staged
	}
	if _, err := g.git(ctx, projectID, repo, g.committing(ctx, repo, "commit", "-m", msg)...); err != nil {
		_, _ = g.git(ctx, projectID, repo, "reset", "--merge")
		return false, err
	}
	return true, nil
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

// DiffStat sums the change between base and HEAD in dir (files, lines added, removed).
func (g *Git) DiffStat(ctx context.Context, projectID, dir, base string) (files, add, del int, err error) {
	out, err := g.git(ctx, projectID, dir, "diff", "--numstat", base+"...HEAD")
	if err != nil || out == "" {
		return 0, 0, 0, err
	}
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(l, "\t", 3)
		if len(f) < 3 {
			continue
		}
		files++
		a, _ := strconv.Atoi(f[0]) // "-" for binary files counts as 0
		d, _ := strconv.Atoi(f[1])
		add, del = add+a, del+d
	}
	return files, add, del, nil
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
