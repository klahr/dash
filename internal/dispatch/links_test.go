package dispatch

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// A runner reports its task's links (conflicts, notes) by the hub's ids;
// the hub keeps the active_work links it recorded itself, which the runner
// doesn't have, and the report with none (an older runner) keeps those.
func TestRunnerLinksReachTheHub(t *testing.T) {
	at := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	runnerTask := model.Todo{ID: "r1", HubTodoID: "h1", Links: []model.Link{
		{Kind: model.LinkConflict, TodoID: "r2", HubTodoID: "h2", Name: "two", Files: []string{"a.go"}, At: at},
		{Kind: model.LinkNote, TodoID: "r3", Name: "the runner's own", At: at.Add(2 * time.Minute)},
	}}
	b, err := json.Marshal(StateOf(runnerTask))
	if err != nil {
		t.Fatal(err)
	}
	var ts TaskState
	if err := json.Unmarshal(b, &ts); err != nil {
		t.Fatal(err)
	}
	have := []model.Link{
		{Kind: model.LinkActiveWork, Out: true, TodoID: "h4", At: at.Add(time.Minute)},
		{Kind: model.LinkConflict, TodoID: "h2", Files: []string{"old.go"}, At: at.Add(-time.Hour)}, // the runner's say replaces it
	}
	got := hubLinks(have, ts.Links)
	if len(got) != 3 {
		t.Fatalf("links %+v", got)
	}
	if got[0].TodoID != "h2" || got[0].HubTodoID != "" || got[0].Files[0] != "a.go" ||
		got[1].TodoID != "h4" || got[1].Kind != model.LinkActiveWork ||
		got[2].TodoID != "" || got[2].Name != "the runner's own" {
		t.Errorf("links %+v", got)
	}
	if got := hubLinks(have, nil); len(got) != 1 || got[0].TodoID != "h4" {
		t.Errorf("with no report: %+v", got)
	}
}
