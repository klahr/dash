package disk

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/localrun"
)

// Defaults of a Monitor.
const (
	// DefaultTrimEvery is how often the Go build cache is checked against
	// its limit (and also whenever space runs low).
	DefaultTrimEvery = 24 * time.Hour
	// DefaultLargestEvery is how often, at most, the largest folders under
	// the cache folder are measured again while space stays low.
	DefaultLargestEvery = time.Hour
	// largestN is how many of them the warning lists.
	largestN = 5
)

// Monitor is the runner's side: it measures its disks for its reports,
// says whether a task may start, and, when the hub's settings ask for it,
// trims the Go build cache while no task builds.
type Monitor struct {
	// Targets are the folders whose filesystems are measured: the projects
	// folder first, then the worktrees and the user's cache folder.
	Targets []Target
	// CacheDir is the user's cache folder (os.UserCacheDir), whose largest
	// folders the low-space warning lists ("" = none).
	CacheDir string
	// Busy reports whether a task (or a command of one) is running on the
	// runner: the Go build cache isn't trimmed under a running build.
	Busy func() bool
	// Changed is told when the status changed in a way the hub should hear
	// of soon (optional).
	Changed func()

	// For tests: how disks, folders and the Go cache are measured and the
	// cache trimmed (nil: for real).
	Measure  func([]Target) []Volume
	Size     func(dir string) (uint64, error)
	GoCache  func(ctx context.Context) (string, error) // `go env GOCACHE`
	CleanGo  func(ctx context.Context) error           // `go clean -cache`
	Now      func() time.Time
	Name     string // the runner's name, for messages
	Interval time.Duration

	mu       sync.Mutex
	settings Settings
	largest  []Folder
	measured time.Time // when largest was
	gocache  *GoCache
	checked  time.Time // when the Go cache was last checked against its limit
	pending  bool      // a trim waits for the running tasks to finish
	low      bool
	trimming bool
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Monitor) volumes() []Volume {
	if m.Measure != nil {
		return m.Measure(m.Targets)
	}
	return Measure(m.Targets)
}

func (m *Monitor) size(dir string) (uint64, error) {
	if m.Size != nil {
		return m.Size(dir)
	}
	return DirSize(dir)
}

// SetSettings takes the thresholds the hub sent. Turning the cache trim on
// (or lowering its limit) checks the cache soon.
func (m *Monitor) SetSettings(s Settings) {
	s = s.Clean()
	m.mu.Lock()
	if s.TrimAbove() > 0 && (m.settings.TrimAbove() == 0 || s.TrimAbove() < m.settings.TrimAbove()) {
		m.checked = time.Time{}
	}
	m.settings = s
	m.mu.Unlock()
}

// Settings are the thresholds in use.
func (m *Monitor) Settings() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings
}

// Status measures the disks now (cheap) and adds what was measured of the
// cache folder and the Go build cache.
func (m *Monitor) Status() Status {
	st := Status{Volumes: m.volumes()}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, low := m.settings.Low(st); low {
		st.Largest = append([]Folder(nil), m.largest...)
	}
	if m.gocache != nil {
		c := *m.gocache
		st.GoCache = &c
	}
	return st
}

// CanStart is nil when there is room to start a task, or why there isn't:
// "runner X has only 1.2 GB free". s, when set, are the hub's thresholds as
// it sent the task, which are taken (nil: the last ones the hub sent).
func (m *Monitor) CanStart(s *Settings) error {
	if s != nil {
		m.SetSettings(*s)
	}
	set := m.Settings()
	st := Status{Volumes: m.volumes()}
	v, full := set.TooFull(st)
	if !full {
		return nil
	}
	return &FullError{Runner: m.Name, Volume: v, Need: set.MinFree()}
}

// FullError is why a runner won't start a task: too little disk space.
type FullError struct {
	Runner string
	Volume Volume
	Need   uint64
}

func (e *FullError) Error() string { return FullMessage(e.Runner, e.Volume, e.Need) }

// FullMessage says that runner has too little free on v to start a task.
func FullMessage(runner string, v Volume, need uint64) string {
	who := "this runner"
	if runner != "" {
		who = "runner " + runner
	}
	where := ""
	if v.Label != "projects" {
		where = " for its " + v.Label + " folder"
	}
	return fmt.Sprintf("%s has only %s free%s (it needs %s to start a task); free some space on it, e.g. with go clean -cache", who, Size(v.Free), where, Size(need))
}

