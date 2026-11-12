package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
)

// A runner updates itself when its owner or an org admin asks (or by
// itself, with AutoUpdate, when it connects running another version than
// the hub's release). It is then in maintenance mode (Runner.Update): the
// hub sends it no new tasks, its running ones finish, and once it is idle
// the hub sends it UpdatePath. The runner downloads and installs the
// release and restarts on it. Maintenance survives the runner's (and the
// hub's) restarts: it ends when the runner connects running the release,
// or when the update fails (Runner.UpdateError says why).

// UpdatePath is where the hub asks a runner to update itself, with
// {"version": "<release>"}. The runner answers 409 while it still has
// tasks running.
const UpdatePath = "/api/hub/update"

// Phases of an update.
const (
	UpdateWaiting  = "waiting"  // for the runner's tasks to finish
	UpdateUpdating = "updating" // the runner was told to update
)

// Update is a runner's update in progress: it is in maintenance mode.
type Update struct {
	Target string    `json:"target"`
	Phase  string    `json:"phase"`
	Auto   bool      `json:"auto,omitempty"` // started by auto-update, not a person
	Since  time.Time `json:"since"`
}

// UpdateResult is how a runner's last update ended.
type UpdateResult struct {
	Target    string    `json:"target"`
	Error     string    `json:"error,omitempty"`
	Cancelled bool      `json:"cancelled,omitempty"`
	At        time.Time `json:"at"`
}

// held says whether auto-update leaves the runner alone for release: its
// update to it failed or was cancelled.
func (u *UpdateResult) held(release string) bool {
	return u != nil && (u.Error != "" || u.Cancelled) && sameVersion(u.Target, release)
}

// Errors of StartUpdate and CancelUpdate.
var (
	ErrNoRelease = errors.New("this hub was built from source: there is no release to update runners to")
	ErrUpToDate  = errors.New("the runner already runs the hub's version")
	ErrNoUpdate  = errors.New("the runner isn't waiting to update")
	ErrUpdating  = errors.New("the runner is updating now; it can't be cancelled any more")
)

