package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

var ErrMergeQueueFull = errors.New("merge queue is full")

// MergeQueue lands approved todos on the default branch one at a time. A
// single goroutine drains the channel, so there are never two git merges in
// flight and the main checkout is never touched concurrently.
//
// Strategy per job: rebase the todo branch onto the default branch inside its
// own worktree (conflicts abort and fail the job without touching main), then
// fast-forward the default branch. History stays linear.
type MergeQueue struct {
	Store *store.Store
	Git   *gitops.Git
	Pub   Publisher

	jobs    chan model.MergeJob
	pending atomic.Int64
}

func NewMergeQueue(s *store.Store, g *gitops.Git, pub Publisher, capacity int) *MergeQueue {
	return &MergeQueue{Store: s, Git: g, Pub: pub, jobs: make(chan model.MergeJob, capacity)}
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
				_, _ = m.Store.SetStatus(job.TodoID, model.StatusFailed, "merge: "+err.Error())
			}
			m.pending.Add(-1)
			m.publish("")
		}
	}
}

func (m *MergeQueue) process(ctx context.Context, job model.MergeJob) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

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

	if err := m.Git.Rebase(ctx, p.ID, wt.Path, p.DefaultBranch); err != nil {
		return fmt.Errorf("conflicts with %s — send back for rework: %w", p.DefaultBranch, err)
	}
	if err := m.Git.FastForward(ctx, p.ID, p.Path, wt.Branch); err != nil {
		return fmt.Errorf("fast-forward %s (is the main checkout clean and on %s?): %w", p.DefaultBranch, p.DefaultBranch, err)
	}
	if _, err := m.Store.SetStatus(t.ID, model.StatusMerged, ""); err != nil {
		return err
	}
	if err := m.Git.RemoveWorktree(ctx, p.ID, p.Path, wt); err != nil {
		slog.Warn("remove worktree", "todo", t.ID, "err", err)
	}
	if p.AutoPush && p.RemoteURL != "" {
		if err := m.Git.Push(ctx, p.ID, p.Path, p.DefaultBranch); err != nil {
			slog.Warn("auto-push", "project", p.ID, "err", err) // merged locally; push can be retried
		}
	}
	return nil
}

func (m *MergeQueue) publish(current string) {
	m.Pub.Publish(model.Event{Type: model.EvMergeQueue, Time: time.Now(), Data: map[string]any{
		"pending": m.pending.Load(), "current": current,
	}})
}
