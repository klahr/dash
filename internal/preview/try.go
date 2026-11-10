package preview

// Try sessions: a terminal on a project itself rather than on a task, for
// trying the project's CLI at a commit of its history. Like a task's
// terminal preview it runs preview_command (or a shell) in a PTY, with
// ./.dash-bin on PATH as $DASH_BIN (startTerminal), stops after IdleAfter
// without keystrokes, and keeps its recent output for Log. It runs in a
// directory the caller prepared (a detached worktree Dash owns, see
// api/try.go), whose Cleanup runs once the session is stopped or replaced.
// A project has one try session at a time, kept under its key (the hub's
// id of the project).

import (
	"errors"
	"log/slog"
	"net/url"
	"sort"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/runner"
)

// ErrNoTry answers calls about a project with no try session.
var ErrNoTry = errors.New("no try session is running for this project")

// TryPath is the WebSocket path that attaches to key's try session (on a
// runner; the hub serves its own, see api's tryWS).
func TryPath(key string) string { return "/ws/hub/projects/" + url.PathEscape(key) + "/try" }

func tryKey(key string) string { return "try:" + key }

// TryStart is a try session to start.
type TryStart struct {
	Key     string        // the project's key: the hub's id of it
	Project model.Project // this Dash's project: its preview_command runs
	Dir     string        // where it runs: a worktree at Commit, prepared by the caller
	Ref     string        // what was asked for ("" for the default branch's tip)
	Commit  model.CommitInfo
	// Cleanup removes Dir; it runs once, after the terminal has stopped.
	Cleanup func()
}

// StartTry starts a try session, replacing the project's current one (which
// is stopped and cleaned up first; the caller prepares Dir after that, see
// StopTry).
func (m *Manager) StartTry(s TryStart) (model.TrySession, error) {
	_ = m.StopTry(s.Key, "replaced")
	proc, err := m.startTerminal(s.Project, "", s.Dir)
	if err != nil {
		if s.Cleanup != nil {
			s.Cleanup()
		}
		return model.TrySession{}, err
	}
	e := &entry{done: make(chan struct{}), cleanup: s.Cleanup,
		info: model.Preview{ProjectID: s.Project.ID, ProcessID: proc.ID, Kind: model.PreviewTerminal, State: model.PreviewLive,
			TerminalURL: TryPath(s.Key), StartedAt: proc.StartedAt},
		try: &model.TrySession{ProjectID: s.Key, Ref: s.Ref, Branch: s.Project.DefaultBranch,
			Commit: s.Commit.SHA, Subject: s.Commit.Subject, Author: s.Commit.Author},
	}
	e.touch()
	m.mu.Lock()
	if m.entries == nil {
		m.entries = map[string]*entry{}
	}
	old := m.entries[tryKey(s.Key)]
	m.entries[tryKey(s.Key)] = e
	m.mu.Unlock()
	if old != nil { // started meanwhile: this one wins
		m.stopEntry(old, "replaced")
	}
	go m.watch(e)
	return trySnapshot(e), nil
}

// trySnapshot is e's try session as the UI sees it.
func trySnapshot(e *entry) model.TrySession {
	p := e.snapshot()
	e.mu.Lock()
	s := *e.try
	e.mu.Unlock()
	s.ProcessID, s.State, s.TerminalURL, s.ExitCode = p.ProcessID, p.State, p.TerminalURL, p.ExitCode
	s.StartedAt, s.LastSeen = p.StartedAt, p.LastSeen
	return s
}

func (m *Manager) tryEntry(key string) *entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries[tryKey(key)]
}

// Try returns key's try session.
func (m *Manager) Try(key string) (model.TrySession, bool) {
	e := m.tryEntry(key)
	if e == nil {
		return model.TrySession{}, false
	}
	return trySnapshot(e), true
}

// Tries lists the try sessions, newest first.
func (m *Manager) Tries() []model.TrySession {
	m.mu.Lock()
	var es []*entry
	for _, e := range m.entries {
		if e.try != nil {
			es = append(es, e)
		}
	}
	m.mu.Unlock()
	out := make([]model.TrySession, 0, len(es))
	for _, e := range es {
		out = append(out, trySnapshot(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// TryTerminal attaches to key's try session; call touch on every keystroke.
func (m *Manager) TryTerminal(key string) (sess *runner.Session, touch func(), err error) {
	e := m.tryEntry(key)
	if e == nil {
		return nil, nil, ErrNoTry
	}
	sess, err = m.Procs.Attach(e.snapshot().ProcessID)
	if err != nil {
		return nil, nil, err
	}
	e.touch()
	return sess, e.touch, nil
}

// TryLog is the end of key's try terminal as plain text: its last lines (at
// most lines; 0 for DefaultLogLines), without escape sequences.
func (m *Manager) TryLog(key string, lines int) ([]byte, error) {
	e := m.tryEntry(key)
	if e == nil {
		return nil, ErrNoTry
	}
	b, err := m.Procs.Tail(e.snapshot().ProcessID, 1<<20)
	if err != nil {
		return nil, err
	}
	if lines <= 0 {
		lines = DefaultLogLines
	}
	return LastLines(PlainText(b), lines), nil
}

// StopTry stops key's try session and removes its worktree (Cleanup).
func (m *Manager) StopTry(key, reason string) error {
	m.mu.Lock()
	e := m.entries[tryKey(key)]
	delete(m.entries, tryKey(key))
	m.mu.Unlock()
	if e == nil {
		return ErrNoTry
	}
	m.stopEntry(e, reason)
	return nil
}

// StopTries stops every try session.
func (m *Manager) StopTries(reason string) {
	for _, s := range m.Tries() {
		_ = m.StopTry(s.ProjectID, reason)
	}
}

// stopEntry stops a try session already out of the map, then cleans up.
func (m *Manager) stopEntry(e *entry, reason string) {
	e.mu.Lock()
	if e.gone {
		e.mu.Unlock()
		return
	}
	e.gone = true
	close(e.done)
	e.try.Reason = reason
	id, cleanup := e.info.ProcessID, e.cleanup
	e.mu.Unlock()
	// An interactive shell ignores SIGTERM: hang it up first, as closing
	// its terminal would.
	if p, err := m.Procs.Get(id); err == nil && p.Running && p.PID > 0 {
		_ = syscall.Kill(-p.PID, syscall.SIGHUP)
	}
	if err := m.Procs.Stop(id); err != nil && !errors.Is(err, runner.ErrNotFound) {
		slog.Warn("stop try session", "project", e.try.ProjectID, "err", err)
	}
	m.waitExited(id)
	if cleanup != nil {
		cleanup()
	}
}

// waitExited waits a little for process id to end, so its worktree isn't
// removed from under it.
func (m *Manager) waitExited(id string) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p, err := m.Procs.Get(id)
		if err != nil || !p.Running {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Sweep stops what has been idle as of now (Run does it every 30 seconds).
// Test hook: production goes through Run; internal/api's Try tests call this
// to force an idle stop, and being in another package they need it exported.
//
//lint:ignore U1000 test hook for internal/api tests
func (m *Manager) Sweep(now time.Time) { m.sweep(now) }
