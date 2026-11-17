package gitops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Dash runs git as the runner's user in worktrees that Claude has written
// to, perhaps as another user or in a container (-claude-sandbox, see
// docs/sandbox.md). A linked worktree's .git file is one of the files Claude
// can rewrite: pointed at a folder of Claude's making, it would give git a
// config of Claude's (core.fsmonitor, hooks, filters, an ssh command), which
// git then runs as the runner's user. So git in a worktree runs with GIT_DIR
// set to the worktree's folder in its project's own .git, found from the
// project's repository and never from the worktree's .git file.

// PinnedEnv is GIT_DIR and GIT_WORK_TREE for git in dir when dir is in a
// worktree under one of WorktreeRoots (<root>/<project>/<name>), nil when it
// isn't, and an error when it is but its project's repository has no
// worktree there.
func (g *Git) PinnedEnv(dir string) ([]string, error) {
	wt, projectID, ok := g.worktreeOf(dir)
	if !ok {
		return nil, nil
	}
	repo := ""
	if g.RepoOf != nil {
		repo, ok = g.RepoOf(projectID)
	}
	if !ok || repo == "" {
		return nil, fmt.Errorf("%s: no project %q to find its git folder in", wt, projectID)
	}
	common, err := commonDir(repo)
	if err != nil {
		return nil, err
	}
	gitdir, err := worktreeGitDir(common, wt)
	if err != nil {
		return nil, err
	}
	return []string{"GIT_DIR=" + gitdir, "GIT_WORK_TREE=" + wt}, nil
}

// worktreeOf is the worktree (<root>/<project>/<name>) dir is in, and its
// project, if dir is under one of g.WorktreeRoots.
func (g *Git) worktreeOf(dir string) (wt, projectID string, ok bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", false
	}
	for _, root := range g.WorktreeRoots {
		if root == "" {
			continue
		}
		root, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		parts := strings.SplitN(rel, string(filepath.Separator), 3)
		if len(parts) < 2 {
			return "", "", false // a project's folder of worktrees: no repository
		}
		return filepath.Join(root, parts[0], parts[1]), parts[0], true
	}
	return "", "", false
}

// commonDir is the git folder of the repository checked out in repo (its
// .git, or the main one's when repo is a linked worktree itself).
func commonDir(repo string) (string, error) {
	repo, err := filepath.Abs(repo)
	if err != nil {
		return "", err
	}
	dotgit := filepath.Join(repo, ".git")
	fi, err := os.Stat(dotgit)
	if err != nil {
		return "", fmt.Errorf("%s isn't a git repository: %w", repo, err)
	}
	if fi.IsDir() {
		return dotgit, nil
	}
	gitdir, err := readPathFile(dotgit, repo, "gitdir: ")
	if err != nil {
		return "", err
	}
	if c, err := readPathFile(filepath.Join(gitdir, "commondir"), gitdir, ""); err == nil {
		return c, nil
	}
	return gitdir, nil
}

// worktreeGitDir is the folder in common (common/worktrees/<id>) that git
// keeps the worktree at wt in: the one whose gitdir file points to wt/.git.
func worktreeGitDir(common, wt string) (string, error) {
	dir := filepath.Join(common, "worktrees")
	cands := []string{filepath.Base(wt)} // git names it after the folder
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() && e.Name() != filepath.Base(wt) {
				cands = append(cands, e.Name())
			}
		}
	}
	for _, id := range cands {
		c := filepath.Join(dir, id)
		if got, err := readPathFile(filepath.Join(c, "gitdir"), c, ""); err == nil && filepath.Base(got) == ".git" && samePath(filepath.Dir(got), wt) {
			return c, nil
		}
	}
	return "", fmt.Errorf("%s isn't a worktree of the repository at %s", wt, filepath.Dir(common))
}

// readPathFile reads the path in file (after prefix), relative to base.
func readPathFile(file, base, prefix string) (string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	p, ok := strings.CutPrefix(strings.TrimSpace(string(b)), prefix)
	if !ok || p == "" {
		return "", fmt.Errorf("%s: not a path", file)
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p), nil
}

// samePath reports whether folders a and b are the same. Only folders: the
// worktree's .git itself may be a link of Claude's making.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}
