// Package runner starts and supervises project processes: web servers, one-off
// tasks (tests, deploys) and interactive PTY sessions. Every process gets its
// own process group so Stop kills the whole tree, and what is left of the
// group when its main process exits is ended too (not on Windows).
package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/klahr/dash/internal/fsutil"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

type Publisher interface{ Publish(model.Event) }

var ErrNotFound = errors.New("process not found")

// ErrNoTerminals is returned for a terminal on a runner whose system Dash
// can't open pseudo-terminals on.
var ErrNoTerminals = errors.New("terminals aren't supported on this runner's operating system yet")

const scrollbackSize = 256 * 1024

// leftoverGrace is how long what a finished process left in its group gets
// after SIGTERM before it is killed.
const leftoverGrace = 5 * time.Second

type proc struct {
	mu   sync.Mutex
	info model.Process
	cmd  *exec.Cmd // nil when Kind == ProcPTY
	tty  terminal  // nil unless Kind == ProcPTY
	buf  []byte    // scrollback, capped at scrollbackSize
	subs map[chan []byte]struct{}
}

type Manager struct {
	pub   Publisher
	mu    sync.RWMutex
	procs map[string]*proc
	store string // file keeping the last server/test/deploy runs, if any
	// saveMu serializes writes of store (they share its .tmp file). Once
	// stopped, StopAll has written the final state on shutdown: processes
	// it kills, exiting afterwards, don't write it again.
	saveMu  sync.Mutex
	stopped bool
}

type savedProc struct {
	Info       model.Process `json:"info"`
	Scrollback []byte        `json:"scrollback"`
}

// Keep saves the latest run of each server, test and deploy command in path,
// so their outcome and output survive restarting or updating Dash. Runs that
// a restart cut short come back without an exit code (interrupted).
func (m *Manager) Keep(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved []savedProc
	if err := json.Unmarshal(b, &saved); err != nil {
		return err
	}
	for _, s := range saved {
		if s.Info.Running || (s.Info.ExitCode != nil && *s.Info.ExitCode == -1) {
			s.Info.Running, s.Info.ExitCode = false, nil
		}
		m.procs[s.Info.ID] = &proc{info: s.Info, buf: s.Scrollback, subs: map[chan []byte]struct{}{}}
	}
	return nil
}

// save writes the latest non-interactive run per project, kind, command and directory.
func (m *Manager) save() {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	if !m.stopped {
		m.write()
	}
}

func (m *Manager) write() {
	m.mu.RLock()
	if m.store == "" {
		m.mu.RUnlock()
		return
	}
	// Pick the latest run per key by its info alone, then copy only those
	// runs' scrollback; encode and write once the lock is released.
	type pick struct {
		p    *proc
		info model.Process
	}
	latest := map[[4]string]pick{}
	for _, p := range m.procs {
		info := p.snapshot()
		if info.Kind == model.ProcPTY {
			continue
		}
		k := [4]string{info.ProjectID, string(info.Kind), info.Command, info.Dir}
		if cur, ok := latest[k]; !ok || info.StartedAt.After(cur.info.StartedAt) {
			latest[k] = pick{p, info}
		}
	}
	path := m.store
	m.mu.RUnlock()
	out := make([]savedProc, 0, len(latest))
	for _, l := range latest {
		l.p.mu.Lock()
		out = append(out, savedProc{Info: l.p.info, Scrollback: append([]byte(nil), l.p.buf...)})
		l.p.mu.Unlock()
	}
	b, _ := json.Marshal(out)
	if err := fsutil.WriteFileAtomic(path, b, 0o600); err != nil {
		slog.Warn("save processes", "err", err)
	}
}

// runKey groups runs of the same thing: a process that has exited is
// forgotten once a newer run of its key has exited too. A quick action
// (Preset, Main) is its own thing even when its command matches another's.
type runKey struct {
	project, kind, command, dir, todo, preset string
	main                                      bool
}

func pruneKey(i model.Process) runKey {
	return runKey{i.ProjectID, string(i.Kind), i.Command, i.Dir, i.TodoID, i.Preset, i.Main}
}

