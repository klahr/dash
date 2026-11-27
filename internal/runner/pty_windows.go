package runner

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// conPTY is a process on a Windows pseudo console (ConPTY, Windows 10 1809
// and Windows Server 2019 or later). exec.Cmd can't pass the pseudo console
// attribute to the child, so it is started with CreateProcess.
type conPTY struct {
	hpc  windows.Handle
	in   *os.File // we write keyboard input here
	out  *os.File // the console's rendered output
	proc *os.Process
	once sync.Once
}

// startTerminal starts cmd (its Path, Args, Dir and Env) on a new pseudo
// console of cols×rows.
func startTerminal(cmd *exec.Cmd, cols, rows uint16) (terminal, error) {
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, err
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		return nil, err
	}
	var hpc windows.Handle
	err := windows.CreatePseudoConsole(coord(cols, rows), inR, outW, 0, &hpc)
	// The console holds its own copies of its ends of the pipes.
	windows.CloseHandle(inR)
	windows.CloseHandle(outW)
	t := &conPTY{hpc: hpc, in: os.NewFile(uintptr(inW), "conpty-in"), out: os.NewFile(uintptr(outR), "conpty-out")}
	if err != nil {
		t.hpc = 0
		t.Close()
		return nil, err
	}
	if t.proc, err = t.spawn(cmd); err != nil {
		t.Close()
		return nil, err
	}
	return t, nil
}

func (t *conPTY) spawn(cmd *exec.Cmd) (*os.Process, error) {
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attrs.Delete()
	// The attribute's value is the console handle itself, not a pointer to it.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&t.hpc)), unsafe.Sizeof(t.hpc)); err != nil {
		return nil, err
	}
	var si windows.StartupInfoEx
	si.Cb = uint32(unsafe.Sizeof(si))
	// No std handles: without this the child would take the runner's own
	// (redirected) ones instead of the pseudo console's.
	si.Flags = windows.STARTF_USESTDHANDLES
	si.ProcThreadAttributeList = attrs.List()

	app, err := windows.UTF16PtrFromString(cmd.Path)
	if err != nil {
		return nil, err
	}
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(cmd.Args))
	if err != nil {
		return nil, err
	}
	var dir *uint16
	if cmd.Dir != "" {
		if dir, err = windows.UTF16PtrFromString(cmd.Dir); err != nil {
			return nil, err
		}
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	block, err := envBlock(env)
	if err != nil {
		return nil, err
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcess(app, line, nil, nil, false, flags, &block[0], dir, &si.StartupInfo, &pi); err != nil {
		return nil, &os.PathError{Op: "start", Path: cmd.Path, Err: err}
	}
	defer windows.CloseHandle(pi.Thread)
	// pi.Process keeps the pid from being reused until FindProcess has its own handle.
	defer windows.CloseHandle(pi.Process)
	p, err := os.FindProcess(int(pi.ProcessId))
	if err != nil {
		windows.TerminateProcess(pi.Process, 1)
		return nil, err
	}
	return p, nil
}

func coord(cols, rows uint16) windows.Coord {
	return windows.Coord{X: int16(min(cols, 0x7fff)), Y: int16(min(rows, 0x7fff))}
}

// envBlock is env as CreateProcess takes it: "k=v" strings, each ended by a
// NUL, then one more NUL. Of a name given twice (in any case) the last wins,
// as exec.Cmd does.
func envBlock(env []string) ([]uint16, error) {
	seen := map[string]int{}
	var kept []string
	for _, kv := range env {
		k, _, ok := strings.Cut(kv[min(1, len(kv)):], "=") // "=C:=C:\x" names "=C:"
		if !ok {
			continue
		}
		k = strings.ToUpper(kv[:min(1, len(kv))] + k)
		if i, dup := seen[k]; dup {
			kept[i] = kv
			continue
		}
		seen[k] = len(kept)
		kept = append(kept, kv)
	}
	var b []uint16
	for _, kv := range kept {
		if strings.IndexByte(kv, 0) >= 0 {
			return nil, errors.New("environment variable contains NUL")
		}
		u, err := windows.UTF16FromString(kv)
		if err != nil {
			return nil, err
		}
		b = append(b, u...) // ends in its NUL
	}
	return append(b, 0), nil
}

func (t *conPTY) Read(b []byte) (int, error)  { return t.out.Read(b) }
func (t *conPTY) Write(b []byte) (int, error) { return t.in.Write(b) }

func (t *conPTY) Resize(cols, rows uint16) error {
	return windows.ResizePseudoConsole(t.hpc, coord(cols, rows))
}

func (t *conPTY) Pid() int { return t.proc.Pid }

func (t *conPTY) Wait() error {
	state, err := t.proc.Wait()
	if err == nil && !state.Success() {
		return &exec.ExitError{ProcessState: state}
	}
	return err
}

// Close closes the pseudo console, which ends what still runs on it, and
// then the pipes. The output pipe only reaches EOF once the console is
// closed, so this is what ends pump.
func (t *conPTY) Close() error {
	t.once.Do(func() {
		if t.hpc != 0 {
			windows.ClosePseudoConsole(t.hpc)
		}
		t.in.Close()
		t.out.Close()
	})
	return nil
}
