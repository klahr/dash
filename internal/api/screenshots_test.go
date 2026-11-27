package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/screenshot"
)

// A task's screenshots are served from its folder, only those its set
// names; the Review view asks for them by name.
func TestTodoScreenshot(t *testing.T) {
	rr := newReleaseRepo(t, "")
	rr.s.Screenshots = &screenshot.Capturer{Root: t.TempDir()}
	td, err := raw(rr.s).CreateTodo(model.Todo{ProjectID: rr.p.ID, Title: "t", Prompt: "p",
		Screenshots: &model.ScreenshotSet{State: model.ShotsDone, Run: 1, Pages: []model.ShotPage{{
			ScreenshotPage: model.ScreenshotPage{Path: "/", Label: "Home"}, WideAfter: "r1-p0-wide-after.webp", WideBefore: "r1-p0-wide-before.webp"}}}})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(rr.s.Screenshots.Root, td.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"r1-p0-wide-after.webp", "r1-p0-phone-after.webp"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("RIFF\x00\x00\x00\x00WEBP"+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	get := func(name string) (int, string, http.Header) {
		w := send(rr.owner, "GET", "/api/todos/"+td.ID+"/screenshots/"+name, "")
		return w.Code, w.Body.String(), w.Header()
	}
	if code, body, hdr := get("r1-p0-wide-after.webp"); code != http.StatusOK || body != "RIFF\x00\x00\x00\x00WEBPr1-p0-wide-after.webp" ||
		hdr.Get("Content-Type") != "image/webp" || hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("picture: %d %q %v", code, body, hdr)
	}
	for _, name := range []string{
		"r1-p0-wide-before.webp", // named, but not taken
		"r1-p0-phone-after.webp", // there, but not the set's
		"stray", "..%2Fsecret.webp",
	} {
		if code, _, _ := get(name); code != http.StatusNotFound {
			t.Errorf("%s: %d", name, code)
		}
	}
}

// Project settings → Screenshots: saved in their clean form, off with nothing set is none.
func TestScreenshotSettings(t *testing.T) {
	rr := newReleaseRepo(t, "")
	patch := func(body string) (int, model.Project) {
		w := send(rr.owner, "PATCH", "/api/projects/"+rr.p.ID, body)
		var p model.Project
		_ = json.Unmarshal(w.Body.Bytes(), &p)
		return w.Code, p
	}
	code, p := patch(`{"screenshots": {"on": true, "ui_paths": [" web/ ", ""], "pages": [{"path": " /settings ", "label": " Settings  →  Board "}, {"path": "/settings"}, {"path": "/"}],
		"setup_command": "echo seeded\r\n"}}`)
	want := &model.ScreenshotSettings{On: true, UIPaths: []string{"web/"}, SetupCommand: "echo seeded",
		Pages: []model.ScreenshotPage{{Path: "/settings", Label: "Settings → Board"}, {Path: "/", Label: "/"}}}
	if code != http.StatusOK || !reflect.DeepEqual(p.Screenshots, want) {
		t.Fatalf("%d %+v", code, p.Screenshots)
	}
	if code, _ := patch(`{"screenshots": {"on": true, "pages": [{"path": "https://example.com/"}]}}`); code != http.StatusBadRequest {
		t.Fatalf("a full URL as a page: %d", code)
	}
	if code, p := patch(`{"name": "renamed"}`); code != http.StatusOK || !reflect.DeepEqual(p.Screenshots, want) {
		t.Fatalf("another setting changed them: %+v", p.Screenshots)
	}
	if code, p := patch(`{"screenshots": {}}`); code != http.StatusOK || p.Screenshots != nil {
		t.Fatalf("cleared: %d %+v", code, p.Screenshots)
	}
	if !(model.Project{Screenshots: want}).ScreenshotsOn() || (model.Project{}).ScreenshotsOn() {
		t.Fatal("ScreenshotsOn")
	}
	pr := model.Project{Screenshots: want}
	if pr.Command(model.CmdScreenshot) != "echo seeded" {
		t.Fatal("the setup command is the project's screenshot command")
	}
	pr.SetCommand(model.CmdScreenshot, "")
	if pr.Screenshots.SetupCommand != "" || want.SetupCommand != "echo seeded" {
		t.Fatal("SetCommand changed the shared settings")
	}
}
