package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync/atomic"

	"github.com/gorilla/websocket"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

const detachKey = 0x1d // Ctrl-]

// attachCmd implements tea.ExecCommand: Bubble Tea releases the terminal,
// Run bridges it to /ws/proc/{id} in raw mode, and the TUI resumes when the
// user detaches (Ctrl-]) or the process exits.
type attachCmd struct {
	client *Client
	procID string
	title  string
	stdin  io.Reader
	stdout io.Writer
}

func (a *attachCmd) SetStdin(r io.Reader)  { a.stdin = r }
func (a *attachCmd) SetStdout(w io.Writer) { a.stdout = w }
func (a *attachCmd) SetStderr(io.Writer)   {}

func (a *attachCmd) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := a.client.dial(ctx, "/ws/proc/"+a.procID)
	if err != nil {
		return err
	}
	defer conn.Close()

	in, ok := a.stdin.(*os.File)
	if !ok {
		in = os.Stdin
	}
	fd := int(in.Fd())
	if term.IsTerminal(fd) {
		old, err := term.MakeRaw(fd)
		if err != nil {
			return err
		}
		defer term.Restore(fd, old)
	}
	fmt.Fprintf(a.stdout, "\x1b[2J\x1b[H\x1b[7m attached: %s — Ctrl-] to detach \x1b[0m\r\n", a.title)

	sendResize := func() {
		if w, h, err := term.GetSize(fd); err == nil {
			_ = conn.WriteJSON(map[string]any{"type": "resize", "cols": w, "rows": h - 1})
		}
	}
	sendResize()
	winch := make(chan os.Signal, 1)
	notifyResize(winch)
	defer signal.Stop(winch)

	// A cancelable reader, so our stdin goroutine doesn't swallow the first
	// keystroke meant for the TUI after we return.
	cr, err := cancelreader.NewReader(in)
	if err != nil {
		return err
	}
	defer cr.Cancel()

	var finished atomic.Bool // process gone: the next keypress just returns to the TUI
	detached := make(chan struct{})
	go func() {
		defer close(detached)
		buf := make([]byte, 1024)
		for {
			n, err := cr.Read(buf)
			if err != nil || finished.Load() {
				return
			}
			for i := 0; i < n; i++ {
				if buf[i] == detachKey {
					if i > 0 {
						_ = conn.WriteJSON(map[string]string{"type": "input", "data": string(buf[:i])})
					}
					return
				}
			}
			if err := conn.WriteJSON(map[string]string{"type": "input", "data": string(buf[:n])}); err != nil {
				return
			}
		}
	}()

	exited := make(chan int, 1)
	go func() {
		defer close(exited)
		for {
			typ, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if typ == websocket.BinaryMessage {
				_, _ = a.stdout.Write(data)
				continue
			}
			var msg struct {
				Type string `json:"type"`
				Code int    `json:"code"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "exit" {
				exited <- msg.Code
				return
			}
		}
	}()

	for {
		select {
		case <-winch:
			sendResize()
		case <-detached:
			return nil
		case code, ok := <-exited:
			finished.Store(true)
			if ok {
				fmt.Fprintf(a.stdout, "\r\n\x1b[7m process exited with code %d — press any key \x1b[0m", code)
			} else {
				fmt.Fprint(a.stdout, "\r\n\x1b[7m connection closed — press any key \x1b[0m")
			}
			<-detached // the stdin goroutine returns on the next keypress
			return nil
		}
	}
}