func sameVersion(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

func (h *Hub) recordLocked(id string) *record {
	i := slices.IndexFunc(h.runners, func(r *record) bool { return r.ID == id })
	if i < 0 {
		return nil
	}
	return h.runners[i]
}

// StartUpdate puts runner id in maintenance mode to update it to the hub's
// release once its tasks are done. Asking again while it waits is fine.
func (h *Hub) StartUpdate(id string) error {
	h.mu.Lock()
	r := h.recordLocked(id)
	switch {
	case r == nil:
		h.mu.Unlock()
		return ErrUnknownName
	case h.Release == "":
		h.mu.Unlock()
		return ErrNoRelease
	case r.Update != nil:
		h.mu.Unlock()
		return nil
	case r.Version != "" && sameVersion(r.Version, h.Release):
		h.mu.Unlock()
		return ErrUpToDate
	}
	h.startUpdateLocked(r, false)
	h.saveLocked()
	h.mu.Unlock()
	h.changed()
	return nil
}

func (h *Hub) startUpdateLocked(r *record, auto bool) {
	r.Update = &Update{Target: h.Release, Phase: UpdateWaiting, Auto: auto, Since: h.clock()}
	r.LastUpdate = nil
	slog.Info("runner: update requested", "name", r.Name, "from", r.Version, "to", h.Release, "auto", auto)
}

// CancelUpdate takes runner id out of maintenance mode while it is still
// waiting for its tasks to finish.
func (h *Hub) CancelUpdate(id string) error {
	h.mu.Lock()
	r := h.recordLocked(id)
	switch {
	case r == nil:
		h.mu.Unlock()
		return ErrUnknownName
	case r.Update == nil:
		h.mu.Unlock()
		return ErrNoUpdate
	case r.Update.Phase != UpdateWaiting:
		h.mu.Unlock()
		return ErrUpdating
	}
	// Cancelled by hand: auto-update doesn't start it again for this release.
	r.LastUpdate = &UpdateResult{Target: r.Update.Target, Cancelled: true, At: h.clock()}
	r.Update = nil
	h.saveLocked()
	h.mu.Unlock()
	h.changed()
	return nil
}

// SetAutoUpdate turns runner id's auto-update on or off. Turned on for a
// runner on another version than the hub's, its update starts.
func (h *Hub) SetAutoUpdate(id string, on bool) error {
	h.mu.Lock()
	r := h.recordLocked(id)
	if r == nil {
		h.mu.Unlock()
		return ErrUnknownName
	}
	r.AutoUpdate = on
	if on && r.Update == nil && r.Version != "" && h.Release != "" && !sameVersion(r.Version, h.Release) {
		h.startUpdateLocked(r, true)
	}
	h.saveLocked()
	h.mu.Unlock()
	h.changed()
	return nil
}

// connectedLocked settles runner r's update now that it connected running
// version: done when it runs the target; failed when it was told to
// update but runs something else (the update was cut off, or the new
// version failed to start and the runner put the old one back). With
// auto-update, a runner on another version than the hub's starts updating.
func (h *Hub) connectedLocked(r *record, version string) {
	now := h.clock()
	if u := r.Update; u != nil {
		switch {
		case sameVersion(version, u.Target):
			slog.Info("runner: updated", "name", r.Name, "version", version)
			r.Update, r.LastUpdate = nil, &UpdateResult{Target: u.Target, At: now}
		case u.Phase == UpdateUpdating:
			msg := fmt.Sprintf("came back running %s, not %s: the update was cut off, or the new version failed to start and the previous one was put back", version, u.Target)
			slog.Warn("runner: update failed", "name", r.Name, "err", msg)
			r.Update, r.LastUpdate = nil, &UpdateResult{Target: u.Target, Error: msg, At: now}
		case h.Release == "":
			r.Update, r.LastUpdate = nil, &UpdateResult{Target: u.Target, Error: ErrNoRelease.Error(), At: now}
		default:
			u.Target = h.Release // the hub was upgraded meanwhile
		}
	}
	if r.Update == nil && h.Release != "" && sameVersion(version, h.Release) && r.LastUpdate != nil && r.LastUpdate.Error != "" {
		r.LastUpdate = nil // updated some other way: the failure is past
	}
	if r.Update == nil && r.AutoUpdate && h.Release != "" && !sameVersion(version, h.Release) && !r.LastUpdate.held(h.Release) {
		h.startUpdateLocked(r, true)
	}
}

// beginUpdate moves runner id's update from waiting to updating, and
// returns its target (false: it isn't waiting).
func (h *Hub) beginUpdate(id string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.recordLocked(id)
	if r == nil || r.Update == nil || r.Update.Phase != UpdateWaiting {
		return "", false
	}
	r.Update.Phase = UpdateUpdating
	h.saveLocked()
	go h.changed()
	return r.Update.Target, true
}

// endUpdate settles runner id's update after the hub told it to: back to
// waiting (retry), or failed with msg.
func (h *Hub) endUpdate(id string, retry bool, msg string) {
	h.mu.Lock()
	r := h.recordLocked(id)
	if r == nil || r.Update == nil || r.Update.Phase != UpdateUpdating {
		h.mu.Unlock()
		return
	}
	if retry {
		r.Update.Phase = UpdateWaiting
	} else {
		slog.Warn("runner: update failed", "name", r.Name, "err", msg)
		r.LastUpdate = &UpdateResult{Target: r.Update.Target, Error: msg, At: h.clock()}
		r.Update = nil
	}
	h.saveLocked()
	h.mu.Unlock()
	h.changed()
}

// RunUpdates tells runners in maintenance mode to update once they are
// online and idle (busy says how many of the hub's tasks runner id is
// working on), looking every interval until ctx ends.
func (h *Hub) RunUpdates(ctx context.Context, every time.Duration, busy func(id string) int) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		h.UpdateIdle(ctx, busy)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// UpdateIdle is one look of RunUpdates. The updates it starts run in the
// background.
func (h *Hub) UpdateIdle(ctx context.Context, busy func(id string) int) {
	h.mu.Lock()
	var ids []string
	for _, r := range h.runners {
		if _, online := h.live[r.ID]; online && r.Update != nil && r.Update.Phase == UpdateWaiting {
			ids = append(ids, r.ID)
		}
	}
	h.mu.Unlock()
	for _, id := range ids {
		if busy(id) > 0 {
			continue
		}
		if target, ok := h.beginUpdate(id); ok {
			go h.sendUpdate(ctx, id, target)
		}
	}
}

// updateTimeout bounds the runner's download, checks and swap.
const updateTimeout = 10 * time.Minute

// sendUpdate tells runner id to update to target. Once it answers, it
// restarts: its update ends when it connects again.
func (h *Hub) sendUpdate(ctx context.Context, id, target string) {
	cctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()
	err := h.Call(cctx, id, http.MethodPost, UpdatePath, map[string]string{"version": target}, nil)
	var ce *CallError
	switch {
	case err == nil:
		// Restarting on the new version; connecting settles it.
	case errors.As(err, &ce) && ce.Status == http.StatusConflict:
		h.endUpdate(id, true, "") // a task came in after all: wait for it
	case errors.As(err, &ce) && (ce.Status == http.StatusForbidden || ce.Status == http.StatusNotFound):
		h.endUpdate(id, false, "its Dash is too old to update itself: run the install command on it once")
	case errors.As(err, &ce):
		h.endUpdate(id, false, ce.Msg)
	case ctx.Err() != nil:
		// The hub is stopping: Keep sets the update waiting again.
	default:
		// The tunnel dropped: try again once it is back.
		h.endUpdate(id, true, "")
	}
}
