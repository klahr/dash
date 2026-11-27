package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/screenshot"
	"github.com/klahr/dash/internal/store"
)

// Before/after screenshots of a finished task's pages (internal/screenshot):
// the task's model.Todo.Screenshots says which pictures there are; the
// pictures are files on the computer that ran the task, served here, or for
// a hub's task fetched from its runner.

// maxShot bounds a picture fetched from a runner.
const maxShot = 8 << 20

// todoScreenshot handles GET /api/todos/{tid}/screenshots/{name}.
func (s *Server) todoScreenshot(w http.ResponseWriter, r *http.Request) {
	sv, t, ok := s.todo(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !screenshot.ValidName(name) || !t.Screenshots.Has(name) {
		writeErr(w, fmt.Errorf("%w: %v", store.ErrNotFound, screenshot.ErrNoPicture))
		return
	}
	var b []byte
	var err error
	if t.OnRunner() {
		b, err = sv.fetchFromRunner(r.Context(), t, "screenshots/"+url.PathEscape(name))
		if err == nil && len(b) > maxShot {
			err = fmt.Errorf("%w: the screenshot is too large", errBadRequest)
		}
	} else {
		b, err = s.readShot(t, name)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeShot(w, b)
}

// hubTaskScreenshot handles GET /api/hub/tasks/{htid}/screenshots/{name} on
// a runner: the hub's todoScreenshot for a task it sent here.
func (s *Server) hubTaskScreenshot(w http.ResponseWriter, r *http.Request) {
	_, t, ok := s.hubTaskFor(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !t.Screenshots.Has(name) {
		writeErr(w, fmt.Errorf("%w: %v", store.ErrNotFound, screenshot.ErrNoPicture))
		return
	}
	b, err := s.readShot(t, name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeShot(w, b)
}

func (s *Server) readShot(t model.Todo, name string) ([]byte, error) {
	path, err := s.Screenshots.Path(t.ID, name)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", store.ErrNotFound, err)
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %v", store.ErrNotFound, screenshot.ErrNoPicture)
	}
	return b, err
}

// writeShot answers with a WebP picture. Its name changes with every run,
// so it may be kept a while.
func writeShot(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(b)
}

// maxShotSetup bounds the screenshot setup command, like other commands' scripts.
const maxShotSetup = 32 << 10

// cleanScreenshotSettings puts a project's screenshot settings in their
// stored form: UI paths trimmed, pages checked (screenshot.CleanPath) and
// labelled, the setup command a clean script. Nothing set and off: nil.
func cleanScreenshotSettings(in *model.ScreenshotSettings) *model.ScreenshotSettings {
	if in == nil {
		return nil
	}
	out := &model.ScreenshotSettings{On: in.On, SetupCommand: model.CleanScript(in.SetupCommand)}
	for _, u := range in.UIPaths {
		if u = strings.TrimSpace(u); u != "" && len(out.UIPaths) < 50 {
			out.UIPaths = append(out.UIPaths, u)
		}
	}
	out.Pages = screenshot.Pages(in.Pages, nil)
	if !out.On && len(out.UIPaths) == 0 && len(out.Pages) == 0 && out.SetupCommand == "" {
		return nil
	}
	return out
}

// checkScreenshotSettings refuses settings that can't be kept: a page path
// that isn't one, or a setup command that is too long.
func checkScreenshotSettings(in *model.ScreenshotSettings) error {
	if in == nil {
		return nil
	}
	if len(in.SetupCommand) > maxShotSetup {
		return fmt.Errorf("%w: the screenshot setup command is longer than %d KiB", errBadRequest, maxShotSetup>>10)
	}
	if len(in.Pages) > screenshot.MaxPages {
		return fmt.Errorf("%w: at most %d pages", errBadRequest, screenshot.MaxPages)
	}
	for _, p := range in.Pages {
		if _, ok := screenshot.CleanPath(p.Path); !ok {
			return fmt.Errorf("%w: %q is not a page's path: start it with / (e.g. /settings)", errBadRequest, p.Path)
		}
	}
	return nil
}

// screenshotCommand is the screenshot setup command req sets, if it sets one.
func (req projectUpdate) screenshotCommand() *string {
	if req.Screenshots == nil {
		return nil
	}
	c := req.Screenshots.SetupCommand
	return &c
}
