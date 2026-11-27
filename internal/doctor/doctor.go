// Package doctor holds a runner's setup checks as the hub keeps and shows
// them (Runners → the runner → Doctor): what the runner needs (git, a
// logged-in claude, the hub, disk space), the features it would lose without
// a tool (gh, headless Chrome), and the tools its projects' commands use.
// Every check that fails says how to fix it, as a command for the runner's
// system to copy; the doctor never installs anything itself.
//
// The runner runs the checks (runnerctl.Doctor) when it starts, about hourly,
// and when someone clicks Check again; it reports them to the hub with its
// heartbeat.
package doctor

import (
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/textutil"
)

// Status is how a check went.
type Status string

const (
	OK      Status = "ok"
	Warning Status = "warning"
	Error   Status = "error"
)

// rank orders statuses from good to bad.
func (s Status) rank() int {
	switch s {
	case Warning:
		return 1
	case Error:
		return 2
	}
	return 0
}

// The groups checks come in. A required check that fails is an error; a
// feature or project check that fails a warning.
const (
	GroupRequired = "required"
	GroupFeatures = "features"
	GroupProject  = "project"
)

// Names of the checks other parts of Dash ask about.
const (
	CheckGit    = "git"
	CheckBash   = "bash"
	CheckClaude = "claude"
	CheckHub    = "hub"
	CheckDisk   = "disk"
	CheckGH     = "gh"
	CheckChrome = "chrome"
)

// Check is one check's outcome.
type Check struct {
	Group string `json:"group"`
	// Name is what was checked: "git", "claude", "chrome", or for a project
	// check the tool ("go", "npm", …).
	Name string `json:"name"`
	// Project and ProjectName are, for a project check, the hub's project
	// id and its name.
	Project     string `json:"project,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
	Status      Status `json:"status"`
	// Message says what was found, and for a feature what is lost without it.
	Message string `json:"message"`
	// Fix is a command for the runner's system that fixes it ("" when there
	// is none to give).
	Fix string `json:"fix,omitempty"`
}

// Report is a runner's latest checks.
type Report struct {
	// At is when the checks ran.
	At time.Time `json:"at"`
	// System names the runner's system as the fix commands are for:
	// "macOS", "Linux (Debian/Ubuntu)", "Windows", …
	System string  `json:"system,omitempty"`
	Checks []Check `json:"checks"`
}

// Worst is the worst status of r's checks (OK for none, or a nil r).
func (r *Report) Worst() Status {
	worst := OK
	if r == nil {
		return worst
	}
	for _, c := range r.Checks {
		if c.Status.rank() > worst.rank() {
			worst = c.Status
		}
	}
	return worst
}

// Has reports whether r's check name (a required or feature check)
// passed. known is false when r doesn't say (nil, or no such check).
func (r *Report) Has(name string) (has, known bool) {
	if r == nil {
		return false, false
	}
	for _, c := range r.Checks {
		if c.Name == name && c.Group != GroupProject {
			return c.Status == OK, true
		}
	}
	return false, false
}

// ProjectMissing are the tools project (a hub project id) needs that r
// found missing (none when r doesn't say).
func (r *Report) ProjectMissing(project string) []string {
	if r == nil {
		return nil
	}
	var out []string
	for _, c := range r.Checks {
		if c.Group == GroupProject && c.Project == project && c.Status != OK {
			out = append(out, c.Name)
		}
	}
	return out
}

// Bounds on what the hub keeps of a report.
const (
	maxChecks  = 100
	maxName    = 64
	maxMessage = 500
	maxFix     = 300
)

// Clean bounds a report a runner sent: at most maxChecks checks, each in a
// known group with a known status and clipped text.
func Clean(r Report) Report {
	out := Report{At: r.At, System: textutil.Clip(r.System, 60), Checks: []Check{}}
	for _, c := range r.Checks {
		if len(out.Checks) == maxChecks {
			break
		}
		if !slices.Contains([]string{GroupRequired, GroupFeatures, GroupProject}, c.Group) {
			continue
		}
		if c.Status != OK && c.Status != Warning && c.Status != Error {
			continue
		}
		c.Name = textutil.Clip(strings.TrimSpace(c.Name), maxName)
		if c.Name == "" {
			continue
		}
		c.Project, c.ProjectName = textutil.Clip(c.Project, maxName), textutil.Clip(c.ProjectName, 200)
		if c.Group != GroupProject {
			c.Project, c.ProjectName = "", ""
		}
		c.Message, c.Fix = textutil.Clip(c.Message, maxMessage), textutil.Clip(c.Fix, maxFix)
		out.Checks = append(out.Checks, c)
	}
	return out
}
