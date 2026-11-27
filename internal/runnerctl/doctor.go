package runnerctl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/disk"
	"github.com/klahr/dash/internal/doctor"
)

// DoctorEvery is how often a runner runs its doctor checks on its own.
const DoctorEvery = time.Hour

// MinWorktreeGB is the free space, in GB, the doctor wants on the disks of
// a runner's projects and task worktrees (or the hub's "start no task
// below", when that is higher): each task builds in a worktree of its own.
const MinWorktreeGB = 5

// DoctorProject is a hub project a runner serves, as the doctor checks it:
// the tools its test and run commands use.
type DoctorProject struct {
	ID, Name                string // the hub's project id and its name
	TestCommand, RunCommand string
}

// Doctor runs a runner's setup checks (see package doctor): Preflight's
// (git, bash on Windows, claude, the hub) and the disk space for worktrees,
// gh and headless Chrome, and the tools of the projects it serves. It only
// looks: it installs nothing.
type Doctor struct {
	Preflight Preflight
	// Server is the hub's address, ClaudeBin the claude it runs.
	Server, ClaudeBin string
	// Disk measures the runner's disks, under the hub's thresholds (optional:
	// no disk check).
	Disk func() (disk.Status, disk.Settings)
	// Projects are the hub's projects it serves (optional).
	Projects func() []DoctorProject
	// Sandbox is the runner's -claude-sandbox ("": Claude runs as the
	// user). Bin is the dash binary, beside which the bundled sandbox
	// scripts are looked for; Data the runner's data directory (its
	// runner.env); Service whether it runs as the background service.
	Sandbox, Bin, Data string
	Service            bool
	// Stat, ReadFile and Getenv stand in for the os package's in tests.
	Stat     func(string) (os.FileInfo, error)
	ReadFile func(string) ([]byte, error)
	Getenv   func(string) string
	// Now stands in for time.Now in tests.
	Now func() time.Time

	runMu sync.Mutex // one run at a time
	mu    sync.Mutex
	last  *doctor.Report
}

// Last is the latest report (nil before the first run ends).
func (d *Doctor) Last() *doctor.Report {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.last == nil {
		return nil
	}
	cp := *d.last
	return &cp
}

func (d *Doctor) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Run runs the checks now and keeps the report as Last. Asked while a run
// is under way, it waits for that one and returns its report.
func (d *Doctor) Run(ctx context.Context) doctor.Report {
	asked := d.now()
	d.runMu.Lock()
	defer d.runMu.Unlock()
	if last := d.Last(); last != nil && !last.At.Before(asked) {
		return *last
	}
	rep := d.run(ctx)
	d.mu.Lock()
	d.last = &rep
	d.mu.Unlock()
	return rep
}

// Loop runs the checks now and every DoctorEvery until ctx ends, calling
// done after each run (to report it).
func (d *Doctor) Loop(ctx context.Context, done func()) {
	for {
		d.Run(ctx)
		if done != nil {
			done()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(DoctorEvery):
		}
	}
}

func (d *Doctor) getenv(k string) string {
	if d.Getenv != nil {
		return d.Getenv(k)
	}
	return os.Getenv(k)
}

func (d *Doctor) stat(p string) bool {
	st := os.Stat
	if d.Stat != nil {
		st = d.Stat
	}
	_, err := st(p)
	return err == nil
}

// System is the runner's system, for the fix commands.
func (d *Doctor) System() doctor.System {
	s := doctor.System{GOOS: d.Preflight.goos()}
	if s.GOOS == "linux" {
		read := os.ReadFile
		if d.ReadFile != nil {
			read = d.ReadFile
		}
		if b, err := read("/etc/os-release"); err == nil {
			s.Family = doctor.ParseOSRelease(string(b))
		}
	}
	return s
}

func (d *Doctor) run(ctx context.Context) doctor.Report {
	sys := d.System()
	p := d.Preflight
	required := func(c Check, fix string) doctor.Check {
		out := doctor.Check{Group: doctor.GroupRequired, Name: c.Name, Status: doctor.OK, Message: c.Detail}
		switch {
		case c.Warn:
			out.Status = doctor.Warning
		case !c.OK:
			out.Status = doctor.Error
		}
		if out.Status != doctor.OK {
			out.Fix = fix
			if out.Fix == "" {
				out.Message += " (" + c.Fix + ")"
			}
		}
		return out
	}
	checks := []doctor.Check{required(p.git(), doctor.Install("git", sys))}
	if sys.GOOS == "windows" {
		checks = append(checks, required(p.bash(), doctor.Install("git", sys)))
	}
	bin := d.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	claude := p.claude(ctx, bin)
	claudeFix := "claude"
	if _, err := p.lookPath(bin); err != nil {
		claudeFix = doctor.Install("claude", sys)
	}
	cc := required(claude, claudeFix)
	if cc.Status != doctor.OK && claudeFix == "claude" {
		cc.Message += ": run claude and log in (/login)"
	}
	checks = append(checks, cc, required(p.hub(ctx, d.Server), ""))
	if c, ok := d.diskCheck(sys); ok {
		checks = append(checks, c)
	}
	checks = append(checks, d.ghCheck(ctx, sys), d.chromeCheck(sys))
	if c, ok := d.sandboxCheck(sys); ok {
		checks = append(checks, c)
	}
	checks = append(checks, d.projectChecks(sys)...)
	return doctor.Report{At: d.now(), System: sys.Name(), Checks: checks}
}