// prune forgets exited processes other than the newest exited one of each
// pruneKey, so a long-running runner doesn't keep every terminal, preview
// and test run it ever started (each with up to scrollbackSize of output).
// A session still attached to a forgotten process keeps working.
func (m *Manager) prune() {
	m.mu.Lock()
	defer m.mu.Unlock()
	type kept struct {
		id      string
		started time.Time
	}
	newest := map[runKey]kept{}
	for id, p := range m.procs {
		info := p.snapshot()
		if info.Running {
			continue
		}
		k := pruneKey(info)
		cur, ok := newest[k]
		switch {
		case !ok:
			newest[k] = kept{id, info.StartedAt}
		case info.StartedAt.After(cur.started):
			delete(m.procs, cur.id)
			newest[k] = kept{id, info.StartedAt}
		default:
			delete(m.procs, id)
		}
	}
}

func NewManager(pub Publisher) *Manager {
	return &Manager{pub: pub, procs: map[string]*proc{}}
}

// Origin is where a process the dashboard started comes from: the
// project's preset command it runs, and whether it runs in the project's
// own checkout (see model.Process).
type Origin struct {
	Preset string
	Main   bool
	// Env is the process's environment (nil: this process's). Other
	// members' commands on a shared runner get one without its owner's
	// Claude login (claudecli.SharedEnv).
	Env []string
}

func (o Origin) set(i *model.Process) { i.Preset, i.Main = o.Preset, o.Main }

func origin(o []Origin) Origin {
	if len(o) > 0 {
		return o[0]
	}
	return Origin{}
}

// StartWeb runs a server command with $PORT set to a free port and exposes
// http://localhost:$PORT.
func (m *Manager) StartWeb(projectID, dir, command string, o ...Origin) (model.Process, error) {
	return m.startWeb(projectID, "", dir, command, origin(o))
}

// StartWebFor is StartWeb for a server running a todo's worktree (a live
// preview); the process carries the todo's id.
func (m *Manager) StartWebFor(projectID, todoID, dir, command string) (model.Process, error) {
	return m.startWeb(projectID, todoID, dir, command, Origin{})
}

func (m *Manager) startWeb(projectID, todoID, dir, command string, o Origin) (model.Process, error) {
	port, err := freePort()
	if err != nil {
		return model.Process{}, err
	}
	cmd := shell(dir, command)
	if o.Env != nil {
		cmd.Env = slices.Clone(o.Env)
	}
	cmd.Env = append(cmd.Env, fmt.Sprintf("PORT=%d", port))
	return m.startPiped(projectID, model.ProcWeb, command, fmt.Sprintf("http://localhost:%d", port), cmd, func(i *model.Process) {
		i.Port, i.TodoID = port, todoID
		o.set(i)
	})
}

// StartTask runs a one-shot command (tests, build, deploy).
func (m *Manager) StartTask(projectID, dir, command string, o ...Origin) (model.Process, error) {
	cmd := shell(dir, command)
	if env := origin(o).Env; env != nil {
		cmd.Env = env
	}
	return m.startPiped(projectID, model.ProcTask, command, "", cmd, origin(o).set)
}

func shell(dir, command string) *exec.Cmd {
	cmd := exec.Command(localrun.Bash(), "-lc", command)
	cmd.Dir = dir
	cmd.Env = localrun.Environ()
	ownGroup(cmd)
	return cmd
}

func (m *Manager) startPiped(projectID string, kind model.ProcessKind, command, url string, cmd *exec.Cmd, init func(*model.Process)) (model.Process, error) {
	if err := localrun.Check(string(kind) + ": " + command); err != nil {
		return model.Process{}, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return model.Process{}, err
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		return model.Process{}, err
	}
	w.Close() // child holds its own copy
	p := m.register(projectID, kind, command, url, cmd.Dir, cmd.Process.Pid, cmd, nil, init)
	go m.pump(p, r)
	go m.wait(p, r)
	return p.snapshot(), nil
}

// terminal is a process attached to a pseudo-terminal (pty_unix.go,
// pty_windows.go): reading gives its output, writing types into it. Close
// ends the output once the process has exited.
type terminal interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
	Pid() int
	Wait() error // as exec.Cmd.Wait
}

