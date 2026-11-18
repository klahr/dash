package mcptools

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/textutil"
)

// ActiveWorkTool is what a running task calls to see the work of its
// project's other tasks (ActiveWork).
const ActiveWorkTool = "active_work"

// RunTestsTool is what a running task calls to run its project's Test
// command through Dash (RunTests).
const RunTestsTool = "run_tests"

// TaskToolNames are what a running task may get (TaskTools).
var TaskToolNames = append(slices.Clone(IssueToolNames), ActiveWorkTool, RunTestsTool)

// WorkFiler is a Board that can tell which files a task's work changes.
type WorkFiler interface {
	// WorkFiles lists the files task t changes compared with the branch it
	// lands on, since their merge base: what its branch has committed, and
	// in a worktree on this computer also what isn't committed yet. A task
	// on a runner is asked of its runner.
	WorkFiles(t model.Todo) ([]string, error)
}

// TestRunner is a Board that runs a running task's tests for it.
type TestRunner interface {
	// RunTests runs the project's Test command in running task id's
	// worktree, as Dash runs it before review, waits for it and returns
	// the result. Dash keeps it: when it passed and no file changed since,
	// Dash doesn't run the tests again when Claude finishes.
	RunTests(id string) (model.Checks, error)
}

// TaskTools returns a running task's tools: the IssueTools, active_work,
// and run_tests when b is a TestRunner.
func TaskTools(b Board, o Options) []Tool {
	tools := append(IssueTools(b, o), ActiveWork(b, o))
	if tr, ok := b.(TestRunner); ok && o.Todo != "" {
		tools = append(tools, RunTests(tr, o.Todo))
	}
	return tools
}

// RunTests returns run_tests: the project's Test command, run by Dash in
// task self's worktree.
func RunTests(tr TestRunner, self string) Tool {
	return Tool{RunTestsTool,
		"Run the project's Test command in this task's worktree, through Dash, and wait for the result " +
			"(exit code and the last lines of output). Use it instead of running the test command yourself: " +
			"when the tests pass and you change no file afterwards, Dash doesn't run them again when you finish.",
		nil,
		func(Args) (any, error) {
			c, err := tr.RunTests(self)
			if err != nil {
				return nil, err
			}
			return testsResult(c), nil
		}}
}

// testsResult is run_tests' answer for checks c.
func testsResult(c model.Checks) string {
	var sb strings.Builder
	if c.Passed {
		fmt.Fprintf(&sb, "The tests passed (%.0fs): `%s`.", c.Duration, c.Command)
	} else {
		fmt.Fprintf(&sb, "The tests failed (exit %d, %.0fs): `%s`.", c.ExitCode, c.Duration, c.Command)
	}
	if c.Passed && c.Tree == "" {
		sb.WriteString(" Files changed while they ran, so Dash runs them again when you finish.")
	} else if c.Passed {
		sb.WriteString(" If you change no file now, Dash doesn't run them again when you finish.")
	}
	if out := strings.TrimSpace(c.Output); out != "" {
		fmt.Fprintf(&sb, "\n\nLast lines of output:\n```\n%s\n```", out)
	}
	return sb.String()
}

// activeStatuses are the statuses active_work lists, in its order.
var activeStatuses = []model.TodoStatus{model.StatusInProgress, model.StatusReview, model.StatusMerging, model.StatusQueued}

// Limits that keep active_work's answer short.
const (
	activeIntentLen   = 160 // characters of a task's intent
	activeTitleLen    = 80
	activeFilesShown  = 25 // files listed per task, then "+N more"
	activeFileWorkers = 8  // tasks whose files are found at once
)

// ActiveWork returns active_work: the project's other queued, running and
// in-review tasks, what each means to do and which files it has changed,
// optionally only those touching some paths.
func ActiveWork(b Board, o Options) Tool {
	pProject, project := projectParam(o)
	return Tool{ActiveWorkTool,
		"List what the project's other tasks are working on: each task that is queued, running, in review or merging " +
			"(not this one), with its id, status, title, intent (from its prompt) and the files it has changed so far " +
			"compared with the branch it lands on (a running task's uncommitted changes too). " +
			"It covers the whole project, including tasks a hub runs on its other runners. " +
			"Call it before larger edits to shared files; pass paths to see only the tasks touching them.",
		[]Param{pProject,
			{"paths", "array", "Only tasks that changed these files, or files under these directories (paths relative to the repository root).", false}},
		func(a Args) (any, error) {
			p, err := project(b, a)
			if err != nil {
				return nil, err
			}
			ts, err := b.Todos(p.ID)
			if err != nil {
				return nil, err
			}
			text, got, files := activeWork(b, ts, o.Todo, cleanPaths(a.List("paths")))
			if r, ok := b.(WorkRecorder); ok && o.Todo != "" {
				r.RecordActiveWork(o.Todo, got, files)
			}
			return text, nil
		}}
}

