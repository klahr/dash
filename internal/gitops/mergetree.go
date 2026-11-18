package gitops

import (
	"context"
	"errors"
	"os/exec"
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
