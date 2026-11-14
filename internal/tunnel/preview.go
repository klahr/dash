package tunnel

// Previews through the tunnel. A runner's live previews listen on its own
// loopback, which the hub can't reach, so the runner keeps a second
// WebSocket open to the hub just for preview traffic. Being its own TCP
// connection, with its own write lock, a heavy page load or a busy hot-reload
// socket can't hold up the task traffic on the main tunnel; and the hub opens
// at most previewStreams streams on it at a time, so its buffers stay bounded.
//
// The hub opens one stream per connection to a preview server and starts it
// with one line, "preview <todo id>\n". The runner answers with one line,
// "ok <port>\n" or "<state>\n" (starting, crashed, none, error), and on ok
// carries raw bytes between the stream and the preview's server from then on
// (HTTP, and WebSocket upgrades for hot reload). The runner looks the todo up
// among the previews it started itself and dials only that server's loopback
// address: the hub can't name a port or host.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	previewChannel = "preview"        // ?channel= of the preview connection
	previewStreams = 32               // streams the hub opens at once on one runner's preview connection
	previewHello   = 15 * time.Second // to send and answer the first line
	maxPreviewLine = 128

	// PreviewHeader is how the hub tells a connecting runner where its
	// previews are served: a URL with "*" in place of the host's first
	// label, e.g. https://*.preview.example.com/. Absent without a preview
	// domain.
	PreviewHeader = "Dash-Preview-Url"
)

// Why a runner can't forward to a preview. Its PreviewDialer returns these,
// and Hub.DialPreview returns them on the hub.
var (
	ErrPreviewNotRunning = errors.New("no preview is running for this task")
	ErrPreviewStarting   = errors.New("the preview server is starting")
	ErrPreviewCrashed    = errors.New("the preview server stopped")
)

var todoIDRe = regexp.MustCompile(`^[a-z0-9]{1,32}$`)

// PreviewDialer connects to a preview a runner started itself.
type PreviewDialer interface {
	// DialPreview connects to todoID's preview server and returns the port
	// it listens on (as the app knows it: http://localhost:<port>).
	DialPreview(todoID string) (conn net.Conn, port int, err error)
}

// previewLink is a runner's preview connection, on the hub.
type previewLink struct {
	s     *Session
	slots chan struct{}
}

// DialPreview connects to todoID's preview on the runner name, through the
// runner's preview connection, and returns the port its server listens on
// there. It waits for a free stream if the runner has previewStreams open.
func (h *Hub) DialPreview(ctx context.Context, name, todoID string) (net.Conn, int, error) {
	if !todoIDRe.MatchString(todoID) {
		return nil, 0, ErrPreviewNotRunning
	}
	h.mu.Lock()
	var link *previewLink
	for _, r := range h.runners {
		if r.Name == name {
			link = h.previews[r.ID]
		}
	}
	h.mu.Unlock()
	if link == nil {
		return nil, 0, fmt.Errorf("%s is offline", name)
	}
	select {
	case link.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case <-link.s.Done():
		return nil, 0, fmt.Errorf("%s is offline", name)
	}
	c, err := link.s.Open()
	if err != nil {
		<-link.slots
		return nil, 0, err
	}
	conn := &slotConn{Conn: c, release: func() { <-link.slots }}
	port, err := previewHandshake(ctx, conn, todoID)
	if err != nil {
		conn.Close()
		return nil, 0, err
	}
	return conn, port, nil
}

func previewHandshake(ctx context.Context, c net.Conn, todoID string) (int, error) {
	dl := time.Now().Add(previewHello)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	_ = c.SetDeadline(dl)
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if _, err := io.WriteString(c, "preview "+todoID+"\n"); err != nil {
		return 0, err
	}
	line, err := readLine(c)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("runner didn't answer: %w", err)
	}
	_ = c.SetDeadline(time.Time{})
	word, rest, _ := strings.Cut(line, " ")
	switch word {
	case "ok":
		port, err := strconv.Atoi(rest)
		if err != nil || port <= 0 || port > 65535 {
			return 0, fmt.Errorf("runner answered %q", line)
		}
		return port, nil
	case "starting":
		return 0, ErrPreviewStarting
	case "crashed":
		return 0, ErrPreviewCrashed
	case "none":
		return 0, ErrPreviewNotRunning
	case "error":
		return 0, errors.New(rest)
	}
	return 0, fmt.Errorf("runner answered %q", line)
}

