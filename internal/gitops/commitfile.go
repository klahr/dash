package gitops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// plumb runs git in repo with extra environment and input, for plumbing
// commands that don't touch any checkout.
func (g *Git) plumb(ctx context.Context, repo string, env []string, stdin []byte, args ...string) (string, error) {
	return g.runIn(ctx, false, env, stdin, "", repo, "git", args...)
}

// CommitFile makes a commit on top of parent that sets path to content and
// changes nothing else, without a checkout: no branch moves and no files
// are written. It returns the new commit and the file's new blob.
func (g *Git) CommitFile(ctx context.Context, projectID, repo, parent, path, content, msg string) (commit, blob string, err error) {
	blob, err = g.plumb(ctx, repo, nil, []byte(content), "hash-object", "-w", "--stdin", "--path", path)
	if err != nil {
		return "", "", err
	}
	mode := "100644"
	if out, _ := g.plumb(ctx, repo, nil, nil, "ls-tree", parent, "--", path); out != "" {
		if f := strings.Fields(out); len(f) > 0 && strings.HasPrefix(f[0], "100") {
			mode = f[0] // keep an executable bit
		}
	}
	tmp, err := os.MkdirTemp("", "dash-index-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(tmp)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	if _, err := g.plumb(ctx, repo, env, nil, "read-tree", parent); err != nil {
		return "", "", err
	}
	if _, err := g.plumb(ctx, repo, env, nil, "update-index", "--add", "--cacheinfo", mode+","+blob+","+path); err != nil {
		return "", "", err
	}
	tree, err := g.plumb(ctx, repo, env, nil, "write-tree")
	if err != nil {
		return "", "", err
	}
	commit, err = g.git(ctx, projectID, repo, g.committing(ctx, repo, "commit-tree", tree, "-p", parent, "-m", msg)...)
	if err != nil {
		return "", "", err
	}
	return commit, blob, nil
}

// PushCommit pushes sha to origin's branch, only as a fast-forward: when
// origin moved on meanwhile, the push is rejected and nothing changes there.
func (g *Git) PushCommit(ctx context.Context, projectID, repo, sha, branch string) error {
	_, err := g.git(ctx, projectID, repo, "push", "origin", sha+":refs/heads/"+branch)
	return err
}

// FastForward moves the branch checked out in dir to sha, a commit made on
// top of it, and updates the checkout's files to match. It fails, changing
// nothing, when sha doesn't descend from the branch or a local change in
// dir would be overwritten.
func (g *Git) FastForward(ctx context.Context, projectID, dir, sha string) error {
	_, err := g.git(ctx, projectID, dir, "merge", "--ff-only", "--quiet", sha)
	return err
}
