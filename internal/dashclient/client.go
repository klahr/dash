// Package dashclient is a client of a Dash server's REST API, logged in with a
// session cookie. dash-mcp uses it.
package dashclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Client talks to a Dash server with a session cookie.
type Client struct {
	base *url.URL
	http *http.Client

	mu     sync.Mutex
	prefix string // "" or "/peer/<name>": the computer later calls go to
}

// Machine mirrors peer.Machine: a computer in this dashboard.
type Machine struct {
	Name   string `json:"name"`
	Local  bool   `json:"local"`
	Prefix string `json:"prefix"`
	OK     bool   `json:"ok"`
	Error  string `json:"error"`
}

// hubOnly paths always go to the Dash we logged in to.
var hubOnly = regexp.MustCompile(`^/api/(login|logout|me|machines|version)\b`)

// ErrUnauthorized is returned when the session is missing or expired.
var ErrUnauthorized = errors.New("not logged in")

// NewClient returns a client of the Dash server at server.
func NewClient(server string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(server, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid server URL %q", server)
	}
	jar, _ := cookiejar.New(nil)
	return &Client{base: u, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}, nil
}

// SetPrefix sends later project calls to another computer ("" = this one).
func (c *Client) SetPrefix(p string) {
	c.mu.Lock()
	c.prefix = p
	c.mu.Unlock()
}

func (c *Client) at(path string) string {
	if hubOnly.MatchString(path) {
		return path
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.prefix + path
}

// Login starts a session.
func (c *Client) Login(username, password string) error {
	return c.do("POST", "/api/login", map[string]string{"username": username, "password": password}, nil)
}

// Machines lists the computers in this dashboard.
func (c *Client) Machines() (ms []Machine, err error) {
	return ms, c.do("GET", "/api/machines", nil, &ms)
}

// Call sends a request to path as given, ignoring SetPrefix.
func (c *Client) Call(method, path string, body, out any) error {
	return c.doRaw(method, path, body, out)
}

func (c *Client) do(method, path string, body, out any) error {
	return c.doRaw(method, c.at(path), body, out)
}

func (c *Client) doRaw(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base.String()+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode == http.StatusUnauthorized && path != "/api/login" {
		return ErrUnauthorized
	}
	if res.StatusCode >= 400 {
		var e struct{ Error string }
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("%s %s: %s", method, path, res.Status)
	}
	switch o := out.(type) {
	case nil:
	case *string:
		*o = string(data)
	default:
		return json.Unmarshal(data, out)
	}
	return nil
}

// PermRequest mirrors perm.Request.
type PermRequest struct {
	ID        string          `json:"id"`
	TodoID    string          `json:"todo_id"`
	ProjectID string          `json:"project_id"`
	Tool      string          `json:"tool"`
	Input     json.RawMessage `json:"input"`
	Created   time.Time       `json:"created"`
	Decision  string          `json:"decision"`
}

// Permissions lists the pending permission requests of a project ("" = all).
func (c *Client) Permissions(pid string) (ps []PermRequest, err error) {
	return ps, c.do("GET", "/api/permissions?project="+url.QueryEscape(pid), nil, &ps)
}

// Decide answers a permission request: allow, always or deny.
func (c *Client) Decide(id, decision string) error {
	return c.do("POST", "/api/permissions/"+id, map[string]string{"decision": decision}, nil)
}

// Usage mirrors usage.Snapshot.
type Usage struct {
	Known       bool   `json:"known"`
	ManualPause bool   `json:"manual_pause"`
	Paused      bool   `json:"paused"`
	Reason      string `json:"reason"`
	Windows     []struct {
		Name        string    `json:"name"`
		Utilization float64   `json:"utilization"`
		ResetsAt    time.Time `json:"resets_at"`
	} `json:"windows"`
}

// Usage returns the Claude subscription usage and the queue state.
func (c *Client) Usage() (u Usage, err error) { return u, c.do("GET", "/api/usage", nil, &u) }

// SetQueuePaused pauses or resumes starting new tasks.
func (c *Client) SetQueuePaused(paused bool) (u Usage, err error) {
	path := "/api/queue/resume"
	if paused {
		path = "/api/queue/pause"
	}
	return u, c.do("POST", path, nil, &u)
}
