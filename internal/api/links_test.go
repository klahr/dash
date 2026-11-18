package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// A task's active_work call is recorded on both sides: the caller gets a
// link to each task it got back, each of them one from the caller, and the
// caller's run log a "👀 Checked other work" entry linked to them.
func TestActiveWorkIsRecorded(t *testing.T) {
	s, _, p := boardServer(t)
	s.Todos.Log.Pub = s.Hub
	self := withStatus(t, s, p.ID, model.StatusInProgress)
	other := withStatus(t, s, p.ID, model.StatusReview)
	done := withStatus(t, s, p.ID, model.StatusFailed) // not active: not listed, not linked

	token, release := s.Board.RegisterTask(testOwner(t, s), p.ID, self.ID)
	defer release()
	h := s.Routes()
	for range 2 { // asked again: still one link each way
		if text, isErr := call(t, h, token, "active_work", `{}`); isErr || !strings.Contains(text, other.ID) {
			t.Fatalf("active_work: %s", text)
		}
	}
	get := func(id string) model.Todo {
		t.Helper()
		td, err := raw(s).GetTodo(id)
		if err != nil {
			t.Fatal(err)
		}
		return td
	}
	if ls := get(self.ID).Links; len(ls) != 1 || ls[0].Kind != model.LinkActiveWork || !ls[0].Out || ls[0].TodoID != other.ID || ls[0].Name != "Broke" {
		t.Errorf("caller's links = %+v", ls)
	}
	if ls := get(other.ID).Links; len(ls) != 1 || ls[0].Kind != model.LinkActiveWork || ls[0].Out || ls[0].TodoID != self.ID {
		t.Errorf("listed task's links = %+v", ls)
	}
	if ls := get(done.ID).Links; len(ls) != 0 {
		t.Errorf("a failed task got links: %+v", ls)
	}

	evs, err := s.Todos.Log.Events(self.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Text != "👀 Checked other work: #"+other.ID || len(evs[0].Tasks) != 1 || evs[0].Tasks[0].ID != other.ID {
		t.Fatalf("caller's run log = %+v", evs)
	}

	// On a hub, the entries it notes about a runner's task join the
	// runner's run log, in time order; the rest of the hub's notes don't.
	s.Todos.Log.Line(self, "▶ something only the hub did")
	runner, _ := json.Marshal([]model.RunEvent{{Kind: "text", Text: "from the runner", At: evs[0].At.Add(-1)}})
	merged, ok := s.withHubNotes(self.ID, runner)
	if !ok || len(merged) != 3 || merged[0].Text != "from the runner" || len(merged[1].Tasks) != 1 {
		t.Fatalf("merged = %+v", merged)
	}
}
