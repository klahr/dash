// Package runner starts and supervises project processes: web servers, one-off
// tasks (tests, deploys) and interactive PTY sessions. Every process gets its
// own process group so Stop kills the whole tree.
package runner

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

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
}

func NewManager(pub Publisher) *Manager {
	return &Manager{pub: pub, procs: map[string]*proc{}}
}

// StartWeb runs a server command with $PORT set to a free port and exposes
// http://localhost:$PORT.
func (m *Manager) StartWeb(projectID, dir, command string) (model.Process, error) {
	port, err := freePort()
	if err != nil {
		return model.Process{}, err
	}
	cmd := shell(dir, command)
	cmd.Env = append(cmd.Env, fmt.Sprintf("PORT=%d", port))
	return m.startPiped(projectID, model.ProcWeb, command, fmt.Sprintf("http://localhost:%d", port), cmd)
}

// StartTask runs a one-shot command (tests, build, deploy).
func (m *Manager) StartTask(projectID, dir, command string) (model.Process, error) {
	return m.startPiped(projectID, model.ProcTask, command, "", shell(dir, command))
}

func shell(dir, command string) *exec.Cmd {
	cmd := exec.Command("bash", "-lc", command)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

func (m *Manager) startPiped(projectID string, kind model.ProcessKind, command, url string, cmd *exec.Cmd) (model.Process, error) {
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
	p := m.register(projectID, kind, command, url, cmd, nil)
	go m.pump(p, r)
	go m.wait(p, r)
	return p.snapshot(), nil
}

// StartPTY runs command (default: the user's shell) attached to a pseudo-terminal.
// pty.Start uses Setsid, which already makes the child a process-group leader,
// so Setpgid must not be set as well (the kernel rejects the combination).
func (m *Manager) StartPTY(projectID, dir, command string, cols, rows uint16) (model.Process, error) {
	if command == "" {
		command = os.Getenv("SHELL")
		if command == "" {
			command = "bash"
		}
	}
	cmd := exec.Command("bash", "-lc", "exec "+command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	if cols == 0 || rows == 0 {
		cols, rows = 120, 32
	}
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return model.Process{}, err
	}
	p := m.register(projectID, model.ProcPTY, command, "", cmd, tty)
	go m.pump(p, tty)
	go m.wait(p, tty)
	return p.snapshot(), nil
}

func (m *Manager) register(projectID string, kind model.ProcessKind, command, url string, cmd *exec.Cmd, tty *os.File) *proc {
	p := &proc{
		info: model.Process{
			ID: store.NewID(), ProjectID: projectID, Kind: kind, Command: command, Dir: cmd.Dir,
			PID: cmd.Process.Pid, URL: url, Running: true, StartedAt: time.Now(),
		},
		cmd: cmd, tty: tty, subs: map[chan []byte]struct{}{},
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
	m.publish(p)
}

// Stop sends SIGTERM to the process group, then SIGKILL after a grace period.
func (m *Manager) Stop(id string) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	pgid := p.info.PID
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