// StartPTY runs command (default: the user's shell, else bash) attached to a
// pseudo-terminal.
func (m *Manager) StartPTY(projectID, dir, command string, cols, rows uint16, o ...Origin) (model.Process, error) {
	if command == "" {
		command = os.Getenv("SHELL")
		if command == "" {
			command = "bash"
		}
	}
	return m.startPTY(projectID, dir, command, "exec "+command, nil, cols, rows, origin(o).set)
}

// StartPTYFor runs script (a bash script, not exec'd, so "build && exec
// $SHELL" works) attached to a pseudo-terminal in a todo's worktree, with env
// added to the environment: a terminal preview. The process carries the
// todo's id; command is what it shows as.
func (m *Manager) StartPTYFor(projectID, todoID, dir, command, script string, env []string, cols, rows uint16) (model.Process, error) {
	return m.startPTY(projectID, dir, command, script, env, cols, rows, func(i *model.Process) { i.TodoID = todoID })
}

func (m *Manager) startPTY(projectID, dir, command, script string, env []string, cols, rows uint16, init func(*model.Process)) (model.Process, error) {
	if err := localrun.Check("terminal: " + command); err != nil {
		return model.Process{}, err
	}
	cmd := exec.Command(localrun.Bash(), "-lc", script)
	cmd.Dir = dir
	cmd.Env = localrun.Environ(append([]string{"TERM=xterm-256color"}, env...)...)
	if cols == 0 || rows == 0 {
		cols, rows = 120, 32
	}
	tty, err := startTerminal(cmd, cols, rows)
	if err != nil {
		return model.Process{}, err
	}
	p := m.register(projectID, model.ProcPTY, command, "", dir, tty.Pid(), nil, tty, init)
	go m.pump(p, tty)
	go m.wait(p, tty)
	return p.snapshot(), nil
}

func (m *Manager) register(projectID string, kind model.ProcessKind, command, url, dir string, pid int, cmd *exec.Cmd, tty terminal, init func(*model.Process)) *proc {
	p := &proc{
		info: model.Process{
			ID: store.NewID(), ProjectID: projectID, Kind: kind, Command: command, Dir: dir,
			PID: pid, URL: url, Running: true, StartedAt: time.Now(),
		},
		cmd: cmd, tty: tty, subs: map[chan []byte]struct{}{},
	}
	if init != nil {
		init(&p.info)
	}
	m.mu.Lock()
	m.procs[p.info.ID] = p
	m.mu.Unlock()
	m.publish(p)
	return p
}

