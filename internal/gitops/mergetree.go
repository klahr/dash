package gitops

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
)

// ErrNoMergeTree is a git too old for `git merge-tree --write-tree` (it came
// in git 2.38): the early conflict check is skipped there.
var ErrNoMergeTree = errors.New("git merge-tree --write-tree needs git 2.38 or later")

// MergeConflicts merges commits a and b in repo without touching any
// checkout or ref (`git merge-tree --write-tree`), and returns the files
// the merge conflicts in: none when it merges cleanly.
func (g *Git) MergeConflicts(ctx context.Context, projectID, repo, a, b string) ([]string, error) {
	_, err := g.run(ctx, false, projectID, repo, "git", "merge-tree", "--write-tree", "--name-only", "-z", "--no-messages", a, b)
	if err == nil {
		return nil, nil
	}
	var ce *CmdError
	var ee *exec.ExitError
	if !errors.As(err, &ce) || !errors.As(err, &ee) {
		return nil, err
	}
	switch {
	case ee.ExitCode() == 1 && IsSHA(strings.SplitN(ce.Output, "\x00", 2)[0]):
		// Conflicts. (A commit it can't find also exits 1, with a message.)
		return ParseMergeTree(ce.Output), nil
	case ee.ExitCode() == 129 || strings.Contains(ce.Output, "usage: git merge-tree"):
		// An unknown option: git before 2.38 (an old one also takes
		// --write-tree for a revision and prints its usage).
		return nil, ErrNoMergeTree
	}
	return nil, err
}

// ParseMergeTree reads the conflicted files from the output of
// `git merge-tree --write-tree --name-only -z`: the merged tree's id, then
// each conflicted path, NUL-terminated, then an empty field before any
// informational messages. Paths are listed once each, in git's order.
func ParseMergeTree(out string) []string {
	fields := strings.Split(out, "\x00")
	if len(fields) < 2 {
		return nil
	}
	var files []string
	seen := map[string]bool{}
	for _, f := range fields[1:] { // fields[0] is the tree
		if f == "" {
			break // the end of the conflicted files
		}
		if !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	return files
}

// BranchHeads maps each local branch (its full ref, "refs/heads/…") to the
// commit it points at, in one cheap command.
func (g *Git) BranchHeads(ctx context.Context, projectID, repo string) (map[string]string, error) {
	out, err := g.run(ctx, false, projectID, repo, "git", "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	heads := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if sha, ref, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			heads[ref] = sha
		}
	}
	return heads, nil
}

// FilesSinceFork lists the files changed on to since it forked from from
// (`git diff from...to`): added, changed, deleted, and both sides of a
// rename.
func (g *Git) FilesSinceFork(ctx context.Context, projectID, repo, from, to string) ([]string, error) {
	out, err := g.run(ctx, false, projectID, repo, "git", "diff", "--name-only", "--no-renames", "-z", from+"..."+to, "--")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// Landed is the commit on the default branch that already holds a
// branch's changes (see LandedOn).
type Landed struct {
	Commit  string
	Subject string
	Todo    string // its "Dash-Todo: <id>" trailer, when it has one
}

// LandedOn tells whether the branch checked out in dir changes nothing
// against base because base already has its changes: its own commits (not
// those merged in from base) changed files that base, since the branch
// forked, changed to the same content. It returns the newest commit on base
// that changed them. ok is false when the branch's commits changed nothing
// on balance (a change undone), or when it has none.
func (g *Git) LandedOn(ctx context.Context, projectID, dir, base string) (l Landed, ok bool, err error) {
	own, err := g.git(ctx, projectID, dir, "rev-list", "--no-merges", "--reverse", base+"..HEAD")
	if err != nil || own == "" {
		return l, false, err
	}
	first, _, _ := strings.Cut(own, "\n")
	fork, err := g.git(ctx, projectID, dir, "rev-parse", "--verify", "--quiet", first+"^")
	if err != nil {
		return l, false, nil // a root commit: nothing it forked from
	}
	out, err := g.git(ctx, projectID, dir, "log", "--no-merges", "--no-renames", "--name-only", "-z", "--format=", base+"..HEAD", "--")
	if err != nil {
		return l, false, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f = strings.TrimSpace(f); f != "" && !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return l, false, nil
	}
	changed, err := g.git(ctx, projectID, dir, append([]string{"diff", "--name-only", "--no-renames", fork, base, "--"}, files...)...)
	if err != nil || changed == "" {
		return l, false, err
	}
	rec, err := g.git(ctx, projectID, dir, append([]string{"log", "-n1", "--format=%H%x1f%s%x1f%B", base, "^" + fork, "--"}, strings.Split(changed, "\n")...)...)
	if err != nil || rec == "" {
		return l, false, err
	}
	parts := strings.SplitN(rec, "\x1f", 3)
	if len(parts) < 3 {
		return l, false, nil
	}
	l.Commit, l.Subject = parts[0], parts[1]
	for _, line := range strings.Split(parts[2], "\n") {
		if id, ok := strings.CutPrefix(strings.TrimSpace(line), "Dash-Todo: "); ok {
			l.Todo = strings.TrimSpace(id)
		}
	}
	return l, true, nil
}
