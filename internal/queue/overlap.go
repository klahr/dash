package queue

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/klahr/dash/internal/model"
)

// Before a queued task starts, the dispatcher looks for another task of its
// project that should go first: one whose title or prompt names a file this
// one's does too, and that is either ahead of it in the queue or already
// started (in progress, in review or merging). Run side by side, the two
// would likely conflict when they merge; started after the other is
// merged, this one builds on its code. So it waits, queued, until that
// task is merged (or leaves the queue another way: failed, moved to
// drafts, deleted), with the reason in its Waiting. Start now runs it
// anyway.
//
// Only a task's first run waits: one sent back with feedback, a conflict
// to fix or a run to resume already has its branch. A task its hub sent
// to this runner doesn't wait here either; the hub made that choice.

// GoFirstNote marks a Waiting reason set by overlapWait; the web client
// offers Start now on such a task.
const GoFirstNote = "to go first"

// overlapWait says why queued task q waits for another task to go first,
// or "" if it doesn't. project lists q's project's tasks (store order:
// queue order), loaded once per dispatch walk.
func overlapWait(q model.Todo, project []model.Todo) string {
	if !waitsForOverlap(q) {
		return ""
	}
	mine := namedFiles(q.Title + "\n" + q.Prompt)
	if len(mine) == 0 {
		return ""
	}
	byID := model.TodosByID(project)
	for _, x := range project {
		if x.ID == q.ID || x.ProjectID != q.ProjectID || !goesFirst(x, q) || needs(x, q.ID, byID) {
			continue
		}
		if shared := sharedFiles(mine, namedFiles(x.Title+"\n"+x.Prompt)); len(shared) > 0 {
			return overlapNote(x, shared)
		}
	}
	return ""
}

// waitsForOverlap reports whether queued task q is one that may wait for
// another task to go first: not yet started, not started now by hand, and
// not a hub's task (which its hub already held).
func waitsForOverlap(q model.Todo) bool {
	return !q.StartNow && q.HubTodoID == "" && !q.Rejoin && !q.MergeMain && !q.Resumable &&
		q.Worktree == nil && q.Runs == 0 && q.Runner == ""
}

// goesFirst reports whether task x goes before queued task q: x has started
// and isn't merged yet, or x is queued, not paused, ahead of q.
func goesFirst(x, q model.Todo) bool {
	switch x.Status {
	case model.StatusInProgress, model.StatusMerging:
		return true
	case model.StatusReview:
		return !x.NoChanges // nothing to merge, so nothing to conflict with
	case model.StatusQueued:
		return !x.Paused && model.QueueBefore(x, q)
	}
	return false
}

// needs reports whether task x waits, through its dependencies or the
// task it builds on, for task id: then id must not wait for x.
func needs(x model.Todo, id string, byID map[string]model.Todo) bool {
	seen := map[string]bool{}
	var walk func(t model.Todo) bool
	walk = func(t model.Todo) bool {
		next := slices.Clone(t.DependsOn)
		if t.BuildsOn != "" {
			next = append(next, t.BuildsOn)
		}
		for _, d := range next {
			if d == id {
				return true
			}
			if seen[d] {
				continue
			}
			seen[d] = true
			if dt, ok := byID[d]; ok && walk(dt) {
				return true
			}
		}
		return false
	}
	return walk(x)
}

// overlapNote is the Waiting reason of a task that waits for x, both naming
// the files shared.
func overlapNote(x model.Todo, shared []string) string {
	where := "is ahead in the queue"
	switch x.Status {
	case model.StatusInProgress:
		where = "is running"
	case model.StatusReview:
		where = "is in review"
	case model.StatusMerging:
		where = "is merging"
	}
	files := shared[0]
	if len(shared) > 1 {
		files += fmt.Sprintf(" and %d more", len(shared)-1)
	}
	return fmt.Sprintf("waits for “%s” (%s) %s: both name %s, so running them side by side would likely conflict; it starts once that task is merged, or use Start now",
		x.Name(60), where, GoFirstNote, files)
}

// fileToken is a run of path characters, with the backticks around it if any.
var fileToken = regexp.MustCompile("(`?)([A-Za-z0-9_./-]+)(`?)")

// codeExts are the extensions a file name without a folder must have to
// count (in backticks): a bare "example.com" or "e.g." is no file.
var codeExts = map[string]bool{
	"c": true, "cc": true, "cfg": true, "conf": true, "cpp": true, "cs": true, "css": true, "env": true,
	"go": true, "gradle": true, "h": true, "hpp": true, "html": true, "ini": true, "java": true,
	"js": true, "json": true, "jsx": true, "kt": true, "lock": true, "lua": true, "md": true,
	"mjs": true, "mod": true, "php": true, "proto": true, "py": true, "rb": true, "rs": true,
	"scss": true, "sh": true, "sql": true, "sum": true, "svelte": true, "swift": true, "toml": true,
	"ts": true, "tsx": true, "txt": true, "vue": true, "xml": true, "yaml": true, "yml": true,
}

// namedFiles are the repository files text names: relative paths with a
// folder and an extension ("web/static/app/todos.js"), and file names in
// backticks with a known extension (`README.md`). URLs and absolute paths
// don't count; they say nothing about the repository's files.
func namedFiles(text string) []string {
	var out []string
	for _, m := range fileToken.FindAllStringSubmatchIndex(text, -1) {
		tok := text[m[4]:m[5]]
		ticked := m[3] > m[2] && m[7] > m[6]
		if m[4] > 0 && text[m[4]-1] == ':' {
			continue // part of a URL ("https://…") or "host:path"
		}
		tok = strings.TrimRight(strings.TrimPrefix(tok, "./"), ".")
		if tok == "" || strings.HasPrefix(tok, "/") || strings.Contains(tok, "//") || strings.Contains(tok, "..") {
			continue
		}
		base := path.Base(tok)
		ext := strings.TrimPrefix(path.Ext(base), ".")
		if ext == "" || len(ext) > 6 || len(base) == len(ext)+1 || !hasLetter(ext) {
			continue
		}
		if !strings.Contains(tok, "/") && !(ticked && codeExts[strings.ToLower(ext)]) {
			continue
		}
		if !slices.Contains(out, tok) {
			out = append(out, tok)
		}
	}
	return out
}

func hasLetter(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }) >= 0
}

// sharedFiles are the files both a and b name: the same path, or a file
// name without a folder and a path ending in it. Each is given in its
// longer form.
func sharedFiles(a, b []string) []string {
	var out []string
	for _, x := range a {
		for _, y := range b {
			var f string
			switch {
			case x == y:
				f = x
			case !strings.Contains(x, "/") && strings.HasSuffix(y, "/"+x):
				f = y
			case !strings.Contains(y, "/") && strings.HasSuffix(x, "/"+y):
				f = x
			}
			if f != "" && !slices.Contains(out, f) {
				out = append(out, f)
			}
		}
	}
	return out
}
