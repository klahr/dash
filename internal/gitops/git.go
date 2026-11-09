// Package gitops wraps the host's git and gh binaries. Every command is
// broadcast as a git.op event so the dashboard can show live git activity.
package gitops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
)

type Publisher interface{ Publish(model.Event) }

type Git struct {
	pub Publisher
	// SignCommits keeps the user's commit.gpgsign setting for commits Dash
	// makes. Off by default: a headless server cannot answer a pinentry
	// prompt, so signing would hang until it times out.
	SignCommits bool
	GhBin       string // GitHub CLI; default "gh"
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
	return g.run(ctx, true, projectID, dir, bin, args...)
}

// run executes a command; publish=false keeps routine polling out of the activity feed.
func (g *Git) run(ctx context.Context, publish bool, projectID, dir, bin string, args ...string) (string, error) {
	start := time.Now()
	args = stripNUL(args)
	if err := localrun.Check(bin + " " + strings.Join(args, " ")); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1")
	// When ctx ends, git is killed but a child it started (ssh) may keep the
	// output pipe open; don't wait for it forever.
	cmd.WaitDelay = 5 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if g.pub != nil && publish {
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

// stripNUL drops NUL bytes from args: no program can receive one (exec fails
// with "invalid argument"), and a stray one pasted into a task's prompt would
// otherwise make every commit, tag or PR carrying that prompt fail.
func stripNUL(args []string) []string {
	var out []string
	for i, a := range args {
		if strings.IndexByte(a, 0) < 0 {
			continue
		}
		if out == nil {
			out = append([]string(nil), args...)
		}
		out[i] = strings.ReplaceAll(a, "\x00", "")
	}
	if out == nil {
		return args
	}
	return out
}

func (g *Git) gh(ctx context.Context, projectID, dir string, args ...string) (string, error) {
	return g.run(ctx, true, projectID, dir, g.ghBin(), args...)
}

// ghQuiet runs gh without an activity event (status polling).
func (g *Git) ghQuiet(ctx context.Context, projectID, dir string, args ...string) (string, error) {
	return g.run(ctx, false, projectID, dir, g.ghBin(), args...)
}

// HasGh reports whether the GitHub CLI is installed here: without it Dash
// can't open (or merge) pull requests.
func (g *Git) HasGh() bool {
	_, err := exec.LookPath(g.ghBin())
	return err == nil
}

func (g *Git) ghBin() string {
	if g.GhBin == "" {
		return "gh"
	}
	return g.GhBin
}

// worktreeLocks serializes `git worktree` commands per repository: git reads
// every .git/worktrees entry while adding one and fails ("failed to read
// .../commondir") on an entry another add or remove has half written.
var worktreeLocks sync.Map // repo dir -> *sync.Mutex

func (g *Git) git(ctx context.Context, projectID, dir string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "worktree" {
		key := dir
		if abs, err := filepath.Abs(dir); err == nil {
			key = abs
		}
		mu, _ := worktreeLocks.LoadOrStore(key, &sync.Mutex{})
		mu.(*sync.Mutex).Lock()
		defer mu.(*sync.Mutex).Unlock()
	}
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

// Show returns the patch a single commit introduced.
func (g *Git) Show(ctx context.Context, projectID, repo, sha string) (string, error) {
	return g.git(ctx, projectID, repo, "show", "--stat", "--patch", "--format=", sha)
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

// SquashInto lands branch on the branch checked out in repo as one commit
// and returns its hash ("" if branch had nothing new). On failure repo is
// reset; a conflict returns an *ErrConflict.
func (g *Git) SquashInto(ctx context.Context, projectID, repo, branch, msg string) (string, error) {
	if _, err := g.git(ctx, projectID, repo, "merge", "--squash", branch); err != nil {
		files := g.conflictedFiles(ctx, projectID, repo)
		_, _ = g.git(ctx, projectID, repo, "reset", "--merge")
		if len(files) > 0 {
			return "", &ErrConflict{Files: files}
		}
		return "", err
	}
	if _, err := g.git(ctx, projectID, repo, "diff", "--cached", "--quiet"); err == nil {
		return "", nil // nothing staged
	}
	if _, err := g.git(ctx, projectID, repo, g.committing(ctx, repo, "commit", "-m", msg)...); err != nil {
		_, _ = g.git(ctx, projectID, repo, "reset", "--merge")
		return "", err
	}
	return g.Head(ctx, projectID, repo, "HEAD")
}

func (g *Git) Push(ctx context.Context, projectID, repo, branch string) error {
	_, err := g.git(ctx, projectID, repo, "push", "origin", branch)
	return err
}

// PushHead pushes what is checked out in dir (e.g. a detached landing
// checkout) to origin's branch, only as a fast-forward: when origin moved on
// meanwhile, the push is rejected and nothing changes there.
func (g *Git) PushHead(ctx context.Context, projectID, dir, branch string) error {
	_, err := g.git(ctx, projectID, dir, "push", "origin", "HEAD:refs/heads/"+branch)
	return err
}

// PushBranch publishes a work branch, overwriting the remote copy only if
// it is the one Dash pushed last (--force-with-lease).
func (g *Git) PushBranch(ctx context.Context, projectID, dir, branch string) error {
	_, err := g.git(ctx, projectID, dir, "push", "--force-with-lease", "origin", branch+":"+branch)
	return err
}

// DeleteRemoteBranch removes branch from origin; a branch that is already
// gone (e.g. GitHub deleted it after merging) is not an error.
func (g *Git) DeleteRemoteBranch(ctx context.Context, projectID, repo, branch string) {
	_, _ = g.git(ctx, projectID, repo, "push", "origin", "--delete", branch)
}

// ErrDiverged means the local default branch has commits origin doesn't
// have and they can't be replayed on top of origin's without conflicts.
type ErrDiverged struct{ Branch, Output string }

func (e *ErrDiverged) Error() string {
	return fmt.Sprintf("%s has diverged from origin/%s and can't be rebased automatically: %s", e.Branch, e.Branch, e.Output)
}

// SyncWithRemote brings branch (checked out in repo) up to date with
// origin: a fast-forward when possible, otherwise local commits that were
// never pushed are rebased onto origin's. On conflict nothing changes and
// an *ErrDiverged is returned.
func (g *Git) SyncWithRemote(ctx context.Context, projectID, repo, branch string) error {
	if _, err := g.git(ctx, projectID, repo, "fetch", "origin", branch); err != nil {
		return err
	}
	if _, err := g.git(ctx, projectID, repo, "merge", "--ff-only", "origin/"+branch); err == nil {
		return nil
	}
	if out, err := g.git(ctx, projectID, repo, g.committing(ctx, repo, "rebase", "origin/"+branch)...); err != nil {
		_, _ = g.git(ctx, projectID, repo, "rebase", "--abort")
		return &ErrDiverged{Branch: branch, Output: lastLine(out)}
	}
	return nil
}

// FetchBranch updates origin/<branch> and origin's tags (so releases tagged
// elsewhere show up) and nothing else: no local branch or checkout moves, and
// a local tag that differs from origin's stays as it is. publish=false keeps
// background fetches out of the activity feed. Callers bound it with a
// context timeout.
func (g *Git) FetchBranch(ctx context.Context, projectID, repo, branch string, publish bool) error {
	// Not --quiet: that would hide which refs a failed fetch rejected.
	out, err := g.run(ctx, publish, projectID, repo, "git", "fetch", "--no-tags", "origin",
		"+refs/heads/"+branch+":refs/remotes/origin/"+branch, tagRefspec)
	if err != nil && onlyClobbered(out) {
		return nil
	}
	return err
}

// AheadBehind compares branch with origin/<branch> as of the last fetch:
// ahead counts local commits origin lacks, behind origin's commits local lacks.
func (g *Git) AheadBehind(ctx context.Context, projectID, repo, branch string) (ahead, behind int, err error) {
	out, err := g.run(ctx, false, projectID, repo, "git", "rev-list", "--left-right", "--count",
		"refs/heads/"+branch+"...refs/remotes/origin/"+branch, "--")
	if err != nil {
		return 0, 0, err
	}
	return ParseAheadBehind(out)
}

// ParseAheadBehind reads `git rev-list --left-right --count A...B` output.
func ParseAheadBehind(out string) (ahead, behind int, err error) {
	f := strings.Fields(out)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", out)
	}
	if ahead, err = strconv.Atoi(f[0]); err != nil {
		return 0, 0, err
	}
	if behind, err = strconv.Atoi(f[1]); err != nil {
		return 0, 0, err
	}
	return ahead, behind, nil
}

// FastForwardToOrigin moves branch to origin/<branch> (as of the last fetch)
// when it is strictly behind it. A branch with commits origin doesn't have
// is left alone: reconciling that is the merge step's job. moved is how many
// commits branch gained. When repo has branch checked out its files follow
// (see Advance), and uncommitted changes in the way stop it.
func (g *Git) FastForwardToOrigin(ctx context.Context, projectID, repo, branch string) (moved, ahead int, err error) {
	ahead, behind, err := g.AheadBehind(ctx, projectID, repo, branch)
	if err != nil || ahead > 0 || behind == 0 {
		return 0, ahead, err
	}
	old, err := g.run(ctx, false, projectID, repo, "git", "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return 0, 0, err
	}
	target, err := g.run(ctx, false, projectID, repo, "git", "rev-parse", "--verify", "refs/remotes/origin/"+branch)
	if err != nil {
		return 0, 0, err
	}
	if err := g.Advance(ctx, projectID, repo, branch, old, target); err != nil {
		return 0, 0, err
	}
	return behind, 0, nil
}

// FindTodoCommit finds the squash commit that landed a todo on branch, by
// its "Dash-Todo: <id>" trailer.
func (g *Git) FindTodoCommit(ctx context.Context, projectID, repo, branch, todoID string) (string, error) {
	out, err := g.git(ctx, projectID, repo, "log", "-n1", "--format=%H", "--fixed-strings", "--grep=Dash-Todo: "+todoID, branch)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", fmt.Errorf("no commit for todo %s on %s", todoID, branch)
	}
	return out, nil
}

// RevertCommit reverts sha on the branch checked out in dir and returns the
// new commit. On conflict the revert is aborted and *ErrConflict returned.
func (g *Git) RevertCommit(ctx context.Context, projectID, dir, sha, msg string) (string, error) {
	args := []string{"revert", "--no-edit"}
	if parents, _ := g.git(ctx, projectID, dir, "rev-list", "--parents", "-n1", sha); len(strings.Fields(parents)) > 2 {
		args = append(args, "-m", "1") // a merge commit (e.g. a PR merged without squashing)
	}
	if _, err := g.git(ctx, projectID, dir, g.committing(ctx, dir, append(args, sha)...)...); err != nil {
		files := g.conflictedFiles(ctx, projectID, dir)
		_, _ = g.git(ctx, projectID, dir, "revert", "--abort")
		if len(files) > 0 {
			return "", &ErrConflict{Files: files}
		}
		return "", err
	}
	if msg != "" {
		if _, err := g.git(ctx, projectID, dir, g.committing(ctx, dir, "commit", "--amend", "-m", msg)...); err != nil {
			return "", err
		}
	}
	return g.Head(ctx, projectID, dir, "HEAD")
}

// AddWorktreeAt checks out a new branch from start in a fresh worktree at path.
// LandingCheckout points dir, a detached worktree that only Dash uses, at
// ref: it creates it, or discards whatever an earlier job left there.
func (g *Git) LandingCheckout(ctx context.Context, projectID, repo, dir, ref string) error {
	if _, err := os.Stat(dir); err == nil {
		_, _ = g.run(ctx, false, projectID, dir, "git", "rebase", "--abort")
		_, _ = g.run(ctx, false, projectID, dir, "git", "reset", "--hard", "-q")
		if _, err := g.git(ctx, projectID, dir, "checkout", "-q", "--force", "--detach", ref); err == nil {
			_, err = g.git(ctx, projectID, dir, "clean", "-ffdq")
			return err
		}
		_, _ = g.git(ctx, projectID, repo, "worktree", "remove", "--force", dir)
		_ = os.RemoveAll(dir)
	}
	_, _ = g.git(ctx, projectID, repo, "worktree", "prune")
	_, err := g.git(ctx, projectID, repo, "worktree", "add", "-q", "--detach", dir, ref)
	return err
}

// ErrDirtyCheckout is returned when moving a branch would overwrite
// uncommitted changes in the folder that has it checked out.
type ErrDirtyCheckout struct {
	Dir    string
	Files  []string
	Output string
}

func (e *ErrDirtyCheckout) Error() string {
	if len(e.Files) == 0 {
		return "can't update " + e.Dir + ": " + lastLine(e.Output)
	}
	return "uncommitted changes to " + strings.Join(e.Files, ", ") + " in " + e.Dir
}

var quotedPath = regexp.MustCompile(`'([^']+)'`)

// Advance moves branch from old to new (failing if it no longer points at
// old). When repo has branch checked out, its files move along the way
// `git checkout` would: unrelated uncommitted changes stay, and changes the
// move would overwrite stop it with an *ErrDirtyCheckout.
func (g *Git) Advance(ctx context.Context, projectID, repo, branch, old, new string) error {
	ref := "refs/heads/" + branch
	if head, _ := g.run(ctx, false, projectID, repo, "git", "symbolic-ref", "-q", "HEAD"); head != ref {
		_, err := g.git(ctx, projectID, repo, "update-ref", ref, new, old)
		return err
	}
	dirty := func(out string) error {
		e := &ErrDirtyCheckout{Dir: repo, Output: out}
		for _, m := range quotedPath.FindAllStringSubmatch(out, -1) {
			e.Files = append(e.Files, m[1])
		}
		return e
	}
	if out, err := g.git(ctx, projectID, repo, "read-tree", "-m", "-u", "-n", old, new); err != nil {
		return dirty(out)
	}
	if _, err := g.git(ctx, projectID, repo, "update-ref", ref, new, old); err != nil {
		return err
	}
	if out, err := g.git(ctx, projectID, repo, "read-tree", "-m", "-u", old, new); err != nil {
		_, _ = g.git(ctx, projectID, repo, "update-ref", ref, old, new)
		return dirty(out)
	}
	return nil
}

// Dirty lists which of files have uncommitted changes (or exist untracked) in dir.
func (g *Git) Dirty(ctx context.Context, projectID, dir string, files []string) ([]string, error) {
	args := []string{"status", "--porcelain", "-z", "--untracked-files=all", "--"}
	for _, f := range files {
		args = append(args, ":(top,literal)"+f) // read-tree names files from the repo root
	}
	out, err := g.run(ctx, false, projectID, dir, "git", args...)
	if err != nil {
		return nil, err
	}
	var dirty []string
	for _, e := range strings.Split(out, "\x00") {
		if len(e) > 3 {
			dirty = append(dirty, e[3:])
		}
	}
	return dirty, nil
}

func (g *Git) AddWorktreeAt(ctx context.Context, projectID, repo, path, branch, start string) error {
	_, err := g.git(ctx, projectID, repo, "worktree", "add", "-B", branch, path, start)
	return err
}

// PruneWorktrees forgets worktrees whose directories are gone.
func (g *Git) PruneWorktrees(ctx context.Context, projectID, repo string) {
	_, _ = g.git(ctx, projectID, repo, "worktree", "prune")
}

// Branches lists local branches under prefix, e.g. "dash/".
func (g *Git) Branches(ctx context.Context, projectID, repo, prefix string) ([]string, error) {
	out, err := g.git(ctx, projectID, repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/"+prefix)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// DeleteBranch force-deletes a local branch.
func (g *Git) DeleteBranch(ctx context.Context, projectID, repo, branch string) error {
	_, err := g.git(ctx, projectID, repo, "branch", "-D", branch)
	return err
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// CreateGitHubRepo uses gh to create a remote, set it as origin and push.
func (g *Git) CreateGitHubRepo(ctx context.Context, projectID, repo, name string, private bool) (string, error) {
	vis := "--public"
	if private {
		vis = "--private"
	}
	if _, err := g.gh(ctx, projectID, repo, "repo", "create", name, vis, "--source=.", "--remote=origin", "--push"); err != nil {
		return "", err
	}
	return g.git(ctx, projectID, repo, "remote", "get-url", "origin")
}

// GitHubRepo is a repository as gh sees it.
type GitHubRepo struct {
	Name string `json:"name"` // OWNER/REPO
	URL  string `json:"url"`
}

// LookupGitHubRepo finds name ("REPO" for the signed-in user, or
// "OWNER/REPO") on GitHub; ok is false when it doesn't exist.
func (g *Git) LookupGitHubRepo(ctx context.Context, name string) (repo GitHubRepo, ok bool, err error) {
	out, err := g.ghQuiet(ctx, "", "", "repo", "view", name, "--json", "nameWithOwner,url")
	if err != nil {
		if strings.Contains(out, "Could not resolve to a Repository") || strings.Contains(out, "HTTP 404") {
			return repo, false, nil
		}
		return repo, false, err
	}
	var raw struct {
		NameWithOwner string `json:"nameWithOwner"`
		URL           string `json:"url"`
	}
	if err := json.Unmarshal([]byte(jsonPart(out)), &raw); err != nil {
		return repo, false, fmt.Errorf("gh repo view: %w", err)
	}
	return GitHubRepo{Name: raw.NameWithOwner, URL: raw.URL}, true, nil
}

// CloneGitHubRepo clones name into path, which must not exist or be empty.
func (g *Git) CloneGitHubRepo(ctx context.Context, projectID, name, path string) error {
	if ents, err := os.ReadDir(path); err == nil && len(ents) > 0 {
		return fmt.Errorf("%s already exists and isn't empty", path)
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	_, err := g.gh(ctx, projectID, parent, "repo", "clone", name, path)
	return err
}

var remoteRe = regexp.MustCompile(`^(?:[a-z+]+://(?:[^@/]+@)?([^/:]+)(?::\d+)?/|[^@/]+@([^:/]+):)([^/]+)/([^/]+?)(?:\.git)?/?$`)

// RepoName turns an origin URL into gh's "HOST/OWNER/REPO", or "" when it
// doesn't look like one (e.g. a local path).
func RepoName(remote string) string {
	m := remoteRe.FindStringSubmatch(strings.TrimSpace(remote))
	if m == nil {
		return ""
	}
	host := m[1] + m[2]
	if host == "ssh.github.com" {
		host = "github.com"
	}
	return host + "/" + m[3] + "/" + m[4]
}

// DeleteGitHubRepo deletes origin's repository on GitHub with gh. gh needs
// the delete_repo scope (gh auth refresh -s delete_repo).
func (g *Git) DeleteGitHubRepo(ctx context.Context, projectID, repo, remote string) error {
	name := RepoName(remote)
	if name == "" {
		return fmt.Errorf("%q is not a GitHub repository URL", remote)
	}
	_, err := g.gh(ctx, projectID, repo, "repo", "delete", name, "--yes")
	return err
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

// CheckoutState is the branch checked out in dir ("" when HEAD is
// detached, see CheckedOut) and whether dir has uncommitted changes to
// tracked files. Untracked files don't count: a fast-forward that would
// overwrite one stops on its own (see Advance).
func (g *Git) CheckoutState(ctx context.Context, projectID, dir string) (branch string, dirty bool, err error) {
	branch = g.CheckedOut(ctx, projectID, dir)
	out, err := g.run(ctx, false, projectID, dir, "git", "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return branch, false, err
	}
	return branch, strings.TrimSpace(out) != "", nil
}

// Contains reports whether commit sha exists in repo and is ref or one of
// its ancestors (false when either is unknown).
func (g *Git) Contains(ctx context.Context, projectID, repo, sha, ref string) bool {
	if !IsSHA(sha) {
		return false
	}
	_, err := g.run(ctx, false, projectID, repo, "git", "merge-base", "--is-ancestor", sha+"^{commit}", ref)
	return err == nil
}

// IsSHA reports whether s looks like a full or abbreviated commit id.
func IsSHA(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// HasBranch reports whether the local branch exists.
func (g *Git) HasBranch(ctx context.Context, projectID, dir, branch string) bool {
	_, err := g.run(ctx, false, projectID, dir, "git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return branch != "" && err == nil
}

// MainBranch guesses an existing repo's main line: the local branch a
// remote's HEAD points at, else main or master, else the one checked out.
// It returns "" when none of them exists.
func (g *Git) MainBranch(ctx context.Context, projectID, dir string) string {
	out, _ := g.run(ctx, false, projectID, dir, "git", "for-each-ref", "--format=%(symref:lstrip=3)", "refs/remotes/*/HEAD")
	candidates := append(strings.Fields(out), "main", "master")
	if b, err := g.run(ctx, false, projectID, dir, "git", "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		candidates = append(candidates, b)
	}
	for _, b := range candidates {
		if g.HasBranch(ctx, projectID, dir, b) {
			return b
		}
	}
	return ""
}

// RemoteURL returns origin's URL, or "" if there is no origin.
func (g *Git) RemoteURL(ctx context.Context, projectID, dir string) string {
	out, err := g.git(ctx, projectID, dir, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return out
}

// Graph returns `git log --graph` of branch, newest first. Commit lines carry
// short hash, refs, subject, author, relative date and full hash after the
// graph, separated by \x1f.
func (g *Git) Graph(ctx context.Context, projectID, repo, branch string, limit int) (string, error) {
	return g.run(ctx, false, projectID, repo, "git", "log", "--graph", "--color=never",
		"--max-count="+strconv.Itoa(limit), "--format=\x1f%h\x1f%D\x1f%s\x1f%an\x1f%ar\x1f%H", branch, "--")
}

// Files lists the paths of the files tracked at ref.
func (g *Git) Files(ctx context.Context, projectID, repo, ref string) ([]string, error) {
	out, err := g.run(ctx, false, projectID, repo, "git", "ls-tree", "-r", "--name-only", "-z", ref)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(strings.TrimRight(out, "\x00"), "\x00"), nil
}

// LocalFiles lists the files in repo's checkout that git doesn't track:
// untracked ones and ignored ones such as .env. Ignored directories
// (node_modules, build output) are skipped rather than listed file by file.
func (g *Git) LocalFiles(ctx context.Context, projectID, repo string) ([]string, error) {
	var files []string
	for _, args := range [][]string{
		{"ls-files", "-z", "--others", "--exclude-standard"},
		{"ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory"},
	} {
		out, err := g.run(ctx, false, projectID, repo, "git", args...)
		if err != nil {
			return nil, err
		}
		for _, f := range strings.Split(out, "\x00") {
			if f != "" && !strings.HasSuffix(f, "/") {
				files = append(files, f)
			}
		}
	}
	return files, nil
}

// Blob returns the object id of path at ref ("" and an error if it isn't there).
func (g *Git) Blob(ctx context.Context, projectID, repo, ref, path string) (string, error) {
	return g.run(ctx, false, projectID, repo, "git", "rev-parse", "--verify", "--quiet", ref+":"+path)
}

// ReadBlob returns a blob's content as stored, without trimming, up to max
// bytes; size is the full size.
func (g *Git) ReadBlob(ctx context.Context, projectID, repo, blob string, max int64) (content []byte, size int64, err error) {
	s, err := g.run(ctx, false, projectID, repo, "git", "cat-file", "-s", blob)
	if err != nil {
		return nil, 0, err
	}
	if size, err = strconv.ParseInt(s, 10, 64); err != nil || size > max {
		return nil, size, err
	}
	if err := localrun.Check("git cat-file blob"); err != nil {
		return nil, 0, err
	}
	cmd := exec.CommandContext(ctx, "git", "cat-file", "blob", blob)
	cmd.Dir = repo
	content, err = cmd.Output()
	return content, size, err
}

// Unpushed counts the commits on branch that origin doesn't have, as of the
// last fetch or push (every commit if branch was never pushed).
func (g *Git) Unpushed(ctx context.Context, projectID, repo, branch string) (int, error) {
	out, err := g.run(ctx, false, projectID, repo, "git", "rev-list", "--count", g.unpushedRange(ctx, projectID, repo, branch))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

// UnpushedTodos lists the todos (by their "Dash-Todo: <id>" trailer) whose
// commits on branch origin doesn't have yet.
func (g *Git) UnpushedTodos(ctx context.Context, projectID, repo, branch string) ([]string, error) {
	out, err := g.run(ctx, false, projectID, repo, "git", "log", "--format=%B", g.unpushedRange(ctx, projectID, repo, branch), "--")
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, l := range strings.Split(out, "\n") {
		if id, ok := strings.CutPrefix(strings.TrimSpace(l), "Dash-Todo: "); ok {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (g *Git) unpushedRange(ctx context.Context, projectID, repo, branch string) string {
	if _, err := g.run(ctx, false, projectID, repo, "git", "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch); err == nil {
		return "origin/" + branch + ".." + branch
	}
	return branch
}

// Commit resolves ref to a commit id.
func (g *Git) Commit(ctx context.Context, projectID, repo, ref string) (string, error) {
	return g.run(ctx, false, projectID, repo, "git", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
}

// MergeBase is the best common ancestor of two commits.
func (g *Git) MergeBase(ctx context.Context, projectID, repo, a, b string) (string, error) {
	return g.run(ctx, false, projectID, repo, "git", "merge-base", a, b)
}

// ChangedFiles lists the files that differ between two commits, deleted
// ones left out.
func (g *Git) ChangedFiles(ctx context.Context, projectID, repo, from, to string) ([]string, error) {
	out, err := g.run(ctx, false, projectID, repo, "git", "diff", "--name-only", "--diff-filter=d", "-z", from, to)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// AddDetachedWorktree checks commit out at path without a branch: a
// throwaway copy for work that only reads, such as a scan.
func (g *Git) AddDetachedWorktree(ctx context.Context, projectID, repo, path, commit string) error {
	_, err := g.git(ctx, projectID, repo, "worktree", "add", "--detach", path, commit)
	return err
}

// RemoveDetachedWorktree deletes a worktree made by AddDetachedWorktree.
func (g *Git) RemoveDetachedWorktree(ctx context.Context, projectID, repo, path string) {
	if _, err := g.git(ctx, projectID, repo, "worktree", "remove", "--force", path); err != nil {
		_ = os.RemoveAll(path)
		_, _ = g.git(ctx, projectID, repo, "worktree", "prune")
	}
}

var scpRemoteRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._~/-]+$`)

// AllowLocalRemotes lets CheckRemote accept local paths and file:// URLs
// (tests only: a runner must not let its hub clone its own files).
var AllowLocalRemotes = false

// CheckRemote accepts a network git remote someone else gave us: https://,
// ssh:// or scp-like user@host:path. It refuses local paths, file:// and
// git's other transports (ext::, fd::), which would let whoever chose the
// URL read files on this computer or run commands.
func CheckRemote(remote string) error {
	r := strings.TrimSpace(remote)
	if AllowLocalRemotes && r != "" && !strings.HasPrefix(r, "-") {
		return nil
	}
	switch {
	case r == "" || strings.HasPrefix(r, "-") || strings.ContainsAny(r, " \t\r\n"):
	case strings.HasPrefix(r, "https://"), strings.HasPrefix(r, "ssh://"):
		u, err := url.Parse(r)
		if err == nil && u.Host != "" && !strings.HasPrefix(u.Host, "-") {
			return nil
		}
	case scpRemoteRe.MatchString(r) && !strings.Contains(r, "::"):
		return nil
	}
	return fmt.Errorf("%q isn't a remote a runner clones: use an https://, ssh:// or user@host:path URL", remote)
}

// Clone clones remote (checked with CheckRemote) into path, which must not
// exist or be empty. Only network transports are allowed.
func (g *Git) Clone(ctx context.Context, projectID, remote, path string) error {
	if err := CheckRemote(remote); err != nil {
		return err
	}
	if ents, err := os.ReadDir(path); err == nil && len(ents) > 0 {
		return fmt.Errorf("%s already exists and isn't empty", path)
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	args := []string{"-c", "protocol.ext.allow=never", "-c", "protocol.fd.allow=never"}
	if !AllowLocalRemotes {
		args = append(args, "-c", "protocol.file.allow=never")
	}
	args = append(args, "clone", "--", strings.TrimSpace(remote), path)
	_, err := g.git(ctx, projectID, parent, args...)
	return err
}
