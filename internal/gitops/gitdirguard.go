package gitops

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// safeConfig is set for every git Dash runs, the way -c is: it wins over
// every config file. Claude writes the worktrees Dash then runs git in, so
// nothing git would run from there may come from them: no file system
// monitor, and no hooks (a project's core.hooksPath may name a folder in
// the checkout, e.g. .githooks, that Claude can change).
var safeConfig = []string{
	"core.fsmonitor", "false",
	"core.hooksPath", os.DevNull,
}

// SafeConfigArgs are safeConfig as git's -c options, for git run outside
// this package.
func SafeConfigArgs() []string {
	var out []string
	for i := 0; i < len(safeConfig); i += 2 {
		out = append(out, "-c", safeConfig[i]+"="+safeConfig[i+1])
	}
	return out
}

// ErrGitDir is a worktree whose .git doesn't lead to git metadata Dash can
// trust: see CheckGitDir.
var ErrGitDir = errors.New("the worktree's .git doesn't point to its repository's metadata")

// CheckGitDir refuses to let Dash run git in dir when dir is a linked
// worktree whose .git file could make git read metadata (config, hooks)
// that whoever wrote the worktree planted. A linked worktree's .git is a
// file naming its gitdir, <common>/worktrees/<name>, whose commondir file
// names <common>. Both must lie outside the worktree, have that shape, and
// the gitdir's own gitdir file must point back to this worktree's .git.
// A dir without .git, or with a .git directory (a clone), passes: that is
// not a worktree Claude was given. So does a submodule's checkout, whose
// gitdir (outside it) has no commondir.
func CheckGitDir(dir string) error {
	if dir == "" {
		return nil
	}
	dotgit := filepath.Join(dir, ".git")
	fi, err := os.Lstat(dotgit)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return nil
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s isn't a file", ErrGitDir, dotgit)
	}
	wt, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(dotgit)
	if err != nil {
		return err
	}
	p, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return fmt.Errorf("%w: %s", ErrGitDir, dotgit)
	}
	gitdir, err := resolve(wt, strings.TrimSpace(p))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGitDir, err)
	}
	if within(wt, gitdir) {
		return fmt.Errorf("%w: %s is inside the worktree", ErrGitDir, gitdir)
	}
	c, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil // a submodule's checkout: its gitdir is a repository of its own
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGitDir, err)
	}
	common, err := resolve(gitdir, strings.TrimSpace(string(c)))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGitDir, err)
	}
	if within(wt, common) || filepath.Dir(gitdir) != filepath.Join(common, "worktrees") {
		return fmt.Errorf("%w: %s", ErrGitDir, gitdir)
	}
	back, err := os.ReadFile(filepath.Join(gitdir, "gitdir"))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGitDir, err)
	}
	if b, err := resolve(gitdir, strings.TrimSpace(string(back))); err != nil || b != filepath.Join(wt, ".git") {
		return fmt.Errorf("%w: %s belongs to another worktree", ErrGitDir, gitdir)
	}
	return nil
}

// resolve is p, relative to base unless absolute, with symlinks resolved.
func resolve(base, p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.EvalSymlinks(p)
}

// within reports whether p is dir or inside it (both cleaned, absolute).
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// GitDirState is what decides which git metadata git uses in dir: its .git
// file and its gitdir's commondir and config.worktree. Compare it before
// and after a run that could write dir: a change means the run tried to
// redirect git.
func GitDirState(dir string) string {
	var b strings.Builder
	dotgit := filepath.Join(dir, ".git")
	if fi, err := os.Lstat(dotgit); err != nil {
		return "none"
	} else if !fi.Mode().IsRegular() {
		return fi.Mode().String()
	}
	gf, _ := os.ReadFile(dotgit)
	b.Write(gf)
	if p, ok := strings.CutPrefix(strings.TrimSpace(string(gf)), "gitdir:"); ok {
		gitdir := strings.TrimSpace(p)
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(dir, gitdir)
		}
		for _, f := range []string{"commondir", "config.worktree"} {
			c, err := os.ReadFile(filepath.Join(gitdir, f))
			fmt.Fprintf(&b, "\x00%s\x00%v\x00%s", f, err == nil, c)
		}
	}
	return b.String()
}
