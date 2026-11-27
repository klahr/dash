package tunnel

import (
	"slices"

	"github.com/klahr/dash/internal/model"
)

// Need is why work of a hub project for some members would wait for a
// runner: none of the org's runners can take it, or none that can is
// online. NeedNone: a runner that may take it is online.
type Need string

const (
	NeedNone Need = ""
	// NeedLabels: no runner of the project's org has the labels it runs
	// on: its labels (or the runners') are what to change.
	NeedLabels Need = "labels"
	// NeedRunner: runners have its labels, but none does these members'
	// work, and they have no runner of their own: they need one (Runners →
	// Add a runner), or a shared runner with its labels.
	NeedRunner Need = "runner"
	// NeedYourLabels: they have a runner of their own, but none with the
	// project's labels, and no shared runner has them either.
	NeedYourLabels Need = "your_labels"
	// NeedOnline: a runner may take the work, but none of those is online:
	// it waits until one is.
	NeedOnline Need = "online"
)

// RunnerNeed says whether one of list (the hub's runners) can take work of
// p for users (model.Todo.WorkFor; the viewer, for the board's warning):
// it serves p (Runner.Serves: p's org and labels, not a planner's) and does
// those users' work (Runner.WorksFor: their own, or a shared runner,
// another member's included). It only warns when no runner can ever take
// the work, or none that could is online; a busy or paused one still will.
func RunnerNeed(list []Runner, p model.Project, users []int64) Need {
	serving := Serving(list, p)
	if len(serving) == 0 {
		return NeedLabels
	}
	may := false
	for _, r := range serving {
		if ok, _ := r.WorksFor(users); !ok {
			continue
		}
		if r.Online {
			return NeedNone
		}
		may = true
	}
	if may {
		return NeedOnline
	}
	// Whether they have runners of their own that do work, with other labels.
	for _, r := range list {
		if r.OrgID() == p.OrgID && !r.ClaudeOnly() && r.OwnerID() > 0 && slices.Contains(users, r.OwnerID()) {
			return NeedYourLabels
		}
	}
	return NeedRunner
}
