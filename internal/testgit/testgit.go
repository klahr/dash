// Package testgit keeps the git that tests start off the computer's own
// repositories and settings. A package whose tests run git calls Run from
// its TestMain.
//
// Tests run inside a checkout (on a runner, a task's worktree of a clone
// Dash keeps in sync). A git variable inherited from a hook or a
// `git rebase --exec` that names that checkout's git directory turns a
// test's harmless `git init /tmp/x` into a rewrite of the clone's shared
// config (core.bare = true: the clone's own folder then fails with "this
// operation must be run in a work tree"). The developer's global config
// (url.insteadOf, commit.gpgsign) has broken tests too.
package testgit

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/localrun"
)

// Run runs m's tests with git isolated (see Isolate) and returns their
// exit code.
func Run(m *testing.M) int {
	cleanup, err := Isolate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testgit:", err)
		return 1
	}
	defer cleanup()
	return m.Run()
}

// Isolate sets up this process's environment, which every git a test
// starts inherits (directly, through Dash or through a shell):
//   - without localrun.RepoEnv, so git finds its repository from the
//     folder it runs in;
//   - GIT_CONFIG_GLOBAL is an empty config of its own (commit signing
//     off) and GIT_CONFIG_NOSYSTEM is set, so the computer's settings
//     don't change what git does;
//   - GIT_CEILING_DIRECTORIES stops git from looking above the package's
//     folder, so a git a test starts without a folder of its own fails
//     instead of working on the checkout the tests run in.
//
// cleanup removes the config file.
func Isolate() (cleanup func(), err error) {
	for _, v := range localrun.RepoEnv {
		os.Unsetenv(v)
	}
	dir, err := os.MkdirTemp("", "dash-testgit-")
	if err != nil {
		return nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	cfg := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(cfg, []byte("[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n"), 0o644); err != nil {
		cleanup()
		return nil, err
	}
	wd, err := os.Getwd()
	if err != nil {
		cleanup()
		return nil, err
	}
	os.Setenv("GIT_CONFIG_GLOBAL", cfg)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(wd))
	return cleanup, nil
}
