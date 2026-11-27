package dispatch

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// A runner's task's screenshots reach the hub with its report, which then
// asks the runner for the pictures by name; the project's screenshot
// settings go to the runner with the link.
func TestScreenshotsTravel(t *testing.T) {
	set := &model.ScreenshotSet{State: model.ShotsDone, Run: 2, Pages: []model.ShotPage{{
		ScreenshotPage: model.ScreenshotPage{Path: "/", Label: "Home"}, WideAfter: "r2-p0-wide-after.webp"}}}
	ts := StateOf(model.Todo{ID: "r1", HubTodoID: "h1", Status: model.StatusReview, Screenshots: set})
	b, err := json.Marshal(ts)
	if err != nil {
		t.Fatal(err)
	}
	var got TaskState
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	var hub model.Todo
	copyReport(&hub, got, model.StatusReview, "laptop")
	if !reflect.DeepEqual(hub.Screenshots, set) {
		t.Fatalf("the hub has %+v", hub.Screenshots)
	}
	set.Pages[0].WideAfter = "changed"
	if ts.Screenshots.Pages[0].WideAfter == "changed" {
		t.Fatal("the report shares the task's set")
	}
}
