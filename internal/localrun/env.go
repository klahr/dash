package localrun

import (
	"os"
	"slices"
	"strings"
)

// RepoEnv are the git variables that pick which repository, index or
// object store git works on instead of the one it finds from its working
// directory. Git sets some of them for the commands it starts (hooks,
// rebase --exec, bisect run, aliases): inherited by a process Dash starts
// in another folder, they point that process's git at the wrong repository.
// With GIT_DIR naming a task worktree's git directory, even `git init
// /tmp/x` rewrites the main clone's shared config to core.bare = true.
var RepoEnv = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_NAMESPACE", "GIT_PREFIX", "GIT_IMPLICIT_WORK_TREE",
	"GIT_INTERNAL_SUPER_PREFIX", "GIT_QUARANTINE_PATH", "GIT_SHALLOW_FILE", "GIT_GRAFT_FILE",
}

// Environ is this process's environment without RepoEnv, followed by
// extra: what every git, Claude and project command Dash starts gets, so
// each finds the repository from the folder it runs in.
func Environ(extra ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(RepoEnv, k) {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}
