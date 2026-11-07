package preview

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// How a browser signs in to a preview host. Dash's session cookie belongs to
// Dash's own host only, so a preview host gets a cookie of its own:
//
//  1. A request without it is redirected to Dash: /preview/<todo>?next=<path>.
//     That page needs a Dash session like any other (or sends you to log in).
//  2. Dash redirects back to <preview>/__dash/auth with a token valid for two
//     minutes, signed for that preview host only.
//  3. The preview host turns the token into its cookie and redirects to <path>.
//
// The key signing both is made fresh on every start of Dash, so a restart
// signs every preview out (the next request goes through the steps again).
const (
	cookieName = "dash_preview"
	authPath   = "/__dash/auth"
	tokenTTL   = 2 * time.Minute
	cookieTTL  = 12 * time.Hour
)

var labelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func newKey() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}

func (m *Manager) sign(purpose, label string, exp time.Time) string {
	e := strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, m.key)
	mac.Write([]byte(purpose + "|" + label + "|" + e))
	return e + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (m *Manager) verify(purpose, label, v string) bool {
	e, _, ok := strings.Cut(v, ".")
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(e, 10, 64)
	if err != nil || time.Now().After(time.Unix(n, 0)) {
		return false
	}
	return hmac.Equal([]byte(v), []byte(m.sign(purpose, label, time.Unix(n, 0))))
}

// AuthURL is where Dash sends a signed-in browser to open todoID's preview
// at path next.
func (m *Manager) AuthURL(todoID, next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		next = "/"
	}
	base := strings.TrimSuffix(m.URL(todoID), "/")
	return base + authPath + "?t=" + url.QueryEscape(m.sign("t", todoID, time.Now().Add(tokenTTL))) + "&next=" + url.QueryEscape(next)
}

// reqHost is the host (and port) the browser asked for.
func (m *Manager) reqHost(r *http.Request) string {
	if m.TrustProxy {
		if f := r.Header.Get("X-Forwarded-Host"); f != "" {
			return strings.TrimSpace(strings.Split(f, ",")[0])
		}
	}
	return r.Host
}