// diskCheck checks the free space on the disks of its projects and task
// worktrees (ok false: it can't measure them).
func (d *Doctor) diskCheck(sys doctor.System) (doctor.Check, bool) {
	if d.Disk == nil {
		return doctor.Check{}, false
	}
	st, set := d.Disk()
	var vols []disk.Volume
	for _, v := range st.Volumes {
		if v.Label != "cache" && v.Total > 0 {
			vols = append(vols, v)
		}
	}
	low, ok := disk.Status{Volumes: vols}.Lowest()
	if !ok {
		return doctor.Check{}, false
	}
	need := max(set.MinFree(), uint64(MinWorktreeGB*disk.GB))
	c := doctor.Check{Group: doctor.GroupRequired, Name: doctor.CheckDisk, Status: doctor.OK,
		Message: fmt.Sprintf("%s free of %s on the disk of its %s", disk.Size(low.Free), disk.Size(low.Total), low.Label)}
	if low.Free < need {
		c.Status = doctor.Error
		c.Message += fmt.Sprintf("; tasks need at least %s for their worktrees", disk.Size(need))
		if sys.GOOS != "windows" {
			c.Fix = "du -sh ~/.cache/* 2>/dev/null | sort -h | tail" // what fills the cache folder
		}
	}
	return c, true
}

// ghCheck checks for gh, logged in: pull requests need it.
func (d *Doctor) ghCheck(ctx context.Context, sys doctor.System) doctor.Check {
	c := doctor.Check{Group: doctor.GroupFeatures, Name: doctor.CheckGH, Status: doctor.OK}
	path, err := d.Preflight.lookPath("gh")
	if err != nil {
		c.Status, c.Fix = doctor.Warning, doctor.Install("gh", sys)
		c.Message = "No gh: no pull requests. Projects on the default pull-request mode merge by pushing to their branch instead."
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := d.Preflight.output(ctx, path, "auth", "status"); err != nil {
		c.Status, c.Fix = doctor.Warning, "gh auth login"
		c.Message = path + ", not logged in to GitHub: no pull requests, and GitHub's checks and repositories can't be read."
		return c
	}
	c.Message = path + ", logged in"
	return c
}

// chromeCheck checks for a Chrome or Chromium that runs headless.
func (d *Doctor) chromeCheck(sys doctor.System) doctor.Check {
	c := doctor.Check{Group: doctor.GroupFeatures, Name: doctor.CheckChrome, Status: doctor.OK}
	if path := d.FindChrome(); path != "" {
		c.Message = path
		return c
	}
	c.Status, c.Fix = doctor.Warning, doctor.Install("chrome", sys)
	c.Message = "No Chrome: before/after screenshots are skipped, and browser tests can't run."
	return c
}

// sandboxCheck checks that Claude runs in a sandbox (-claude-sandbox),
// where a bundled script can give it one (ok false: none can here).
func (d *Doctor) sandboxCheck(sys doctor.System) (doctor.Check, bool) {
	c := doctor.Check{Group: doctor.GroupFeatures, Name: doctor.CheckSandbox, Status: doctor.OK}
	if d.Sandbox != "" {
		c.Message = "Claude runs tasks and scans in a sandbox: " + d.Sandbox
		return c, true
	}
	script, needs := SandboxScript(sys.GOOS, d.Preflight.lookPath)
	if script == "" {
		return c, false
	}
	c.Status = doctor.Warning
	c.Message = "Claude runs unsandboxed: its tools reach what you can, such as your SSH keys, gh login and other projects."
	var fix []string
	if needs != "" {
		if inst := doctor.Install(needs, sys); inst != "" {
			fix = append(fix, inst)
		} else {
			c.Message += " Install " + needs + " first."
		}
	}
	if path := FindSandboxScript(d.Bin, script); path != "" {
		fix = append(fix, SandboxCommand(path, d.Data, d.Service))
		if !d.Service {
			c.Message += " Restart the runner after the fix."
		}
	} else {
		c.Message += " Put " + script + " from the release archive beside dash and turn it on: see docs/sandbox.md."
	}
	c.Fix = strings.Join(fix, " && ")
	return c, true
}

// FindChrome is the Chrome or Chromium this runner has ("": none): the
// first on PATH, or in its system's usual place.
func (d *Doctor) FindChrome() string {
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "chrome-headless-shell", "headless-shell", "headless_shell"} {
		if path, err := d.Preflight.lookPath(name); err == nil {
			return path
		}
	}
	var places []string
	switch d.Preflight.goos() {
	case "darwin":
		places = []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium"}
		if home := d.getenv("HOME"); home != "" {
			places = append(places, filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"))
		}
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if dir := d.getenv(env); dir != "" {
				places = append(places, dir+`\Google\Chrome\Application\chrome.exe`)
			}
		}
	}
	for _, p := range places {
		if d.stat(p) {
			return p
		}
	}
	return ""
}

// projectChecks checks, for each project, the tools its test and run
// commands start with (doctor.CommandNames).
func (d *Doctor) projectChecks(sys doctor.System) []doctor.Check {
	if d.Projects == nil {
		return nil
	}
	found := map[string]string{} // tool → its path ("": missing)
	var out []doctor.Check
	for _, p := range d.Projects() {
		tools := doctor.CommandNames(p.TestCommand)
		for _, t := range doctor.CommandNames(p.RunCommand) {
			if !slices.Contains(tools, t) {
				tools = append(tools, t)
			}
		}
		for _, t := range tools {
			path, ok := found[t]
			if !ok {
				path, _ = d.Preflight.lookPath(t)
				found[t] = path
			}
			c := doctor.Check{Group: doctor.GroupProject, Name: t, Project: p.ID, ProjectName: p.Name, Status: doctor.OK, Message: path}
			if path == "" {
				c.Status, c.Fix = doctor.Warning, doctor.Install(t, sys)
				c.Message = fmt.Sprintf("%s not found: %s's test or run command uses it, so its tasks fail when they run it.", t, p.Name)
			}
			out = append(out, c)
		}
	}
	return out
}
