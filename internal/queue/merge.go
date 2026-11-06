package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/store"
)

var ErrMergeQueueFull = errors.New("merge queue is full")

// MergeQueue lands approved todos on the default branch one at a time. A
// single goroutine drains the channel, so there are never two merges in
// flight and the main checkout is never touched concurrently.
//
// Per job:
//  1. merge the default branch into the todo's worktree. A conflict sends
//     the todo back to Claude to resolve (auto-fix), then it returns here
//     without another review.
//  2. if that brought in new commits, run the tests again; failures go back
//     to Claude the same way
//  3. squash the branch onto the default branch: one commit per todo
type MergeQueue struct {
	Store       *store.Store
	Git         *gitops.Git
	Pub         Publisher
	Log         *TodoLog
	Perms       *perm.Broker
	Kick        func() // wake the todo engine after sending a todo back
	TestTimeout time.Duration

	jobs    chan model.MergeJob
	pending atomic.Int64
}

func NewMergeQueue(s *store.Store, g *gitops.Git, pub Publisher, log *TodoLog, capacity int) *MergeQueue {
	return &MergeQueue{Store: s, Git: g, Pub: pub, Log: log, TestTimeout: 20 * time.Minute, jobs: make(chan model.MergeJob, capacity)}
}

// Enqueue adds a job without blocking the caller (an HTTP handler).
func (m *MergeQueue) Enqueue(job model.MergeJob) error {
	select {
	case m.jobs <- job:
		m.pending.Add(1)
		m.publish("")
		return nil
	default:
		return ErrMergeQueueFull
	}
}

func (m *MergeQueue) Pending() int { return int(m.pending.Load()) }

// Run is the single merge worker. Start exactly one.
func (m *MergeQueue) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-m.jobs:
			m.publish(job.TodoID)
			if err := m.process(ctx, job); err != nil {
				slog.Warn("merge failed", "todo", job.TodoID, "err", err)
				if t, gerr := m.Store.GetTodo(job.TodoID); gerr == nil {
					m.Log.Line(t, "✖ merge: "+err.Error())
				}
				_, _ = m.Store.SetStatus(job.TodoID, model.StatusFailed, "merge: "+err.Error())
			}
			m.pending.Add(-1)
			m.publish("")
		}
	}
}

func (m *MergeQueue) process(ctx context.Context, job model.MergeJob) error {
	t, err := m.Store.GetTodo(job.TodoID)
	if err != nil {
		return err
	}
	if t.Status != model.StatusMerging || t.Worktree == nil {
		return fmt.Errorf("todo %s not mergeable (status %s)", t.ID, t.Status)
	}
	p, err := m.Store.GetProject(t.ProjectID)
	if err != nil {
		return err
	}
	wt := *t.Worktree
	log := func(s string) { m.Log.Line(t, s) }
	log("▶ merging into " + p.DefaultBranch)

	// 1. Bring the default branch into the todo's branch.
	changed, err := m.Git.MergeIn(ctx, p.ID, wt.Path, p.DefaultBranch)
	var conflict *gitops.ErrConflict
	if errors.As(err, &conflict) {
		log("⚠ " + conflict.Error())
		return m.sendBack(t, p, "", true, "conflicts with "+p.DefaultBranch+" in "+strings.Join(conflict.Files, ", "))
	}
	if err != nil {
		return err
	}

	// 2. Re-test if the default branch moved since the todo's tests ran.
	if changed && p.TestCommand != "" {
		checks := runChecks(ctx, m.Log, t, p, wt.Path, "merge", m.TestTimeout)
		if t, err = m.Store.UpdateTodo(t.ID, func(x *model.Todo) error { x.Checks = &checks; return nil }); err != nil {
			return err
		}
		if !checks.Passed {
			note := fmt.Sprintf("Your change was merged with the latest `%s`, and now the tests fail. %s", p.DefaultBranch, testNote(checks))
			return m.sendBack(t, p, note, false, "tests fail after merging "+p.DefaultBranch)
		}
	}

	// 3. Land it.
	if _, err := m.Git.SquashInto(ctx, p.ID, p.Path, wt.Branch, commitMessage(t)); err != nil {
		return fmt.Errorf("squash onto %s (is the main checkout clean and on %s?): %w", p.DefaultBranch, p.DefaultBranch, err)
	}
	if _, err := m.Store.SetStatus(t.ID, model.StatusMerged, ""); err != nil {
		return err
	}
	log("✔ merged into " + p.DefaultBranch)
	if err := m.Git.RemoveWorktree(ctx, p.ID, p.Path, wt); err != nil {
		slog.Warn("remove worktree", "todo", t.ID, "err", err)
	}
	if m.Perms != nil {
		m.Perms.Forget(t.ID)
	}
	if p.AutoPush && p.RemoteURL != "" {
		if err := m.Git.Push(ctx, p.ID, p.Path, p.DefaultBranch); err != nil {
			slog.Warn("auto-push", "project", p.ID, "err", err) // merged locally; push can be retried
		}
	}
	return nil
}

// sendBack returns an approved todo to Claude to fix a conflict or failing
// tests. Once fixed it goes straight back to the merge queue (AutoMerge).
// Without fixes left it fails and waits for a human.
func (m *MergeQueue) sendBack(t model.Todo, p model.Project, note string, mergeMain bool, why string) error {
	if t.AutoFixes >= p.MaxAutoFixes {
		_, err := m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
			if note != "" {
				x.Feedback = append(x.Feedback, note)
			}
			x.MergeMain = x.MergeMain || mergeMain // a rework will resolve it
			x.Status = model.StatusFailed
			x.Error = why + " — send it back for rework to let Claude fix it"
			return nil
		})
		return err
	}
	m.Log.Line(t, fmt.Sprintf("↻ %s; sending back to Claude (fix %d of %d)", why, t.AutoFixes+1, p.MaxAutoFixes))
	_, err := m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if note != "" {
			x.Feedback = append(x.Feedback, note)
		}
		x.MergeMain = x.MergeMain || mergeMain
		x.AutoMerge = true
		x.AutoFixes++
		x.Status = model.StatusQueued
		x.Error = ""
		return nil
	})
	if err == nil && m.Kick != nil {
		m.Kick()
	}
	return err
}

func commitMessage(t model.Todo) string {
	subject := t.Title
	if subject == "" {
		subject = firstLine(t.Prompt)
	}
	body := strings.TrimSpace(t.Prompt)
	if len(body) > 2000 {
		body = body[:2000] + "…"
	}
	return fmt.Sprintf("%s\n\n%s\n\nDash-Todo: %s", subject, body, t.ID)
}

func (m *MergeQueue) publish(current string) {
	m.Pub.Publish(model.Event{Type: model.EvMergeQueue, Time: time.Now(), Data: map[string]any{
		"pending": m.pending.Load(), "current": current,
	}})
}