// host is the host the browser asked for, without port.
func (m *Manager) host(r *http.Request) string {
	h := m.reqHost(r)
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// label returns the todo id a preview host is for.
func (m *Manager) label(host string) (string, bool) {
	if m.Domain == "" {
		return "", false
	}
	l, ok := strings.CutSuffix(host, "."+m.Domain)
	if !ok || !labelRE.MatchString(l) {
		return "", false
	}
	return l, true
}

func (m *Manager) secure(r *http.Request) bool {
	if m.Secure != nil {
		return m.Secure(r)
	}
	return r.TLS != nil
}

// SetOrigin records the origin Dash's UI is served from, as r reached it:
// unauthenticated preview requests are sent there to sign in, and previews
// are served with its scheme and port.
func (m *Manager) SetOrigin(r *http.Request) {
	if m == nil {
		return
	}
	h := m.reqHost(r)
	if _, ok := m.label(m.host(r)); ok || h == "" {
		return
	}
	scheme := "http"
	if m.secure(r) {
		scheme = "https"
	}
	m.origin.Store(scheme + "://" + h)
}

// dashOrigin is Dash's own origin: as last seen, else guessed from a preview
// domain of the form "preview.<dash host>".
func (m *Manager) dashOrigin() string {
	if o, _ := m.origin.Load().(string); o != "" {
		return o
	}
	if rest, ok := strings.CutPrefix(m.Domain, "preview."); ok && strings.Contains(rest, ".") {
		return "https://" + rest
	}
	return ""
}

// originFor is the Dash origin label's preview belongs with: OriginFor's
// for its todo when set, else dashOrigin.
func (m *Manager) originFor(label string) string {
	if m.OriginFor != nil {
		id := label
		if _, todoID, ok := m.remoteLabel(label); ok {
			id = todoID
		}
		if o := m.OriginFor(id); o != "" {
			return o
		}
	}
	return m.dashOrigin()
}

// Wrap serves preview hosts with the proxy and passes everything else to
// next (Dash itself). Without a preview domain it returns next unchanged.
func (m *Manager) Wrap(next http.Handler) http.Handler {
	if m == nil || m.Domain == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := m.host(r)
		if label, ok := m.label(host); ok {
			m.serve(w, r, label)
			return
		}
		// A page served from a preview is same-site with Dash, so the browser
		// would send it Dash's SameSite cookie: don't let one drive the API,
		// including over a WebSocket (whose handshake is a GET).
		if o := r.Header.Get("Origin"); o != "" && (r.Method != http.MethodGet && r.Method != http.MethodHead || isUpgrade(r)) {
			if u, err := url.Parse(o); err == nil {
				if _, fromPreview := m.label(strings.ToLower(u.Hostname())); fromPreview {
					http.Error(w, "previews can't call Dash", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Manager) serve(w http.ResponseWriter, r *http.Request, label string) {
	secure := m.secure(r)
	dashOrigin := m.originFor(label)
	if r.URL.Path == authPath {
		q := r.URL.Query()
		if !m.verify("t", label, q.Get("t")) {
			page(w, http.StatusForbidden, "Link expired", "This preview link has expired. Open the preview again from Dash.", dashOrigin, false)
			return
		}
		ss := http.SameSiteLaxMode
		if secure {
			ss = http.SameSiteNoneMode // also inside Dash's iframe when previews live on another site
		}
		http.SetCookie(w, &http.Cookie{
			Name: cookieName, Value: m.sign("c", label, time.Now().Add(cookieTTL)), Path: "/",
			MaxAge: int(cookieTTL.Seconds()), HttpOnly: true, Secure: secure, SameSite: ss,
		})
		next := q.Get("next")
		if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
			next = "/"
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, next, http.StatusFound)
		return
	}
	if c, err := r.Cookie(cookieName); err != nil || !m.verify("c", label, c.Value) {
		origin := dashOrigin
		if origin != "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) && !isUpgrade(r) {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, origin+"/preview/"+label+"?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			return
		}
		page(w, http.StatusUnauthorized, "Sign in to Dash", "Open this preview from Dash to sign in to it.", origin, false)
		return
	}
	if r.URL.Path == capturePath {
		m.serveCapture(w, label)
		return
	}
	origin := "http://" + m.reqHost(r)
	if secure {
		origin = "https://" + m.reqHost(r)
	}
	e := m.entry(label)
	if e == nil {
		if runner, todoID, ok := m.remoteLabel(label); ok {
			m.serveRemote(w, r, label, runner, todoID, origin)
			return
		}
		page(w, http.StatusNotFound, "Preview not running", "No preview is running for this task. Start one with Preview on the task in Dash.", dashOrigin, false)
		return
	}
	e.mu.Lock()
	state, addr, port := e.info.State, e.addr, e.info.Port
	e.mu.Unlock()
	switch {
	case state == "starting":
		page(w, http.StatusServiceUnavailable, "Starting…", "The preview server is starting, waiting for its port to answer. This page reloads by itself.", "", true)
		return
	case state != "live" || addr == "":
		page(w, http.StatusBadGateway, "Preview crashed", "The preview server stopped. Its log is in Dash, on the task.", dashOrigin, false)
		return
	}
	e.touch()
	m.proxy(addr, port, origin, nil).ServeHTTP(touchWriter{w, e}, r)
}

func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// proxy forwards to the server at addr as if the browser had opened
// http://localhost:<port> (dev servers often only allow that host), and
// rewrites what points back at localhost to the preview's origin. transport
// connects to addr (nil: over TCP).
func (m *Manager) proxy(addr string, port int, origin string, transport http.RoundTripper) *httputil.ReverseProxy {
	local := fmt.Sprintf("localhost:%d", port)
	locals := []string{"http://" + local, "http://127.0.0.1:" + strconv.Itoa(port), "http://[::1]:" + strconv.Itoa(port)}
	return &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: "http", Host: addr})
			pr.SetXForwarded()
			pr.Out.Host = local
			u, _ := url.Parse(origin)
			pr.Out.Header.Set("X-Forwarded-Host", u.Host)
			pr.Out.Header.Set("X-Forwarded-Proto", u.Scheme)
			// Dash's own cookies are none of the app's business.
			var keep []string
			for _, c := range pr.In.Cookies() {
				if c.Name != cookieName && c.Name != "dash_session" {
					keep = append(keep, c.Name+"="+c.Value)
				}
			}
			pr.Out.Header.Del("Cookie")
			if len(keep) > 0 {
				pr.Out.Header.Set("Cookie", strings.Join(keep, "; "))
			}
			if pr.In.Header.Get("Origin") == origin {
				pr.Out.Header.Set("Origin", "http://"+local) // dev servers check WebSocket origins
			}
			if ref := pr.In.Header.Get("Referer"); strings.HasPrefix(ref, origin) {
				pr.Out.Header.Set("Referer", "http://"+local+strings.TrimPrefix(ref, origin))
			}
			if wantsDocument(pr.In) {
				pr.Out.Header.Set("Accept-Encoding", "gzip") // pages get the capture script added (capture.go)
			}
		},
		ModifyResponse: func(res *http.Response) error {
			// It's shown in Dash's iframe.
			res.Header.Del("X-Frame-Options")
			if csp := res.Header.Get("Content-Security-Policy"); csp != "" {
				res.Header.Set("Content-Security-Policy", dropDirective(csp, "frame-ancestors"))
			}
			if loc := res.Header.Get("Location"); loc != "" {
				for _, l := range locals {
					if rest, ok := strings.CutPrefix(loc, l); ok {
						res.Header.Set("Location", origin+rest)
						break
					}
				}
			}
			return injectCapture(res)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			m.remoteError(w, err) // "not answering", unless a runner said why
		},
		FlushInterval: -1, // server-sent events (some dev servers' reload channel)
	}
}