// WorkRecorder is a Board that records a task's active_work calls, so the
// board shows which tasks looked at which.
type WorkRecorder interface {
	// RecordActiveWork records that task self called active_work and got
	// back the tasks got, with the files listed for each (files[i]).
	RecordActiveWork(self string, got []model.Todo, files [][]string)
}

// activeWork is active_work's answer: one compact text block per task. It
// returns the tasks it lists too, with the files it lists for each.
func activeWork(b Board, ts []model.Todo, self string, paths []string) (text string, got []model.Todo, gotFiles [][]string) {
	var active []model.Todo
	for _, t := range ts {
		if t.ID != self && slices.Contains(activeStatuses, t.Status) {
			active = append(active, t)
		}
	}
	slices.SortStableFunc(active, func(x, y model.Todo) int {
		return slices.Index(activeStatuses, x.Status) - slices.Index(activeStatuses, y.Status)
	})
	files, errs := workFiles(b, active)

	var sb strings.Builder
	n := 0
	for i, t := range active {
		fs := files[i]
		if len(paths) > 0 {
			if fs = touching(fs, paths); len(fs) == 0 {
				continue
			}
		}
		n++
		got, gotFiles = append(got, t), append(gotFiles, fs)
		status := statusWord(t)
		fmt.Fprintf(&sb, "- %s [%s] %s\n", t.ID, status, textutil.OneLine(t.Name(activeTitleLen), activeTitleLen))
		if in := intent(t); in != "" {
			fmt.Fprintf(&sb, "  intent: %s\n", in)
		}
		switch {
		case errs[i] != nil:
			fmt.Fprintf(&sb, "  files: unknown (%s)\n", textutil.OneLine(errs[i].Error(), 100))
		case len(fs) == 0:
			sb.WriteString("  files: none yet\n")
		default:
			label := "files"
			if len(paths) > 0 {
				label = fmt.Sprintf("files (%d of %d match)", len(fs), len(files[i]))
			}
			fmt.Fprintf(&sb, "  %s: %s\n", label, fileList(fs))
		}
	}
	switch {
	case n == 0 && len(paths) > 0:
		return "No other active task in this project touches " + strings.Join(paths, ", ") + ".", nil, nil
	case n == 0:
		return "No other task in this project is queued, running or in review.", nil, nil
	}
	return strings.TrimRight(sb.String(), "\n"), got, gotFiles
}

// workFiles finds the files of every task in ts, a few at a time (on a hub
// each one asks a runner).
func workFiles(b Board, ts []model.Todo) ([][]string, []error) {
	files, errs := make([][]string, len(ts)), make([]error, len(ts))
	wf, ok := b.(WorkFiler)
	if !ok {
		return files, errs
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, activeFileWorkers)
	for i, t := range ts {
		if t.Worktree == nil && !t.OnRunner() {
			continue // not started: no files yet
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			files[i], errs[i] = wf.WorkFiles(t)
		}()
	}
	wg.Wait()
	return files, errs
}

func statusWord(t model.Todo) string {
	switch {
	case t.Status == model.StatusInProgress:
		return "running"
	case t.Status == model.StatusQueued && t.Paused:
		return "queued, paused"
	}
	return string(t.Status)
}

// intent is the start of t's prompt on one line, without the first line
// when that is what t is named by (its title, or with none, that line).
func intent(t model.Todo) string {
	p := strings.TrimSpace(t.Prompt)
	first := strings.TrimSpace(strings.TrimLeft(textutil.FirstLine(p), "#*- "))
	if strings.EqualFold(first, strings.TrimSpace(t.Name(len(first)+1))) {
		_, p, _ = strings.Cut(p, "\n")
	}
	return textutil.OneLine(p, activeIntentLen)
}

func fileList(fs []string) string {
	if len(fs) <= activeFilesShown {
		return strings.Join(fs, ", ")
	}
	return strings.Join(fs[:activeFilesShown], ", ") + fmt.Sprintf(" +%d more", len(fs)-activeFilesShown)
}

// cleanPaths makes the paths a call names comparable with git's: slashes,
// relative, no "./" or trailing "/".
func cleanPaths(ps []string) []string {
	var out []string
	for _, p := range ps {
		p = path.Clean(strings.ReplaceAll(p, `\`, "/"))
		p = strings.TrimPrefix(p, "/")
		if p == "." || p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// touching is the files in fs that are one of paths or under one of them.
func touching(fs, paths []string) []string {
	var out []string
	for _, f := range fs {
		for _, p := range paths {
			if f == p || strings.HasPrefix(f, p+"/") {
				out = append(out, f)
				break
			}
		}
	}
	return out
}
