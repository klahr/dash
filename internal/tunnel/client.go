package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"runtime"
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

// hubDo sends a request to the hub at server: body (if any) to path, with
// token (if any) as the bearer, the headers in hdr, through client (nil: one
// that gives up after 30 seconds). A 401 is ErrRevoked; any other answer is
// the caller's, who closes its body.
func hubDo(ctx context.Context, client *http.Client, server, token, method, path string, body io.Reader, hdr http.Header) (*http.Response, error) {
	u, err := CheckServer(server)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String()+path, body)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusUnauthorized {
		res.Body.Close()
		return nil, ErrRevoked
	}
	return res, nil
}

// HubJSON sends in (nil: no body) as JSON to path on the hub at server and
// decodes the answer into out (nil: not read), reading at most limit bytes
// of it (<= 0: 1 MiB). client nil is one that gives up after 30 seconds;
// token "" sends none. A 401 is ErrRevoked; another answer outside 2xx an
// error with the hub's {"error": …} (or its status). A 204, or a 2xx with no
// body, leaves out as it is. The answer is returned, its body closed, for
// its status and headers.
func HubJSON(ctx context.Context, client *http.Client, server, token, method, path string, in, out any, limit int64) (*http.Response, error) {
	var body io.Reader
	hdr := http.Header{"Accept": {"application/json"}}
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
		hdr.Set("Content-Type", "application/json")
	}
	res, err := hubDo(ctx, client, server, token, method, path, body, hdr)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if limit <= 0 {
		limit = 1 << 20
	}
	r := io.LimitReader(res.Body, limit)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var e struct{ Error string }
		_ = json.NewDecoder(r).Decode(&e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return res, &HubError{Status: res.StatusCode, Message: e.Error}
	}
	if out == nil || res.StatusCode == http.StatusNoContent {
		return res, nil
	}
	if err := json.NewDecoder(r).Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return res, fmt.Errorf("the hub's answer: %w", err)
	}
	return res, nil
}

// HubError is a hub's answer outside 2xx (but 401: ErrRevoked).
type HubError struct {
	Status  int    // the HTTP status
	Message string // the hub's {"error": …}, or the status
}

func (e *HubError) Error() string { return e.Message }

// Register swaps a registration code from the hub's Runners page for this
// runner's credentials. prev is the token this computer was registered with before, if any: the
// hub then replaces that runner instead of refusing its name.
func Register(ctx context.Context, server, code, name, prev string) (Credentials, error) {
	u, err := CheckServer(server)
	if err != nil {
		return Credentials{}, err
	}
	var out struct {
		ID, Name, Token string
		OrgID           int64 `json:"org_id"`
		UserID          int64 `json:"user_id"`
	}
	in := map[string]string{"code": code, "name": name, "previous": prev}
	if _, err := HubJSON(ctx, nil, u.String(), "", http.MethodPost, "/runner/register", in, &out, 0); err != nil {
		return Credentials{}, fmt.Errorf("register with %s: %w", u, err)
	}
	if out.Token == "" {
		return Credentials{}, fmt.Errorf("register with %s: the hub sent no token", u)
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
	// Platform is this runner's "os/arch", told to the hub on connect so the
	// Runners page can show it ("": runtime.GOOS/runtime.GOARCH).
	Platform string
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
	return reconnect(ctx, "hub", minB, maxB, c.serveOnce, func(err error) bool { return errors.Is(err, ErrRevoked) })
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
	platform := c.Platform
	if platform == "" {
		platform = runtime.GOOS + "/" + runtime.GOARCH
	}
	hdr.Set(PlatformHeader, platform)
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
	_, err := HubJSON(ctx, nil, creds.Server, creds.Token, http.MethodPost, "/runner/report", report, reply, 1<<20)
	if err != nil && !errors.Is(err, ErrRevoked) {
		return fmt.Errorf("report to hub: %w", err)
	}
	return err
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
	// BoardGoalHeader names, with BoardProjectHeader, the hub's goal whose
	// coordinator a chat runs for: its calls get that goal's tools.
	BoardGoalHeader = "X-Dash-Hub-Goal"
	// BoardApprovedHeader names, for a chat's call, the tool call
	// (update_project) its user approved on the runner, where the chat
	// runs and its approval card is answered.
	BoardApprovedHeader = "X-Dash-Hub-Approved"
)

type boardApprovedKey struct{}

// WithBoardApproved makes Board calls made with ctx say that the chat's
// user approved this call of tool.
func WithBoardApproved(ctx context.Context, tool string) context.Context {
	return context.WithValue(ctx, boardApprovedKey{}, tool)
}

type boardGoalKey struct{}

// WithBoardGoal makes Board calls made with ctx name hubGoal, the hub's
// goal whose coordinator the run is.
func WithBoardGoal(ctx context.Context, hubGoal string) context.Context {
	return context.WithValue(ctx, boardGoalKey{}, hubGoal)
}

// Board posts body, an MCP request of a run of the hub's task hubTodo
// (or, without one, of a chat in the hub's project hubProject), to the hub
// in creds. The caller closes the answer's body.
func Board(ctx context.Context, creds Credentials, hubTodo, hubProject string, body []byte) (*http.Response, error) {
	if _, err := CheckServer(creds.Server); err != nil {
		return nil, err
	}
	hdr := http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"}}
	if hubTodo != "" {
		hdr.Set(BoardTodoHeader, hubTodo)
	} else if hubProject != "" {
		hdr.Set(BoardProjectHeader, hubProject)
		if g, _ := ctx.Value(boardGoalKey{}).(string); g != "" {
			hdr.Set(BoardGoalHeader, g)
		}
	}
	if t, _ := ctx.Value(boardApprovedKey{}).(string); t != "" && hubTodo == "" {
		hdr.Set(BoardApprovedHeader, t)
	}
	// The answer may be a stream, passed on as it comes: hubDo, not HubJSON.
	res, err := hubDo(ctx, &http.Client{Timeout: 2 * time.Minute}, creds.Server, creds.Token, http.MethodPost, BoardPath, bytes.NewReader(body), hdr)
	if err != nil && !errors.Is(err, ErrRevoked) {
		return nil, fmt.Errorf("the hub's board: %w", err)
	}
	return res, err
}
