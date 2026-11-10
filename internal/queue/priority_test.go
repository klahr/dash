package queue

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// The dispatcher takes prioritised tasks first, lower priority first and
// oldest first among equals, then unprioritised ones oldest first: a P1 task
// doesn't wait behind older tasks that have no priority.
func TestDispatchOrderPrioritisedFirstThenFIFO(t *testing.T) {
	parallel(t)

	e, st, rem, _ := remoteEngine(t)
	p, _ := st.CreateProject(model.Project{Name: "shared", RemoteURL: "https://x/y", Runners: []string{"laptop"}})
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ids := map[string]string{}
	for i, c := range []struct {
		name string
		prio *int
	}{
		{"unset-old", nil}, {"p3", new(3)}, {"unset-new", nil}, {"p1-old", new(1)}, {"p0", new(0)}, {"p1-new", new(1)},
	} {
		td := queued(t, st, p)
		if _, err := st.UpdateTodo(td.ID, func(td *model.Todo) error {
			td.Priority, td.CreatedAt = c.prio, base.Add(time.Duration(i)*time.Minute)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		ids[td.ID] = c.name
	}

	e.dispatch(context.Background())

	var got []string
	for _, id := range rem.sent {
		got = append(got, ids[id])
	}
	if want := []string{"p0", "p1-old", "p1-new", "p3", "unset-old", "unset-new"}; !slices.Equal(got, want) {
		t.Errorf("dispatched %v, want %v", got, want)
	}
}
