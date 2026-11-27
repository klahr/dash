package model

import (
	"slices"
	"strings"
	"time"
)

// ScreenshotSettings are a web project's before/after screenshots: when a
// task finishes, Dash runs the app (RunCommand) on the task's branch and on
// the branch it lands on, opens pages in headless Chrome and keeps a picture
// of each, at a wide and a phone size, for Review (internal/screenshot).
type ScreenshotSettings struct {
	// On turns the screenshots on (off by default).
	On bool `json:"on,omitempty"`
	// UIPaths are the folders and files of the app's UI, relative to the
	// repository root (e.g. "web/", "src/components"): screenshots are taken
	// only when the task changed a file under one of them. Empty: any change.
	UIPaths []string `json:"ui_paths,omitempty"`
	// Pages are captured for every task, before the ones Claude lists.
	Pages []ScreenshotPage `json:"pages,omitempty"`
	// SetupCommand runs once the app answers, before the pages are opened
	// (seed data, make a test user), with $PORT and $DASH_APP_URL. Lines it
	// prints as "Set-Cookie: name=value" are set in the browser, to sign in.
	SetupCommand string `json:"setup_command,omitempty"`
}

// ScreenshotsOn reports whether p takes screenshots of its finished tasks.
func (p Project) ScreenshotsOn() bool { return p.Screenshots != nil && p.Screenshots.On }

// ScreenshotPage is a page of the app to capture: its path ("/settings")
// and a short label ("Settings → Board").
type ScreenshotPage struct {
	Path  string `json:"path"`
	Label string `json:"label,omitempty"`
}

// How a task's screenshots stand (ScreenshotSet.State).
const (
	ShotsCapturing = "capturing" // being taken now
	ShotsDone      = "done"      // taken (some may be missing: see Note)
	ShotsSkipped   = "skipped"   // none taken: Note says why
)

// ScreenshotSet is a task's before/after screenshots, from its latest run.
type ScreenshotSet struct {
	State string `json:"state"`
	// Note says why there are none, or what is missing ("before: the server
	// didn't start"). Short, for the Review view.
	Note  string     `json:"note,omitempty"`
	Pages []ShotPage `json:"pages,omitempty"`
	// Listed are the pages Claude listed in its report, kept for a later
	// run (a test fix) whose report lists none.
	Listed []ScreenshotPage `json:"listed,omitempty"`
	// Run is the task's run (Todo.Runs) they are of.
	Run int       `json:"run,omitempty"`
	At  time.Time `json:"at"`
}

// ShotPage is one page's pictures: file names in the task's screenshot
// folder (served at /api/todos/<task>/screenshots/<name>), "" where the
// picture couldn't be taken.
type ShotPage struct {
	ScreenshotPage
	WideBefore  string `json:"wide_before,omitempty"`
	WideAfter   string `json:"wide_after,omitempty"`
	PhoneBefore string `json:"phone_before,omitempty"`
	PhoneAfter  string `json:"phone_after,omitempty"`
}

// Files are the page's pictures that were taken.
func (p ShotPage) Files() []string {
	var out []string
	for _, f := range []string{p.WideBefore, p.WideAfter, p.PhoneBefore, p.PhoneAfter} {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Has reports whether name is one of the set's pictures.
func (s *ScreenshotSet) Has(name string) bool {
	if s == nil || name == "" {
		return false
	}
	for _, p := range s.Pages {
		if slices.Contains(p.Files(), name) {
			return true
		}
	}
	return false
}

// Clone is a deep copy of s (nil for nil).
func (s *ScreenshotSet) Clone() *ScreenshotSet {
	if s == nil {
		return nil
	}
	cp := *s
	cp.Pages = slices.Clone(s.Pages)
	cp.Listed = slices.Clone(s.Listed)
	return &cp
}

// Clone is a deep copy of s (nil for nil).
func (s *ScreenshotSettings) Clone() *ScreenshotSettings {
	if s == nil {
		return nil
	}
	cp := *s
	cp.UIPaths = slices.Clone(s.UIPaths)
	cp.Pages = slices.Clone(s.Pages)
	return &cp
}

// screenshotCommand is p's screenshot setup command ("" for none).
func (p Project) screenshotCommand() string {
	if p.Screenshots == nil {
		return ""
	}
	return p.Screenshots.SetupCommand
}

// setScreenshotCommand sets p's screenshot setup command, in a copy of its
// settings (the stored project may share them).
func (p *Project) setScreenshotCommand(cmd string) {
	if p.Screenshots == nil {
		if strings.TrimSpace(cmd) == "" {
			return
		}
		p.Screenshots = &ScreenshotSettings{}
	} else {
		p.Screenshots = p.Screenshots.Clone()
	}
	p.Screenshots.SetupCommand = cmd
}
