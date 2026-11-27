package queue

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/screenshot"
)

// shotsExec is a Claude run that changes file (made with its folders) and
// ends with report.
func shotsExec(file, report string) *fakeExec {
	return &fakeExec{result: report, fn: func(_ int, req agent.ExecRequest) error {
		path := filepath.Join(req.Dir, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("<p>changed</p>\n"), 0o644)
	}}
}

const shotsReport = "Moved the board settings.\n\nScreenshot pages:\n- /settings — Settings → Board\n"

// A finished task's screenshots: whether they are taken, and why not, is on
// the task once it is in Review; they never hold it up. Here the runner has
// no Chrome, the one reason the task shows (the others keep no set).
func TestScreenshotDecisionOnTheTask(t *testing.T) {
	parallel(t)
	web := model.Project{Name: "p", RunCommand: "true", Screenshots: &model.ScreenshotSettings{On: true, UIPaths: []string{"web/"}}}
	for _, c := range []struct {
		name, file, report string
		project            model.Project
		state, note        string // "": no set
		hint               bool
	}{
		{"no Chrome here", "web/index.html", shotsReport, web, model.ShotsSkipped, "no Chrome", true},
		{"no pages listed", "web/index.html", "Done.", web, model.ShotsSkipped, "listed no pages", true},
		{"no UI file changed", "README.md", shotsReport, web, "", "", true},
		{"off", "web/index.html", shotsReport, model.Project{Name: "p", RunCommand: "true"}, "", "", false},
		{"not a web project", "web/index.html", shotsReport, model.Project{Name: "p", Screenshots: web.Screenshots}, "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			exe := shotsExec(c.file, c.report)
			h := newHarness(t, exe, c.project)
			h.engine.Screenshots = &screenshot.Capturer{Root: filepath.Join(t.TempDir(), "shots"), Chrome: filepath.Join(t.TempDir(), "no-chrome")}
			td := h.waitFor(h.add("change the page").ID, model.StatusReview)
			s := td.Screenshots
			switch {
			case c.state == "" && s != nil:
				t.Fatalf("want no screenshots, got %+v", s)
			case c.state != "" && (s == nil || s.State != c.state || !strings.Contains(s.Note, c.note) || s.Run != td.Runs):
				t.Fatalf("want %s with %q, got %+v", c.state, c.note, s)
			}
			if c.report == shotsReport && s != nil && (len(s.Listed) != 1 || s.Listed[0].Path != "/settings" || s.Listed[0].Label != "Settings → Board") {
				t.Fatalf("want Claude's page kept, got %+v", s.Listed)
			}
			if got := strings.Contains(exe.calls[0].AppendSystem, "Screenshot pages:"); got != c.hint {
				t.Fatalf("hint in the system prompt: %v, want %v", got, c.hint)
			}
		})
	}
}

// Without a Capturer (a Dash that takes none), nothing changes.
func TestNoCapturerNoScreenshots(t *testing.T) {
	parallel(t)
	exe := shotsExec("web/index.html", shotsReport)
	h := newHarness(t, exe, model.Project{Name: "p", RunCommand: "true", Screenshots: &model.ScreenshotSettings{On: true}})
	td := h.waitFor(h.add("change the page").ID, model.StatusReview)
	if td.Screenshots != nil || strings.Contains(exe.calls[0].AppendSystem, "Screenshot pages:") {
		t.Fatalf("got %+v", td.Screenshots)
	}
}