// Run checks the disks every Interval (default a minute) until ctx ends:
// when space runs low it measures the largest folders under the cache
// folder, and it checks the Go build cache against its limit once a day,
// when space runs low, and again once the tasks that held up a trim end.
func (m *Monitor) Run(ctx context.Context) {
	every := m.Interval
	if every <= 0 {
		every = time.Minute
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		m.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Check is one round of Run.
func (m *Monitor) Check(ctx context.Context) {
	st := Status{Volumes: m.volumes()}
	now := m.now()
	m.mu.Lock()
	s := m.settings
	_, low := s.Low(st)
	crossed := low && !m.low
	m.low = low
	measure := low && m.CacheDir != "" && (crossed || now.Sub(m.measured) >= DefaultLargestEvery)
	if !low && m.largest != nil {
		m.largest = nil
	}
	trim := s.TrimAbove() > 0 && (crossed || m.pending || m.checked.IsZero() || now.Sub(m.checked) >= DefaultTrimEvery)
	m.mu.Unlock()
	if crossed {
		slog.Warn("runner: disk space low", "free", Size(lowest(st).Free), "path", lowest(st).Path)
	}
	if trim {
		m.TrimGoCache(ctx)
	} else if m.goCacheStale(now) {
		m.measureGoCache(ctx)
	}
	if measure {
		largest := Largest(m.CacheDir, largestN, m.size)
		m.mu.Lock()
		m.largest, m.measured = largest, now
		m.mu.Unlock()
		if m.Changed != nil {
			m.Changed()
		}
	}
}

func lowest(st Status) Volume {
	v, _ := st.Lowest()
	return v
}

// goCacheStale: the Go cache size shown is measured about daily, and at
// the start (when go is there).
func (m *Monitor) goCacheStale(now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.checked.IsZero() || now.Sub(m.checked) >= DefaultTrimEvery
}

// measureGoCache measures the Go build cache's size without trimming it.
func (m *Monitor) measureGoCache(ctx context.Context) (path string, size uint64, err error) {
	defer func() {
		m.mu.Lock()
		m.checked = m.now()
		if path != "" || err != nil {
			c := GoCache{Path: path, Size: size}
			if m.gocache != nil {
				c.Trimmed, c.Freed = m.gocache.Trimmed, m.gocache.Freed
			}
			if err != nil {
				c.Error = err.Error()
			}
			m.gocache = &c
		}
		m.mu.Unlock()
	}()
	path, err = m.goCachePath(ctx)
	if err != nil || path == "" {
		return "", 0, err
	}
	size, err = m.size(path)
	return path, size, err
}

// TrimGoCache runs `go clean -cache` if the Go build cache is bigger than
// the limit set, and no task is running; with one running, it tries again
// once none is. It reports whether it trimmed.
func (m *Monitor) TrimGoCache(ctx context.Context) bool {
	m.mu.Lock()
	limit := m.settings.TrimAbove()
	if limit == 0 || m.trimming {
		m.mu.Unlock()
		return false
	}
	m.trimming = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.trimming = false
		m.mu.Unlock()
	}()
	path, size, err := m.measureGoCache(ctx)
	if err != nil || path == "" || size <= limit {
		m.setPending(false)
		return false
	}
	if m.Busy != nil && m.Busy() {
		m.setPending(true) // not under a running build
		return false
	}
	m.setPending(false)
	slog.Info("runner: trimming the Go build cache", "path", path, "size", Size(size), "limit", Size(limit))
	err = m.cleanGo(ctx)
	after, _ := m.size(path)
	m.mu.Lock()
	c := GoCache{Path: path, Size: after, Trimmed: m.now().UTC().Format(time.RFC3339)}
	if after < size {
		c.Freed = size - after
	}
	if err != nil {
		c.Error = "go clean -cache: " + err.Error()
		slog.Warn("runner: trim the Go build cache", "err", err)
	}
	m.gocache = &c
	m.mu.Unlock()
	if m.Changed != nil {
		m.Changed()
	}
	return err == nil
}

func (m *Monitor) setPending(p bool) {
	m.mu.Lock()
	m.pending = p
	m.mu.Unlock()
}

func (m *Monitor) goCachePath(ctx context.Context) (string, error) {
	if m.GoCache != nil {
		return m.GoCache(ctx)
	}
	if _, err := exec.LookPath("go"); err != nil {
		return "", nil // no Go here: nothing to trim
	}
	if err := localrun.Check("go env GOCACHE"); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "env", "GOCACHE").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOCACHE: %w", err)
	}
	p := strings.TrimSpace(string(out))
	if p == "off" {
		return "", nil
	}
	return p, nil
}

func (m *Monitor) cleanGo(ctx context.Context) error {
	if m.CleanGo != nil {
		return m.CleanGo(ctx)
	}
	if err := localrun.Check("go clean -cache"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "clean", "-cache")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}