func dropDirective(csp, name string) string {
	var keep []string
	for _, d := range strings.Split(csp, ";") {
		if f := strings.Fields(d); len(f) > 0 && strings.EqualFold(f[0], name) {
			continue
		}
		if strings.TrimSpace(d) != "" {
			keep = append(keep, strings.TrimSpace(d))
		}
	}
	return strings.Join(keep, "; ")
}

// touchWriter counts WebSocket traffic (hot reload) as activity, so an open
// preview isn't stopped as idle.
type touchWriter struct {
	http.ResponseWriter
	e *entry
}

func (t touchWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

func (t touchWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, brw, err := http.NewResponseController(t.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	return touchConn{c, t.e}, brw, nil
}

type touchConn struct {
	net.Conn
	e *entry
}

func (c touchConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.e.touch()
	}
	return n, err
}

func (c touchConn) Write(b []byte) (int, error) {
	c.e.touch()
	return c.Conn.Write(b)
}

// page writes a small status page in the preview's place.
func page(w http.ResponseWriter, code int, title, msg, dash string, refresh bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	meta := ""
	if refresh {
		meta = `<meta http-equiv="refresh" content="1">`
	}
	link := ""
	if dash != "" {
		link = `<p><a href="` + html.EscapeString(dash) + `/">Open Dash</a></p>`
	}
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">%s<title>%s</title>
<style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#101722;color:#e6ecf5;font:15px/1.5 system-ui,sans-serif}main{max-width:32em;padding:24px}h1{font-size:18px;margin:0 0 6px}p{color:#8a9ab2;margin:0 0 8px}a{color:#8aa4ff}</style></head>
<body><main><h1>%s</h1><p>%s</p>%s</main></body></html>`, meta, html.EscapeString(title), html.EscapeString(title), html.EscapeString(msg), link)
}
