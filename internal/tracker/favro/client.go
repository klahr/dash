package favro

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is Favro's REST API.
const DefaultBaseURL = "https://favro.com/api/v1"

// Client is a Favro REST API client for one account (user email + API
// token) in one organization. Its requests are authenticated with HTTP
// basic auth and the organizationId header, follow Favro's paging
// (requestId + page) and back off on rate limits: the backoff is the
// account's, shared by every client with the same email and token.
//
// Errors never carry the token.
type Client struct {
	BaseURL string
	Email   string
	Token   string
	OrgID   string
	HTTP    *http.Client

	acct *account
	// sleep waits (tests make it instant and record it).
	sleep func(ctx context.Context, d time.Duration) error

	mu sync.Mutex
	// backend is Favro's X-Favro-Backend-Identifier, sent back so the pages
	// of one listing are served by the backend that holds it.
	backend string
}

// maxRetries is how often a rate-limited or failed (5xx) request is tried again.
const maxRetries = 5

// maxBackoff caps one wait for the rate limit to reset.
const maxBackoff = 2 * time.Minute

// NewClient makes a client for an account in an organization (org may be
// empty for the calls that list organizations).
func NewClient(baseURL, email, token, org string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"), Email: email, Token: token, OrgID: org,
		HTTP: &http.Client{Timeout: 30 * time.Second},
		acct: accountFor(baseURL, email, token),
	}
}

// ---- rate limits, per account ----

// account is the rate-limit state of one Favro account: Favro counts
// requests per user and token, so every project using them waits together.
type account struct {
	mu    sync.Mutex
	until time.Time // no requests before then
}

var accounts sync.Map // key → *account

func accountFor(baseURL, email, token string) *account {
	sum := sha256.Sum256([]byte(baseURL + "\x00" + strings.ToLower(email) + "\x00" + token))
	a, _ := accounts.LoadOrStore(hex.EncodeToString(sum[:]), &account{})
	return a.(*account)
}

func (a *account) holdUntil(t time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t.After(a.until) {
		a.until = t
	}
}

func (a *account) wait() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return time.Until(a.until)
}

func (c *Client) pause(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if d > maxBackoff {
		d = maxBackoff
	}
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// parseReset reads X-RateLimit-Reset (a date, or seconds since the epoch)
// or Retry-After (seconds, or a date).
func parseReset(h http.Header, now time.Time) (time.Time, bool) {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return now.Add(time.Duration(n) * time.Second), true
		}
		if t, err := http.ParseTime(v); err == nil {
			return t, true
		}
	}
	v := strings.TrimSpace(h.Get("X-RateLimit-Reset"))
	if v == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC1123, time.RFC1123Z} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n > 1e12 { // milliseconds
			return time.UnixMilli(n), true
		}
		return time.Unix(n, 0), true
	}
	return time.Time{}, false
}

// ---- requests ----

// APIError is a request Favro refused.
type APIError struct {
	Method, Path string
	Status       int
	Message      string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("favro: %s %s: %d %s", e.Method, e.Path, e.Status, http.StatusText(e.Status))
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// IsNotFound reports whether err is Favro's 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// do sends one request and decodes the answer into out (may be nil).
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	for attempt := 0; ; attempt++ {
		if err := c.pause(ctx, c.acct.wait()); err != nil {
			return err
		}
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return err
		}
		req.SetBasicAuth(c.Email, c.Token)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.OrgID != "" {
			req.Header.Set("organizationId", c.OrgID)
		}
		c.mu.Lock()
		if c.backend != "" {
			req.Header.Set("X-Favro-Backend-Identifier", c.backend)
		}
		c.mu.Unlock()
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// The URL carries no secrets; drop the token-free *url.Error wrapper's noise.
			return fmt.Errorf("favro: %s %s: %w", method, path, unwrapURLError(err))
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if id := resp.Header.Get("X-Favro-Backend-Identifier"); id != "" {
			c.mu.Lock()
			c.backend = id
			c.mu.Unlock()
		}
		now := time.Now()
		reset, hasReset := parseReset(resp.Header, now)
		if rem := resp.Header.Get("X-RateLimit-Remaining"); rem != "" && hasReset {
			if n, err := strconv.Atoi(strings.TrimSpace(rem)); err == nil && n <= 0 {
				c.acct.holdUntil(reset) // the last request allowed until it resets
			}
		}
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if retry && attempt < maxRetries {
			wait := time.Duration(1<<attempt) * time.Second
			if resp.StatusCode == http.StatusTooManyRequests && hasReset && reset.After(now) {
				wait = reset.Sub(now)
			}
			if resp.StatusCode == http.StatusTooManyRequests {
				c.acct.holdUntil(now.Add(wait))
			} else if err := c.pause(ctx, wait); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return &APIError{Method: method, Path: path, Status: resp.StatusCode, Message: errorMessage(data)}
		}
		if out == nil || len(bytes.TrimSpace(data)) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("favro: %s %s: bad answer: %w", method, path, err)
		}
		return nil
	}
}

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// errorMessage is Favro's error text, short.
func errorMessage(data []byte) string {
	var e struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	msg := ""
	if json.Unmarshal(data, &e) == nil {
		msg = e.Message
		if msg == "" {
			msg = e.Error
		}
	} else {
		msg = strings.TrimSpace(string(data))
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

// page is one page of a Favro listing.
type page struct {
	Limit     int               `json:"limit"`
	Page      int               `json:"page"`
	Pages     int               `json:"pages"`
	RequestID string            `json:"requestId"`
	Entities  []json.RawMessage `json:"entities"`
}

// maxPages stops a listing that never ends.
const maxPages = 1000

// list reads every page of a listing and calls each for every entity.
func (c *Client) list(ctx context.Context, path string, q url.Values, each func(json.RawMessage) error) error {
	if q == nil {
		q = url.Values{}
	}
	var p page
	if err := c.do(ctx, http.MethodGet, path, q, nil, &p); err != nil {
		return err
	}
	for n := 0; ; n++ {
		for _, e := range p.Entities {
			if err := each(e); err != nil {
				return err
			}
		}
		next := p.Page + 1
		if next >= p.Pages || p.RequestID == "" || n >= maxPages {
			return nil
		}
		nq := url.Values{}
		for k, v := range q {
			nq[k] = v
		}
		nq.Set("requestId", p.RequestID)
		nq.Set("page", strconv.Itoa(next))
		reqID := p.RequestID
		p = page{}
		if err := c.do(ctx, http.MethodGet, path, nq, nil, &p); err != nil {
			return err
		}
		if p.RequestID == "" {
			p.RequestID = reqID
		}
		if p.Page < next { // a server that doesn't echo the page
			p.Page = next
		}
	}
}

// listAll decodes every entity of a listing into a slice of T.
func listAll[T any](ctx context.Context, c *Client, path string, q url.Values) ([]T, error) {
	var out []T
	err := c.list(ctx, path, q, func(raw json.RawMessage) error {
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("favro: %s: bad entity: %w", path, err)
		}
		out = append(out, v)
		return nil
	})
	return out, err
}
