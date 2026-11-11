package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/klahr/dash/internal/fsutil"
)

// Credentials are what a runner keeps after registering with a hub.
type Credentials struct {
	Server string `json:"server"` // the hub's base URL
	ID     string `json:"id"`
	Name   string `json:"name"`
	Token  string `json:"token"`
	// OrgID is the hub's org the runner works for (0: registered with an
	// older hub). The runner refuses projects of any other org.
	OrgID int64 `json:"org_id,omitempty"`
	// UserID is the hub's user who registered the runner, its owner (0:
	// registered with an older hub). The runner runs the general chat and
	// Claude's short calls only for them.
	UserID int64 `json:"user_id,omitempty"`
	// Projects is the runner's -projects directory, as set up by
	// `dash runner setup --projects` ("" = the default).
	Projects string `json:"projects,omitempty"`
}

// ErrRevoked means the hub no longer knows the runner's token.
var ErrRevoked = errors.New("the hub rejected this runner's token (revoked?); register again with --code")

// CheckServer validates a hub URL. Plain http is accepted only for loopback
// hosts: the token and all traffic travel over it.
func CheckServer(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("--server %q: want https://host[:port]", raw)
	}
	if u.Scheme == "http" {
		h := u.Hostname()
		if h != "localhost" && h != "127.0.0.1" && h != "::1" {
			return nil, fmt.Errorf("--server %q: use https (plain http only for localhost)", raw)
		}
	}
	return u, nil
}

