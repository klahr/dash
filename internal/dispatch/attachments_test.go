package dispatch

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
)

// uploadFleet is a runner that answers the upload of an attached file
// (PUT UploadPath) with upload's error; everything else goes as usual.
type uploadFleet struct {
	*fakeFleet
	upload error
}

func (f uploadFleet) Call(ctx context.Context, id, method, path string, in, out any) error {
	if method == http.MethodPut && strings.HasPrefix(path, "/api/hub/uploads/") {
		f.fakeFleet.mu.Lock()
		f.fakeFleet.calls = append(f.fakeFleet.calls, id+" "+method+" "+path)
		f.fakeFleet.mu.Unlock()
		return f.upload
	}
	return f.fakeFleet.Call(ctx, id, method, path, in, out)
}

// attachFixture is a fixture whose runner "box" answers uploads with
// upload, a project on it and a task with one attachment at path. notes
// is the task's log.
func attachFixture(t *testing.T, upload error, path string) (*fixture, model.Project, model.Todo, *[]string) {
	t.Helper()
	fx := newFixture(t)
	notes := &[]string{}
	fx.d.Note = func(_ model.Todo, line string) { *notes = append(*notes, line) }
	fx.d.Fleet = uploadFleet{fx.fleet, upload}
	fx.fleet.set(runnerOf("box", fx.orgA, true))
	p := fx.project(t, fx.orgA, "box")
	td := fx.todo(t, p, func(x *model.Todo) {
		x.Attachments = []model.Attachment{{ID: "att1", Name: "shot.png", Type: "image/png", Size: 3, Path: path}}
	})
	return fx, p, td, notes
}

func attachedFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func hasNote(notes []string, s string) bool {
	for _, n := range notes {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

// The attached file is sent to the runner before the task, which names it.
func TestAttachmentIsSentWithTheTask(t *testing.T) {
	t.Parallel()
	fx, p, td, _ := attachFixture(t, nil, attachedFile(t))
	if _, _, err := fx.d.Dispatch(context.Background(), p, td); err != nil {
		t.Fatal(err)
	}
	calls := fx.fleet.callsTo("box")
	if len(calls) != 3 || calls[1] != "box PUT "+UploadPathFor("att1", "shot.png") {
		t.Errorf("calls %q, want link, upload, task", calls)
	}
	if len(fx.fleet.tasks) != 1 || len(fx.fleet.tasks[0].Attachments) != 1 || fx.fleet.tasks[0].Attachments[0].ID != "att1" {
		t.Errorf("tasks sent %+v", fx.fleet.tasks)
	}
}

// A file gone from the hub is left out: the task goes without it, and its
// log says so.
func TestAttachmentGoneFromTheHubIsLeftOut(t *testing.T) {
	t.Parallel()
	fx, p, td, notes := attachFixture(t, nil, filepath.Join(t.TempDir(), "gone.png"))
	if _, _, err := fx.d.Dispatch(context.Background(), p, td); err != nil {
		t.Fatal(err)
	}
	for _, c := range fx.fleet.callsTo("box") {
		if strings.Contains(c, "/api/hub/uploads/") {
			t.Errorf("uploaded a missing file: %q", c)
		}
	}
	if len(fx.fleet.tasks) != 1 || len(fx.fleet.tasks[0].Attachments) != 0 {
		t.Errorf("tasks sent %+v, want one without attachments", fx.fleet.tasks)
	}
	if !hasNote(*notes, "shot.png is gone from the hub") {
		t.Errorf("task log %q", *notes)
	}
}

// A runner too old to take uploads gets the task without its files, and
// the task's log says to update it.
func TestRunnerTooOldForAttachmentsGetsTheTaskWithoutThem(t *testing.T) {
	t.Parallel()
	old := &tunnel.CallError{Status: http.StatusForbidden, Msg: "box: this runner doesn't offer this"}
	fx, p, td, notes := attachFixture(t, old, attachedFile(t))
	rid, _, err := fx.d.Dispatch(context.Background(), p, td)
	if err != nil || rid != "box" {
		t.Fatalf("dispatch = %q %v", rid, err)
	}
	if len(fx.fleet.tasks) != 1 || len(fx.fleet.tasks[0].Attachments) != 0 {
		t.Errorf("tasks sent %+v, want one without attachments", fx.fleet.tasks)
	}
	if !hasNote(*notes, "too old to take attached files") {
		t.Errorf("task log %q", *notes)
	}
}

// Any other failure to upload holds the task back: it waits, and isn't sent.
func TestFailedUploadMakesTheTaskWait(t *testing.T) {
	t.Parallel()
	for name, upErr := range map[string]error{
		"500":       &tunnel.CallError{Status: http.StatusInternalServerError, Msg: "box: disk error"},
		"other 403": &tunnel.CallError{Status: http.StatusForbidden, Msg: "box: not allowed"},
		"offline":   tunnel.ErrOffline,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fx, p, td, _ := attachFixture(t, upErr, attachedFile(t))
			_, _, err := fx.d.Dispatch(context.Background(), p, td)
			var we *WaitError
			if !errors.As(err, &we) || !strings.Contains(we.Reason, "sending shot.png") {
				t.Fatalf("dispatch = %v, want a WaitError for the upload", err)
			}
			if len(fx.fleet.tasks) != 0 {
				t.Errorf("task sent despite the failed upload: %+v", fx.fleet.tasks)
			}
		})
	}
}
