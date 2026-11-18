package gitops

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// WorkTree is the tree object of the files checked out in dir as they are
// now: what committing every change (as CommitAll does: tracked and
// untracked files, not ignored ones) would commit. dir's own index and
// files stay as they are; a copy of the index is used, so the files git
// already knows aren't read again. Two equal trees are the same code, on
// whatever commit.
func (g *Git) WorkTree(ctx context.Context, projectID, dir string) (string, error) {
	tmp, err := os.MkdirTemp("", "dash-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	index := filepath.Join(tmp, "index")
	cur, err := g.run(ctx, false, projectID, dir, "git", "rev-parse", "--git-path", "index")
	if err != nil {
		return "", err
	}
	if cur = strings.TrimSpace(cur); !filepath.IsAbs(cur) {
		cur = filepath.Join(dir, cur)
	}
	if b, err := os.ReadFile(cur); err == nil {
		if err := os.WriteFile(index, b, 0o600); err != nil {
			return "", err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := g.runEnv(ctx, false, env, projectID, dir, "git", "add", "-A"); err != nil {
		return "", err
	}
	tree, err := g.runEnv(ctx, false, env, projectID, dir, "git", "write-tree")
	return strings.TrimSpace(tree), err
}
