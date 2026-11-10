package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// ErrFileChanged means a file isn't, on the default branch, what a change to
// it was made from.
var ErrFileChanged = errors.New("changed on the default branch since it was read")

// ErrUsesPR means a project lands work through pull requests only, so
// nothing is committed straight to its default branch.
var ErrUsesPR = errors.New("this project lands changes through pull requests")

// CommitFile commits content as path straight onto p's default branch, if
// the file there is still blob ("" for a file that isn't there yet), and
// returns the commit and the file's new blob. No task and no agent run: the
// commit is made with plumbing on top of the branch (no checkout), then
// lands the way merges do, on the merge worker, after the merges queued
// before it: synced with origin first when the project pushes, pushed before
// the branch moves in a project shared through origin, and pushed after it
// with auto-push. A project in pull request mode gets ErrUsesPR.
func (m *MergeQueue) CommitFile(ctx context.Context, p model.Project, path, blob, content, msg string) (sha, newBlob string, err error) {
	if p.UsesPR() {
		return "", "", ErrUsesPR
	}
	if derr := m.Do(ctx, func(c context.Context) { sha, newBlob, err = m.commitFile(c, p, path, blob, content, msg) }); derr != nil {
		return "", "", derr
	}
	return sha, newBlob, err
}

func (m *MergeQueue) commitFile(ctx context.Context, p model.Project, path, blob, content, msg string) (string, string, error) {
	for attempt := 1; ; attempt++ {
		p = m.refreshRemote(ctx, p)
		if p.SyncsRemote() {
			if err := m.syncRemote(ctx, p); err != nil {
				return "", "", err
			}
		}
		sha, nb, err := m.landFile(ctx, p, path, blob, content, msg)
		var moved *originMoved
		if errors.As(err, &moved) && attempt < maxLandAttempts {
			continue // origin moved before the push: sync and check the file again
		}
		if err != nil {
			return "", "", err
		}
		if !p.LandsOnOrigin() && p.AutoPush && p.RemoteURL != "" {
			if err := m.pushSynced(ctx, p); err != nil {
				slog.Warn("push after committing a file", "project", p.Name, "file", path, "err", err)
			}
		}
		return sha, nb, nil
	}
}

// landFile commits path on top of the default branch and moves the branch
// there (pushing it first in a project shared through origin).
func (m *MergeQueue) landFile(ctx context.Context, p model.Project, path, blob, content, msg string) (string, string, error) {
	unlock, err := m.Remote.Lock(ctx, p.ID)
	if err != nil {
		return "", "", err
	}
	defer unlock()
	if err := m.checkoutClean(ctx, p); err != nil {
		return "", "", err
	}
	old, err := m.Git.Head(ctx, p.ID, p.Path, p.Base())
	if err != nil {
		return "", "", err
	}
	if cur, _ := m.Git.Blob(ctx, p.ID, p.Path, old, path); cur != blob {
		return "", "", fmt.Errorf("%s %w", path, ErrFileChanged)
	}
	sha, nb, err := m.Git.CommitFile(ctx, p.ID, p.Path, old, path, content, msg)
	if err != nil {
		return "", "", err
	}
	if p.LandsOnOrigin() {
		if err := m.Git.PushCommit(ctx, p.ID, p.Path, sha, p.Base()); err != nil {
			if m.originMovedFrom(ctx, p, old) {
				return "", "", &originMoved{branch: p.Base()}
			}
			m.setSyncError(p.ID, err)
			return "", "", &syncError{fmt.Errorf("can't push %s to GitHub: %w", p.Base(), err)}
		}
		m.setSyncError(p.ID, nil)
		m.pushed(p, p.Base(), sha)
		if err := m.Git.Advance(ctx, p.ID, p.Path, p.Base(), old, sha); err != nil {
			slog.Warn("move the default branch after pushing", "project", p.Name, "err", err)
		}
	} else if err := m.Git.Advance(ctx, p.ID, p.Path, p.Base(), old, sha); err != nil {
		var dirty *gitops.ErrDirtyCheckout
		if errors.As(err, &dirty) {
			return "", "", fmt.Errorf("commit or stash your changes to %s in %s first: %w", path, p.Path, err)
		}
		return "", "", err
	}
	m.Remote.recount(ctx, p)
	return sha, nb, nil
}
