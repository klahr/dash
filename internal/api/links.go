package api

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
)

// RecordActiveWork records task self's active_work call that returned got:
// a LinkActiveWork on self for each task it got (Out), one on each of them
// from self, and "👀 Checked other work: #12, #15" in self's run log.
func (b board) RecordActiveWork(self string, got []model.Todo, files [][]string) {
	b.sv.recordActiveWork(self, got, files, time.Now())
}

// RecordActiveWork records an active_work call of the run's task.
func (pb projectBoard) RecordActiveWork(self string, got []model.Todo, files [][]string) {
	if _, err := pb.todo(self); err != nil {
		return
	}
	pb.b.RecordActiveWork(self, got, files)
}

func (sv *service) recordActiveWork(self string, got []model.Todo, files [][]string, now time.Time) {
	me, err := sv.data.UpdateTodo(self, func(x *model.Todo) error {
		for i, o := range got {
			x.Links = model.AddLink(x.Links, model.Link{Kind: model.LinkActiveWork, Out: true,
				TodoID: o.ID, HubTodoID: o.HubTodoID, Name: o.Name(72), Files: files[i], At: now})
		}
		return nil
	})
	if err != nil {
		slog.Warn("record an active_work call", "todo", self, "err", err)
		return
	}
	for i, o := range got {
		if _, err := sv.data.UpdateTodo(o.ID, func(x *model.Todo) error {
			x.Links = model.AddLink(x.Links, model.Link{Kind: model.LinkActiveWork,
				TodoID: me.ID, HubTodoID: me.HubTodoID, Name: me.Name(72), Files: files[i], At: now})
			return nil
		}); err != nil {
			slog.Warn("record an active_work call", "todo", o.ID, "err", err)
		}
	}
	if l := sv.s.Todos; l != nil && l.Log != nil && l.Log.Pub != nil {
		l.Log.Event(me, activeWorkEvent(got, now))
	}
}

// activeWorkEvent is the run log's entry for an active_work call that
// returned got: "👀 Checked other work: #12, #15", linked to each.
func activeWorkEvent(got []model.Todo, at time.Time) model.RunEvent {
	if len(got) == 0 {
		return model.RunEvent{Kind: "status", Text: "👀 Checked other work: none found", At: at}
	}
	ids := make([]string, len(got))
	refs := make([]model.TaskRef, len(got))
	for i, o := range got {
		ids[i] = "#" + o.ID
		refs[i] = model.TaskRef{ID: o.ID, HubID: o.HubTodoID, Name: o.Name(72)}
	}
	return model.RunEvent{Kind: "status", Text: "👀 Checked other work: " + strings.Join(ids, ", "), Tasks: refs, At: at}
}

// withHubNotes adds to a runner's events of task id (its JSON, b) what the
// hub notes in its own log about the task: what its goal's coordinator did
// (see goalTimeline) and its active_work calls, which the hub answers (see
// recordActiveWork). In time order; ok is false when b can't be read.
func (s *Server) withHubNotes(id string, b []byte) ([]model.RunEvent, bool) {
	var evs []model.RunEvent
	if json.Unmarshal(b, &evs) != nil {
		return nil, false
	}
	local, _ := s.Todos.Log.Events(id)
	added := false
	for _, ev := range local {
		if ev.Kind == "status" && (strings.HasPrefix(ev.Text, model.GoalTimeline) || strings.HasPrefix(ev.Text, "👀 ")) {
			evs, added = append(evs, ev), true
		}
	}
	if added {
		slices.SortStableFunc(evs, func(a, b model.RunEvent) int { return a.At.Compare(b.At) })
	}
	return nonNil(evs), true
}
