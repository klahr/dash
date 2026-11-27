package queue

import (
	"context"
	"errors"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/screenshot"
	"github.com/klahr/dash/internal/store"
)

// Before/after screenshots of a finished task (TodoEngine.Screenshots): once
// its work is handed off, they are taken in the background, alongside the
// merge queue, a pull request or Review, and never hold the task up. A new
// run of the task stops a capture still going.

// shotJob is a capture going on for a task.
type shotJob struct{ cancel context.CancelFunc }

// errStaleShots: the task ran again since the capture started.
var errStaleShots = errors.New("the task ran again")

// screenshotsWanted reports whether a task of p gets the screenshot hint:
// this Dash takes screenshots and p is a web project that wants them.
func (e *TodoEngine) screenshotsWanted(p model.Project) bool {
	return e.Screenshots != nil && p.ScreenshotsOn() && p.RunCommand != "" && !p.TerminalPreview()
}

// stopScreenshots stops the capture going on for task id, if any.
func (e *TodoEngine) stopScreenshots(id string) {
	e.mu.Lock()
	j := e.shots[id]
	delete(e.shots, id)
	e.mu.Unlock()
	if j != nil {
		j.cancel()
	}
}

// startScreenshots decides whether the task's run gets screenshots
// (screenshot.Decide) and, if so, starts taking them. What it decides is on
// the task at once: none, skipped with a note, or being captured.
func (r *todoRun) startScreenshots() {
	e, t, p := r.e, r.t, r.p
	c := e.Screenshots
	if c == nil || t.Worktree == nil {
		return
	}
	listed := screenshot.ParsePages(r.report)
	if len(listed) == 0 && t.Screenshots != nil {
		listed = t.Screenshots.Listed // a test fix's report lists none: the pages are the same
	}
	var changed []string
	var base, chrome string
	if p.ScreenshotsOn() {
		if b, err := e.Git.MergeBase(r.ctx, p.ID, p.Path, p.For(t).Base(), r.wt.Branch); err == nil {
			base = b
			changed, _ = e.Git.ChangedFiles(r.ctx, p.ID, r.wt.Path, base, "HEAD")
		}
		chrome = c.ChromePath()
	}
	d := screenshot.Decide(p, changed, listed, chrome)
	env, err := e.Shared.shellEnv(t)
	if d.Run && err != nil {
		d = screenshot.Decision{Pages: d.Pages, Note: "No screenshots: " + err.Error()}
	}
	run := t.Runs
	if !d.Run {
		var set *model.ScreenshotSet
		if d.Note != "" {
			set = &model.ScreenshotSet{State: model.ShotsSkipped, Note: d.Note, Listed: listed, Run: run, At: time.Now()}
			r.log("📸 " + d.Note)
		}
		if t.Screenshots != nil || set != nil {
			_ = r.update(func(x *model.Todo) { x.Screenshots = set })
			c.Remove(t.ID)
		}
		return
	}
	pending := &model.ScreenshotSet{State: model.ShotsCapturing, Listed: listed, Run: run, At: time.Now()}
	for _, pg := range d.Pages {
		pending.Pages = append(pending.Pages, model.ShotPage{ScreenshotPage: pg})
	}
	_ = r.update(func(x *model.Todo) { x.Screenshots = pending })

	parent := e.runCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	job := &shotJob{cancel: cancel}
	e.mu.Lock()
	if old := e.shots[t.ID]; old != nil {
		old.cancel()
	}
	e.shots[t.ID] = job
	e.mu.Unlock()
	log := func(s string) { e.Log.Line(t, s) }
	go func() {
		defer func() {
			e.mu.Lock()
			if e.shots[t.ID] == job {
				delete(e.shots, t.ID)
			}
			e.mu.Unlock()
			cancel()
		}()
		set := c.Capture(ctx, screenshot.Job{
			Project: p, TodoID: t.ID, Run: run, Dir: r.wt.Path, Base: base, Pages: d.Pages, Env: env, Log: log,
		})
		set.Listed = listed
		_, err := e.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
			if x.Runs != run {
				return errStaleShots
			}
			x.Screenshots = &set
			return nil
		})
		if errors.Is(err, store.ErrNotFound) {
			c.Remove(t.ID) // deleted while they were taken
		}
	}()
}
