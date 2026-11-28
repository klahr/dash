package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// acpConn is a JSON-RPC 2.0 connection to an ACP agent over its stdin and
// stdout, one message per line. Dash is the client: it calls the agent's
// methods (call, notify) and answers what the agent calls back (onRequest).
//
// Notifications are handled in the order they arrive, on the goroutine
// that reads (serve), before any response that comes after them: a
// session/prompt answer is only seen once the updates it ends are. The
// agent's requests (a permission request may wait on a person) are
// answered on goroutines of their own.
type acpConn struct {
	w   io.Writer
	wmu sync.Mutex

	mu       sync.Mutex
	next     int64
	pending  map[int64]chan acpMessage
	closed   bool
	skippedN int
	done     chan struct{} // closed once serve returns

	// onNotify gets each notification the agent sends.
	onNotify func(method string, params json.RawMessage)
	// onRequest answers each request the agent sends; nil answers "method
	// not found".
	onRequest func(method string, params json.RawMessage) (any, *acpError)
	// onOther gets each line that isn't a JSON-RPC message.
	onOther func(line string)
}

// acpMessage is any JSON-RPC message: a request (Method and ID), a
// notification (Method) or a response (ID with Result or Error).
type acpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpError       `json:"error,omitempty"`
}

// acpError is a JSON-RPC error, as the agent sent it.
type acpError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *acpError) Error() string {
	msg := e.Message
	if len(e.Data) > 0 && string(e.Data) != "null" {
		var d struct {
			Details string `json:"details"`
			Message string `json:"message"`
		}
		if json.Unmarshal(e.Data, &d) == nil && (d.Details != "" || d.Message != "") {
			msg += ": " + strings.TrimSpace(d.Details+" "+d.Message)
		} else if s := strings.TrimSpace(string(e.Data)); len(s) < 300 {
			msg += ": " + s
		}
	}
	return fmt.Sprintf("%s (%d)", msg, e.Code)
}

// JSON-RPC and ACP error codes.
const (
	acpMethodNotFound = -32601
	// acpAuthRequired is ACP's auth_required: the agent isn't signed in.
	acpAuthRequired = -32000
)

// errACPClosed is the error of a call the agent never answered: it exited
// or closed its output.
var errACPClosed = errors.New("the agent closed the connection")

// isAuthRequired reports whether err is the agent's auth_required error.
func isAuthRequired(err error) bool {
	var e *acpError
	if !errors.As(err, &e) {
		return false
	}
	return e.Code == acpAuthRequired && strings.Contains(strings.ToLower(e.Message), "auth")
}

func newACPConn(w io.Writer) *acpConn {
	return &acpConn{w: w, pending: map[int64]chan acpMessage{}, done: make(chan struct{})}
}

// serve reads the agent's messages from r until it ends, then fails every
// call still waiting.
func (c *acpConn) serve(r io.Reader) {
	n := pumpLines(r, c.handle)
	c.mu.Lock()
	c.closed, c.skippedN = true, n
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.done)
}

// skipped is how many over-long lines serve left out, once it returned.
func (c *acpConn) skipped() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.skippedN
}

func (c *acpConn) handle(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	var m acpMessage
	if err := json.Unmarshal([]byte(line), &m); err != nil || (m.Method == "" && len(m.ID) == 0) {
		if c.onOther != nil {
			c.onOther(line)
		}
		return
	}
	switch {
	case m.Method != "" && len(m.ID) > 0:
		go c.answer(m)
	case m.Method != "":
		if c.onNotify != nil {
			c.onNotify(m.Method, m.Params)
		}
	default:
		id, err := strconv.ParseInt(strings.Trim(string(m.ID), `"`), 10, 64)
		if err != nil {
			return
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
}

// answer replies to one of the agent's requests.
func (c *acpConn) answer(m acpMessage) {
	var result any
	rerr := &acpError{Code: acpMethodNotFound, Message: "Dash doesn't serve " + m.Method}
	if c.onRequest != nil {
		result, rerr = c.onRequest(m.Method, m.Params)
	}
	reply := map[string]any{"jsonrpc": "2.0", "id": m.ID}
	if rerr != nil {
		reply["error"] = rerr
	} else {
		if result == nil {
			result = map[string]any{}
		}
		reply["result"] = result
	}
	_ = c.write(reply)
}

func (c *acpConn) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(append(b, '\n'))
	return err
}

// notify sends a notification (no answer).
func (c *acpConn) notify(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// call sends a request and waits for its answer, which it decodes into out
// (if not nil). An error answer is an *acpError.
func (c *acpConn) call(ctx context.Context, method string, params, out any) error {
	ch := make(chan acpMessage, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errACPClosed
	}
	c.next++
	id := c.next
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return fmt.Errorf("%s: %w", method, errACPClosed)
		}
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			if err := json.Unmarshal(m.Result, out); err != nil {
				return fmt.Errorf("%s: unexpected answer: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}