func (m *Manager) pump(p *proc, r io.Reader) {
	b := make([]byte, 32*1024)
	for {
		n, err := r.Read(b)
		if n > 0 {
			chunk := append([]byte(nil), b[:n]...)
			p.mu.Lock()
			p.buf = appendScrollback(p.buf, chunk)
			for ch := range p.subs {
				select {
				case ch <- chunk:
				default: // slow viewer drops output rather than stalling the process
				}
			}
			p.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// appendScrollback appends chunk to buf, dropping the oldest output beyond
// scrollbackSize. It shifts buf in place rather than reslicing its tail, so
// the backing array stays about scrollbackSize instead of growing past it.
func appendScrollback(buf, chunk []byte) []byte {
	if len(chunk) >= scrollbackSize {
		return append(buf[:0], chunk[len(chunk)-scrollbackSize:]...)
	}
	if over := len(buf) + len(chunk) - scrollbackSize; over > 0 {
		buf = buf[:copy(buf, buf[over:])]
	}
	return append(buf, chunk...)
}

func (m *Manager) wait(p *proc, r io.Closer) {
	var err error
	if p.tty != nil {
		err = p.tty.Wait()
	} else {
		err = p.cmd.Wait()
	}
	// Whatever the command left running in its group (a server started with
	// &) goes with it, however it ended; Stop only reaches running processes.
	// p.info.PID is set for terminals too (p.cmd is nil for them) and never
	// changes, so it is read without the lock.
	localrun.ReapGroup(p.info.PID, leftoverGrace)
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		code = -1
	}
	time.Sleep(100 * time.Millisecond) // let pump drain trailing output
	r.Close()
	p.mu.Lock()
	p.info.Running = false
	p.info.ExitCode = &code
	for ch := range p.subs {
		close(ch)
	}
	p.subs = map[chan []byte]struct{}{}
	p.mu.Unlock()
	if p.tty == nil {
		m.save()
	}
	m.prune()
	m.publish(p)
}

// Stop asks the process group to stop (SIGTERM; on Windows it is killed
// at once), then kills it after a grace period.
func (m *Manager) Stop(id string) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	info := p.snapshot()
	if !info.Running {
		return nil // finished, or restored from a previous run: its PID may belong to something else now
	}
	pgid := info.PID
	if err := terminateGroup(pgid); err != nil {
		return err
	}
	go func() {
		time.Sleep(5 * time.Second)
		if p.snapshot().Running { // gone already: its id may be reused by now
			killGroup(pgid)
		}
	}()
	return nil
}

// HangUp sends SIGHUP to a running process's group, as closing its
// terminal would: an interactive shell ignores SIGTERM. Nothing happens on
// Windows, where Stop kills the tree anyway.
func (m *Manager) HangUp(id string) {
	if p, err := m.Get(id); err == nil && p.Running && p.PID > 0 {
		hangUpGroup(p.PID)
	}
}

// StopAll is called on shutdown.
func (m *Manager) StopAll() {
	m.saveMu.Lock()
	if !m.stopped {
		m.write()
		m.stopped = true
	}
	m.saveMu.Unlock()
	for _, p := range m.List("") {
		if p.Running {
			killGroup(p.PID)
		}
	}
}

func (m *Manager) List(projectID string) []model.Process {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []model.Process
	for _, p := range m.procs {
		if projectID == "" || p.info.ProjectID == projectID {
			out = append(out, p.snapshot())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// Session is a viewer attached to a process (used by the terminal WebSocket).
type Session struct {
	Scrollback []byte
	Output     <-chan []byte // closed when the process exits or Detach is called
	Info       model.Process
	p          *proc
	ch         chan []byte
}

func (m *Manager) Attach(id string) (*Session, error) {
	p, err := m.get(id)
	if err != nil {
		return nil, err
	}
	ch := make(chan []byte, 256)
	p.mu.Lock()
	defer p.mu.Unlock()
	s := &Session{Scrollback: append([]byte(nil), p.buf...), Output: ch, Info: p.info, p: p, ch: ch}
	if p.info.Running {
		p.subs[ch] = struct{}{}
	} else {
		close(ch)
	}
	return s, nil
}

func (s *Session) Detach() {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	if _, ok := s.p.subs[s.ch]; ok {
		delete(s.p.subs, s.ch)
		close(s.ch)
	}
}

// Write sends keyboard input. Only PTY processes accept input.
func (s *Session) Write(b []byte) error {
	if s.p.tty == nil {
		return errors.New("process is not interactive")
	}
	_, err := s.p.tty.Write(b)
	return err
}

func (s *Session) Resize(cols, rows uint16) error {
	if s.p.tty == nil {
		return nil
	}
	return s.p.tty.Resize(cols, rows)
}

// Tail returns up to the last n bytes of a process's output.
func (m *Manager) Tail(id string, n int) ([]byte, error) {
	p, err := m.get(id)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.buf
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return append([]byte(nil), b...), nil
}

// Get returns a process's current state.
func (m *Manager) Get(id string) (model.Process, error) {
	p, err := m.get(id)
	if err != nil {
		return model.Process{}, err
	}
	return p.snapshot(), nil
}

func (m *Manager) get(id string) (*proc, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.procs[id]
	if !ok {
		return nil, ErrNotFound
	}
	return p, nil
}

func (p *proc) snapshot() model.Process {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info
}

func (m *Manager) publish(p *proc) {
	info := p.snapshot()
	m.pub.Publish(model.Event{Type: model.EvProcUpdated, ProjectID: info.ProjectID, Data: info, Time: time.Now()})
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