// readLine reads up to "\n" byte by byte, so nothing after it is consumed.
func readLine(r io.Reader) (string, error) {
	var b []byte
	var one [1]byte
	for len(b) <= maxPreviewLine {
		if _, err := io.ReadFull(r, one[:]); err != nil {
			return "", err
		}
		if one[0] == '\n' {
			return string(b), nil
		}
		b = append(b, one[0])
	}
	return "", errors.New("line too long")
}

// slotConn gives its stream slot back when closed.
type slotConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *slotConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// servePreviewConnect keeps a runner's preview connection until it closes.
func (h *Hub) servePreviewConnect(w http.ResponseWriter, r *http.Request, rec record, hash string) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	link := &previewLink{s: NewSession(ws, true), slots: make(chan struct{}, previewStreams)}
	h.mu.Lock()
	if !h.registeredLocked(rec.ID) {
		h.mu.Unlock()
		link.s.Close()
		return
	}
	if h.previews == nil {
		h.previews = map[string]*previewLink{}
	}
	old := h.previews[rec.ID]
	h.previews[rec.ID] = link
	h.mu.Unlock()
	if old != nil {
		old.s.Close()
	}
	go h.watch(link.s, hash)
	<-link.s.Done()
	h.mu.Lock()
	if h.previews[rec.ID] == link {
		delete(h.previews, rec.ID)
	}
	h.mu.Unlock()
}

// ---- the runner's side ----

// keepPreviews holds the preview connection open while ctx lasts (the main
// tunnel is up), reconnecting when it drops.
func (c *Client) keepPreviews(ctx context.Context, url string) {
	backoff := 500 * time.Millisecond
	for ctx.Err() == nil {
		start := time.Now()
		err := c.servePreviews(ctx, url)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 30*time.Second {
			backoff = 500 * time.Millisecond
		}
		slog.Warn("runner: preview connection lost, retrying", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (c *Client) servePreviews(ctx context.Context, url string) error {
	ws, res, err := c.dialer().DialContext(ctx, url+"?channel="+previewChannel, http.Header{"Authorization": {"Bearer " + c.Creds.Token}})
	if err != nil {
		if res != nil {
			return fmt.Errorf("connect previews: %s", res.Status)
		}
		return err
	}
	sess := NewSession(ws, false)
	defer sess.Close()
	stop := context.AfterFunc(ctx, func() { sess.Close() })
	defer stop()
	for {
		st, err := sess.Accept()
		if err != nil {
			return ErrSessionClosed
		}
		go c.forwardPreview(st)
	}
}

// forwardPreview serves one stream the hub opened: it names a todo, and the
// runner connects it to that todo's preview server, if it started one.
func (c *Client) forwardPreview(st net.Conn) {
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(previewHello))
	line, err := readLine(st)
	if err != nil {
		return
	}
	id, ok := strings.CutPrefix(line, "preview ")
	if !ok || !todoIDRe.MatchString(id) {
		return
	}
	local, port, err := c.Previews.DialPreview(id)
	if err != nil {
		answer := "none"
		switch {
		case errors.Is(err, ErrPreviewStarting):
			answer = "starting"
		case errors.Is(err, ErrPreviewCrashed):
			answer = "crashed"
		case errors.Is(err, ErrPreviewNotRunning):
		default:
			answer = "error " + strings.ReplaceAll(err.Error(), "\n", " ")
		}
		_, _ = io.WriteString(st, answer+"\n")
		return
	}
	defer local.Close()
	if _, err := io.WriteString(st, "ok "+strconv.Itoa(port)+"\n"); err != nil {
		return
	}
	_ = st.SetDeadline(time.Time{})
	pipe(st, local)
}

// pipe copies both ways until either side is done, then closes both.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	a.Close()
	b.Close()
	<-done
}
