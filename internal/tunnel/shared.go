package tunnel

import (
	"errors"
	"slices"

	"github.com/klahr/dash/internal/claudecli"
)

// Whose work a runner does. A runner runs work on its owner's computer, as
// its owner, and Claude there is signed in with its owner's login. So a
// runner does only its owner's work (the tasks they created or started,
// their scans and actions), unless it is a shared runner: one whose owner or
// an org admin turned on Take other members' tasks (Runner.Shared) and that
// reports an API key in ANTHROPIC_API_KEY and no subscription login
// (claudecli.Auth.APIKeyOnly). A subscription (Pro, Max, Team or
// Enterprise) is one person's and is never shared. The runner checks the
// same again for every start of work (see the api package's hubShared).

// ErrNotAPIKey is why Take other members' tasks can't be turned on.
var ErrNotAPIKey = errors.New("only a runner that runs Claude with an API key, and has no Claude subscription login, can take other members' tasks")

// Auth is how the runner last said its Claude Code signs in (nil: it
// hasn't, or is too old to say).
func (r Runner) Auth() *claudecli.Auth {
	if r.Reported == nil || r.Reported.Claude == nil {
		return nil
	}
	return r.Reported.Claude.Auth
}

// OwnerID is the user who registered r (0: unknown).
func (r Runner) OwnerID() int64 {
	if r.Owner == nil {
		return 0
	}
	return r.Owner.UserID
}

// ownerName is r's owner's username, for messages.
func (r Runner) ownerName() string {
	if r.Owner == nil || r.Owner.Username == "" {
		return "its owner"
	}
	return r.Owner.Username
}

// TakesOthers reports whether r does other members' work now: Shared is on
// and it reports an API key and no subscription login. Otherwise it says
// why not ("" when it isn't Shared).
func (r Runner) TakesOthers() (bool, string) {
	if !r.Shared {
		return false, ""
	}
	if why := r.Auth().NotShared(); why != "" {
		return false, why
	}
	return true, ""
}

// WorksFor reports whether r may do work for users (model.Todo.WorkFor):
// all of them are its owner, or it takes other members' work. Otherwise it
// says why not, e.g. "laptop is bob's runner and only takes bob's tasks".
func (r Runner) WorksFor(users []int64) (bool, string) {
	owner := r.OwnerID()
	if owner > 0 && len(users) > 0 && !slices.ContainsFunc(users, func(u int64) bool { return u != owner }) {
		return true, ""
	}
	ok, why := r.TakesOthers()
	if ok {
		return true, ""
	}
	if why != "" {
		return false, r.Name + " takes other members' tasks, but " + why
	}
	return false, r.Name + " is " + r.ownerName() + "'s runner and only takes " + r.ownerName() + "'s tasks"
}

// CanShare says why runner r can't be made shared ("" when it can).
func (r Runner) CanShare() string {
	if why := r.Auth().NotShared(); why != "" {
		return "it can't take other members' tasks: " + why
	}
	return ""
}

// SetShared turns runner id's Take other members' tasks on or off. On is
// refused (ErrNotAPIKey) unless the runner reports an API key and no
// subscription login.
func (h *Hub) SetShared(id string, on bool) error {
	if on {
		r, ok := h.Get(id)
		if !ok {
			return ErrUnknownName
		}
		if why := r.CanShare(); why != "" {
			return errors.Join(ErrNotAPIKey, errors.New(r.Name+": "+why))
		}
	}
	return h.settleShared(id, on)
}

// settleShared sets runner id's Shared and marks it settled (see
// MigrateShared).
func (h *Hub) settleShared(id string, on bool) error {
	h.mu.Lock()
	i := slices.IndexFunc(h.runners, func(r *record) bool { return r.ID == id })
	if i < 0 {
		h.mu.Unlock()
		return ErrUnknownName
	}
	h.runners[i].Shared, h.runners[i].SharedSet = on, true
	h.saveLocked()
	h.mu.Unlock()
	h.changed()
	return nil
}

// Unsettled are the runners registered before Take other members' tasks
// whose setting hasn't been settled yet (see MigrateShared).
func (h *Hub) Unsettled() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var ids []string
	for _, r := range h.runners {
		if !r.SharedSet {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// MigrateShared settles, once, whether runner id, registered before runners
// did only their owner's work, takes other members' tasks: on if it reports
// an API key and no subscription login and others' tasks (othersWork) went
// to it, off otherwise. It waits (returns false) until the runner has said
// how Claude signs in; a runner too old to say stays unsettled, and so not
// shared. It returns whether it settled it, and how.
func (h *Hub) MigrateShared(id string, othersWork func(Runner) bool) (settled, on bool, err error) {
	r, ok := h.Get(id)
	if !ok {
		return false, false, ErrUnknownName
	}
	if r.Auth() == nil {
		return false, false, nil
	}
	h.mu.Lock()
	i := slices.IndexFunc(h.runners, func(x *record) bool { return x.ID == id })
	set := i >= 0 && h.runners[i].SharedSet
	h.mu.Unlock()
	if set {
		return false, r.Shared, nil
	}
	on = r.Auth().APIKeyOnly() && othersWork(r)
	return true, on, h.settleShared(id, on)
}
