// Package runner starts and supervises project processes: web servers, one-off
// tasks (tests, deploys) and interactive PTY sessions. Every process gets its
// own process group so Stop kills the whole tree.
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
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/klahr/dash/internal/fsutil"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

type Publisher interface{ Publish(model.Event) }

var ErrNotFound = errors.New("process not found")

const scrollbackSize = 256 * 1024

type proc struct {
	mu   sync.Mutex
	info model.Process
	cmd  *exec.Cmd
	tty  *os.File // nil unless Kind == ProcPTY
	buf  []byte   // scrollback, capped at scrollbackSize
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
	defer m.mu.RUnlock()
	if m.store == "" {
		return
	}
	latest := map[[4]string]savedProc{}
	for _, p := range m.procs {
		p.mu.Lock()
		s := savedProc{Info: p.info, Scrollback: append([]byte(nil), p.buf...)}
		p.mu.Unlock()
		if s.Info.Kind == model.ProcPTY {
			continue
		}
		k := [4]string{s.Info.ProjectID, string(s.Info.Kind), s.Info.Command, s.Info.Dir}
		if cur, ok := latest[k]; !ok || s.Info.StartedAt.After(cur.Info.StartedAt) {
			latest[k] = s
		}
	}
	out := make([]savedProc, 0, len(latest))
	for _, s := range latest {
		out = append(out, s)
	}
	b, _ := json.Marshal(out)
	if err := fsutil.WriteFileAtomic(m.store, b, 0o600); err != nil {
		slog.Warn("save processes", "err", err)
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
	cmd.Env = append(cmd.Env, fmt.Sprintf("PORT=%d", port))
	return m.startPiped(projectID, model.ProcWeb, command, fmt.Sprintf("http://localhost:%d", port), cmd, func(i *model.Process) {
		i.Port, i.TodoID = port, todoID
		o.set(i)
	})
}

// StartTask runs a one-shot command (tests, build, deploy).
func (m *Manager) StartTask(projectID, dir, command string, o ...Origin) (model.Process, error) {
	return m.startPiped(projectID, model.ProcTask, command, "", shell(dir, command), origin(o).set)
}

func shell(dir, command string) *exec.Cmd {
	cmd := exec.Command("bash", "-lc", command)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
	p := m.register(projectID, kind, command, url, cmd, nil, init)
	go m.pump(p, r)
	go m.wait(p, r)
	return p.snapshot(), nil
}

// StartPTY runs command (default: the user's shell) attached to a pseudo-terminal.
// pty.Start uses Setsid, which already makes the child a process-group leader,
// so Setpgid must not be set as well (the kernel rejects the combination).
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
	cmd := exec.Command("bash", "-lc", script)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "TERM=xterm-256color"), env...)
	if cols == 0 || rows == 0 {
		cols, rows = 120, 32
	}
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return model.Process{}, err
	}
	p := m.register(projectID, model.ProcPTY, command, "", cmd, tty, init)
	go m.pump(p, tty)
	go m.wait(p, tty)
	return p.snapshot(), nil
}

func (m *Manager) register(projectID string, kind model.ProcessKind, command, url string, cmd *exec.Cmd, tty *os.File, init func(*model.Process)) *proc {
	p := &proc{
		info: model.Process{
			ID: store.NewID(), ProjectID: projectID, Kind: kind, Command: command, Dir: cmd.Dir,
			PID: cmd.Process.Pid, URL: url, Running: true, StartedAt: time.Now(),
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
			p.buf = append(p.buf, chunk...)
			if len(p.buf) > scrollbackSize {
				p.buf = p.buf[len(p.buf)-scrollbackSize:]
			}
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

func (m *Manager) wait(p *proc, r io.Closer) {
	err := p.cmd.Wait()
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
	m.publish(p)
}

// Stop sends SIGTERM to the process group, then SIGKILL after a grace period.
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
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	go func() {
		time.Sleep(5 * time.Second)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}()
	return nil
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
			_ = syscall.Kill(-p.PID, syscall.SIGKILL)
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
	return pty.Setsize(s.p.tty, &pty.Winsize{Cols: cols, Rows: rows})
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