// Register swaps a registration code from the hub's Runners page for this
// runner's credentials. prev is the token this computer was registered with before, if any: the
// hub then replaces that runner instead of refusing its name.
func Register(ctx context.Context, server, code, name, prev string) (Credentials, error) {
	u, err := CheckServer(server)
	if err != nil {
		return Credentials{}, err
	}
	body, _ := json.Marshal(map[string]string{"code": code, "name": name, "previous": prev})
	req, err := http.NewRequestWithContext(ctx, "POST", u.String()+"/runner/register", bytes.NewReader(body))
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return Credentials{}, err
	}
	defer res.Body.Close()
	var out struct {
		ID, Name, Token, Error string
		OrgID                  int64 `json:"org_id"`
		UserID                 int64 `json:"user_id"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != http.StatusOK || out.Token == "" {
		if out.Error == "" {
			out.Error = res.Status
		}
		return Credentials{}, fmt.Errorf("register with %s: %s", u, out.Error)
	}
	return Credentials{Server: u.String(), ID: out.ID, Name: out.Name, Token: out.Token, OrgID: out.OrgID, UserID: out.UserID}, nil
}

// LoadCredentials reads credentials saved by SaveCredentials.
func LoadCredentials(path string) (Credentials, error) {
	var c Credentials
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

// SaveCredentials writes c readable by this user only.
func SaveCredentials(path string, c Credentials) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	return fsutil.WriteFileAtomic(path, b, 0o600)
}

// Client keeps a runner's tunnel to its hub open and serves the hub's
// requests with Handler.
type Client struct {
	Creds   Credentials
	Handler http.Handler
	// OnState is called with true when the tunnel comes up and false when
	// it drops (optional).
	OnState func(connected bool)
	// MinBackoff and MaxBackoff bound the wait between reconnects
	// (defaults 1s and 1m).
	MinBackoff, MaxBackoff time.Duration
	Dialer                 *websocket.Dialer
	// Previews are the previews this runner started; the hub reaches them
	// over a second connection (preview.go). Nil: no preview connection.
	Previews PreviewDialer
	// Version is this runner's Dash version, told to the hub on connect.
	Version string
	// OnPreviewURL is told, on every connect, where the hub serves this
	// runner's previews (see PreviewHeader; "" if it doesn't). Optional.
	OnPreviewURL func(template string)
}

func (c *Client) dialer() *websocket.Dialer {
	if c.Dialer != nil {
		return c.Dialer
	}
	return &websocket.Dialer{HandshakeTimeout: 20 * time.Second, Proxy: http.ProxyFromEnvironment,
		ReadBufferSize: 32 << 10, WriteBufferSize: 32 << 10}
}

// Run connects, serves until the tunnel drops, and reconnects with
// exponential backoff, until ctx ends or the hub revokes the token.
func (c *Client) Run(ctx context.Context) error {
	minB, maxB := c.MinBackoff, c.MaxBackoff
	if minB <= 0 {
		minB = time.Second
	}
	if maxB <= 0 {
		maxB = time.Minute
	}
	backoff := minB
	for {
		start := time.Now()
		err := c.serveOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrRevoked) {
			return err
		}
		if time.Since(start) > 30*time.Second {
			backoff = minB // it was up for a while: this is a fresh outage
		}
		// Jitter, so runners don't all knock at once after a hub restart.
		wait := backoff/2 + rand.N(backoff/2+1)
		slog.Warn("runner: hub connection lost, retrying", "err", err, "in", wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		backoff = min(backoff*2, maxB)
	}
}

func (c *Client) serveOnce(ctx context.Context) error {
	u, err := CheckServer(c.Creds.Server)
	if err != nil {
		return err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path += "/runner/connect"
	hdr := http.Header{"Authorization": {"Bearer " + c.Creds.Token}}
	if c.Version != "" {
		hdr.Set(VersionHeader, c.Version)
	}
	ws, res, err := c.dialer().DialContext(ctx, u.String(), hdr)
	if err != nil {
		if res != nil && res.StatusCode == http.StatusUnauthorized {
			return ErrRevoked
		}
		if res != nil {
			return fmt.Errorf("connect: %s", res.Status)
		}
		return err
	}
	sess := NewSession(ws, false)
	defer sess.Close()
	slog.Info("runner: connected to hub", "server", c.Creds.Server, "as", c.Creds.Name)
	if c.OnPreviewURL != nil {
		c.OnPreviewURL(res.Header.Get(PreviewHeader))
	}
	if c.Previews != nil {
		pctx, cancel := context.WithCancel(ctx)
		go c.keepPreviews(pctx, u.String())
		defer cancel()
		go func() { <-sess.Done(); cancel() }()
	}
	if c.OnState != nil {
		c.OnState(true)
		defer c.OnState(false)
	}
	srv := &http.Server{
		Handler:           c.Handler,
		ReadHeaderTimeout: 30 * time.Second,
	}
	stop := context.AfterFunc(ctx, func() { sess.Close() })
	defer stop()
	go func() { <-sess.Done(); _ = srv.Close() }()
	_ = srv.Serve(sess)
	return ErrSessionClosed
}

// Report posts a runner's report (see Hub.ServeReport) to the hub in creds.
func Report(ctx context.Context, creds Credentials, report any) error {
	return ReportReply(ctx, creds, report, nil)
}

// ReportReply is Report, decoding the hub's answer into reply (when the hub
// gives one; an older hub answers 204 and reply is left as it is).
func ReportReply(ctx context.Context, creds Credentials, report, reply any) error {
	u, err := CheckServer(creds.Server)
	if err != nil {
		return err
	}
	b, err := json.Marshal(report)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u.String()+"/runner/report", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return ErrRevoked
	}
	if res.StatusCode >= 300 {
		var e struct{ Error string }
		_ = json.NewDecoder(res.Body).Decode(&e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return fmt.Errorf("report to hub: %s", e.Error)
	}
	if reply != nil && res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(reply); err != nil {
			return fmt.Errorf("report to hub: its answer: %w", err)
		}
	}
	return nil
}

// BoardPath is where a runner sends the Dash board tool calls (MCP
// JSON-RPC) of the hub's work it runs: the hub's tasks and the chats the
// hub asks for. The hub serves them on its own board.
const BoardPath = "/runner/mcp/dash"

// Which hub work a board call is for: the hub's ids of its task (a task's
// run) or of the chat's project (none: the general chat).
const (
	BoardTodoHeader    = "X-Dash-Hub-Todo"
	BoardProjectHeader = "X-Dash-Hub-Project"
)

// Board posts body, an MCP request of a run of the hub's task hubTodo
// (or, without one, of a chat in the hub's project hubProject), to the hub
// in creds. The caller closes the answer's body.
func Board(ctx context.Context, creds Credentials, hubTodo, hubProject string, body []byte) (*http.Response, error) {
	u, err := CheckServer(creds.Server)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u.String()+BoardPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	if hubTodo != "" {
		req.Header.Set(BoardTodoHeader, hubTodo)
	} else if hubProject != "" {
		req.Header.Set(BoardProjectHeader, hubProject)
	}
	res, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("the hub's board: %w", err)
	}
	if res.StatusCode == http.StatusUnauthorized {
		res.Body.Close()
		return nil, ErrRevoked
	}
	return res, nil
}
