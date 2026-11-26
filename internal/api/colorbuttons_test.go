package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// Project settings → General's "Use the project colour for buttons and
// highlights": on until turned off, and saved apart from the colour.
func TestProjectColorButtonsSetting(t *testing.T) {
	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	h := loggedIn(t, s.Routes())
	patch := func(body string) model.Project {
		t.Helper()
		w := do(t, h, "PATCH", "/api/projects/"+p.ID, body)
		if w.Code != http.StatusOK {
			t.Fatalf("PATCH %s: %d %s", body, w.Code, w.Body)
		}
		var got model.Project
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	if got := patch(`{"color":"#ef6b6b"}`); got.ColorButtons != nil || !got.UsesColorForButtons() {
		t.Errorf("a new project's buttons should take its colour: %v", got.ColorButtons)
	}
	got := patch(`{"color_buttons":false}`)
	if got.UsesColorForButtons() || got.Color != "#ef6b6b" {
		t.Errorf("turned off: uses %v, color %q", got.UsesColorForButtons(), got.Color)
	}
	// Other settings leave it as it is.
	if got := patch(`{"name":"renamed"}`); got.UsesColorForButtons() {
		t.Error("changing the name turned color_buttons back on")
	}
	if got, err := raw(s).GetProject(p.ID); err != nil || got.UsesColorForButtons() {
		t.Errorf("stored: %v, %v", got.ColorButtons, err)
	}
	if got := patch(`{"color_buttons":true}`); !got.UsesColorForButtons() {
		t.Error("turned back on: still off")
	}
}
