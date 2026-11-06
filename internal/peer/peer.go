// Package peer lets one Dash show other computers' Dash servers in the same
// dashboard. Each computer keeps its own repositories, worktrees and Claude
// login; the Dash you open (the hub) forwards /peer/<name>/… to the other
// one with a shared token. Run the peers behind HTTPS, e.g. Tailscale.
package peer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Peer is another computer running Dash.
type Peer struct {
	Name string
	URL  *url.URL
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Parse reads a -peer flag value: "name=https://host:port".
func Parse(s string) (Peer, error) {
	name, raw, ok := strings.Cut(s, "=")
	if !ok {
		return Peer{}, fmt.Errorf("peer %q: want name=https://host", s)
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if !nameRe.MatchString(name) {
		return Peer{}, fmt.Errorf("peer name %q: use lowercase letters, digits and dashes", name)
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Peer{}, fmt.Errorf("peer %s: %q is not an http(s) URL", name, raw)
	}
	return Peer{Name: name, URL: u}, nil
}

// Machine is one computer in the dashboard.
type Machine struct {
	Name   string `json:"name"`
	Local  bool   `json:"local"`           // the Dash serving this page
	Prefix string `json:"prefix"`          // path prefix for its API: "" or "/peer/<name>"
	OK     bool   `json:"ok"`              // reachable and accepting our token
	Error  string `json:"error,omitempty"` // why not
	URL    string `json:"url,omitempty"`   // where it runs (peers only)
}

// Hub forwards requests to peers and reports which machines are reachable.
type Hub struct {
	Self  string // this machine's name
	Token string // shared with the peers (their DASH_PEER_TOKEN)
	Peers []Peer

	client  *http.Client
	proxies map[string]*httputil.ReverseProxy
	once    sync.Once
}

func (h *Hub) init() {
	h.once.Do(func() {
		h.client = &http.Client{Timeout: 5 * time.Second}
		h.proxies = map[string]*httputil.ReverseProxy{}
		for _, p := range h.Peers {
			h.proxies[p.Name] = h.proxy(p)
		}
	})
}

func (h *Hub) proxy(p Peer) *httputil.ReverseProxy {
	prefix := "/peer/" + p.Name
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(p.URL)
			r.Out.URL.Path = strings.TrimRight(p.URL.Path, "/") + strings.TrimPrefix(r.In.URL.Path, prefix)
			r.Out.URL.RawPath = ""
			// The browser's session belongs to the hub; the peer gets the shared token.
			r.Out.Header.Del("Cookie")
			r.Out.Header.Del("Origin") // token-authenticated, not a cross-site browser request
			r.Out.Header.Set("Authorization", "Bearer "+h.Token)
			r.Out.Host = p.URL.Host
		},
		ModifyResponse: func(res *http.Response) error {
			res.Header.Del("Set-Cookie")
			if res.StatusCode == http.StatusUnauthorized {
				// Don't let the browser think its own session expired.
				res.StatusCode = http.StatusBadGateway
				res.Status = "502 Bad Gateway"
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"error":%q}`, p.Name+" is unreachable: "+err.Error())
		},
		FlushInterval: -1, // stream WebSocket and long responses immediately
	}
}

// ServeHTTP handles /peer/{name}/….
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.init()
	px, ok := h.proxies[r.PathValue("name")]
	if !ok {
		http.Error(w, `{"error":"no such computer"}`, http.StatusNotFound)
		return
	}
	px.ServeHTTP(w, r)
}

// Machines lists this computer and every peer, checking the peers now.
func (h *Hub) Machines(ctx context.Context) []Machine {
	h.init()
	out := make([]Machine, 1+len(h.Peers))
	out[0] = Machine{Name: h.Self, Local: true, OK: true}
	var wg sync.WaitGroup
	for i, p := range h.Peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := Machine{Name: p.Name, Prefix: "/peer/" + p.Name, URL: p.URL.String()}
			if err := h.ping(ctx, p); err != nil {
				m.Error = err.Error()
			} else {
				m.OK = true
			}
			out[i+1] = m
		}()
	}
	wg.Wait()
	return out
}

func (h *Hub) ping(ctx context.Context, p Peer) error {
	req, err := http.NewRequestWithContext(ctx, "GET", p.URL.String()+"/api/me", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	res, err := h.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("unreachable (%v)", err)
	}
	res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return errors.New("rejected the token; set the same DASH_PEER_TOKEN on both computers")
	case res.StatusCode != http.StatusOK:
		return fmt.Errorf("answered %s", res.Status)
	}
	return nil
}
