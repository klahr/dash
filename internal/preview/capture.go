package preview

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// Draw mode in Dash freezes the preview into a picture to draw on. Dash's page
// can't read the pixels of a page from another origin, so the proxy adds a
// small script to every HTML page it serves: when Dash's page asks (by
// postMessage, from an allowed origin), it renders the page's viewport to a
// PNG with modern-screenshot and posts it back. It does nothing else.
const capturePath = "/__dash/capture.js"

//go:embed capture/modern-screenshot.js
var screenshotJS string // modern-screenshot 4.7.0 (MIT, see capture/modern-screenshot.LICENSE)

//go:embed capture/capture.js
var captureJS string

// parentOrigins says who may ask for a capture, as exact origins (scheme,
// host and port): Dash's own origin, the todo's org's, and https on the host
// the preview domain hangs off (dash.example.com for preview.dash.example.com),
// which is where docs/previews.md puts Dash.
func (m *Manager) parentOrigins(label string) []string {
	origins := []string{}
	add := func(o string) {
		if o != "" && !slices.Contains(origins, o) {
			origins = append(origins, o)
		}
	}
	add(m.dashOrigin())
	add(m.originFor(label)) // the todo's org's subdomain
	if p := m.parentHost(); p != "" {
		add("https://" + p)
	}
	return origins
}

func (m *Manager) serveCapture(w http.ResponseWriter, label string) {
	cfg, _ := json.Marshal(map[string][]string{"origins": m.parentOrigins(label)})
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// The library is a UMD bundle: hide the page's module loaders and globals
	// from it, so it neither registers with them nor adds a global.
	fmt.Fprintf(w, "(function () {\nvar ms = (function () { var define, module, exports, globalThis, box = {};\n(function () {\n%s\n}).call(box);\nreturn box.modernScreenshot; })();\nvar CFG = %s;\n%s\n})();\n", screenshotJS, cfg, captureJS)
}

// wantsDocument reports whether r is a page load (in a tab or a frame), the
// only responses the capture script is added to.
func wantsDocument(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if d := r.Header.Get("Sec-Fetch-Dest"); d != "" {
		return d == "document" || d == "iframe" || d == "frame"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

var (
	headRE = regexp.MustCompile(`(?i)<head(\s[^>]*)?>`)
	htmlRE = regexp.MustCompile(`(?i)<html(\s[^>]*)?>`)
)

// sniffLimit is how far into a page the proxy looks for <head> or <html>.
const sniffLimit = 64 << 10

func newNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawStdEncoding.EncodeToString(b)
}

// injectCapture adds the capture script to an HTML page right after its
// <head> (or <html>) tag, and lets the page's CSP allow it. Fragments (no
// <head> or <html> near the start, e.g. htmx partials) and encodings it can't
// read are passed through untouched.
func injectCapture(res *http.Response) error {
	if res.Request == nil || !wantsDocument(res.Request) || res.StatusCode < 200 || res.StatusCode >= 300 || res.StatusCode == http.StatusNoContent {
		return nil
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "text/html") {
		return nil
	}
	body, decoded := res.Body, false
	switch strings.ToLower(res.Header.Get("Content-Encoding")) {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(body)
		if err != nil {
			return nil // let the browser make what it can of it
		}
		body, decoded = readCloser{zr, res.Body}, true
	default:
		return nil
	}
	// Read until the tag shows up: a streaming server sends the head first.
	var buf []byte
	var loc []int
	chunk := make([]byte, 8<<10)
	for len(buf) < sniffLimit {
		n, err := body.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if loc = headRE.FindIndex(buf); loc != nil || err != nil {
			break
		}
	}
	if loc == nil {
		loc = htmlRE.FindIndex(buf) // a page without a <head> tag
	}
	if loc != nil {
		nonce := newNonce()
		tag := []byte(`<script src="` + capturePath + `" nonce="` + nonce + `" async></script>`)
		buf = append(buf[:loc[1]:loc[1]], append(tag, buf[loc[1]:]...)...)
		if csps := res.Header.Values("Content-Security-Policy"); len(csps) > 0 {
			res.Header.Del("Content-Security-Policy")
			for _, c := range csps {
				res.Header.Add("Content-Security-Policy", allowCapture(c, nonce))
			}
		}
		res.Header.Del("ETag") // the page differs from the app's
	}
	if loc != nil || decoded {
		res.Header.Del("Content-Encoding")
		res.Header.Del("Content-Length")
		res.ContentLength = -1
	}
	res.Body = readCloser{io.MultiReader(bytes.NewReader(buf), body), res.Body}
	return nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

// allowCapture changes a page's CSP so the capture script loads and works:
//   - the script: by nonce where the policy already uses nonces or hashes
//     (adding one elsewhere would switch off 'unsafe-inline'), else by 'self';
//   - its rendering: an SVG drawn into a canvas, as data: and blob: images;
//   - Trusted Types are dropped (the library writes markup by hand).
func allowCapture(csp, nonce string) string {
	dirs := parseCSP(csp)
	script := pick(dirs, "script-src-elem", "script-src", "default-src")
	if script != nil {
		add := "'self'"
		if script.has("'strict-dynamic'") || script.hasPrefix("'nonce-") || script.hasPrefix("'sha") {
			add = "'nonce-" + nonce + "'"
		}
		script.drop("'none'")
		script.add(add)
		dirs = set(dirs, script, "script-src")
	}
	if img := pick(dirs, "img-src", "default-src"); img != nil {
		img.drop("'none'")
		img.add("data:", "blob:")
		dirs = set(dirs, img, "img-src")
	}
	if c := pick(dirs, "connect-src", "default-src"); c != nil {
		// The library fetches the page's images and fonts to embed them.
		c.drop("'none'")
		c.add("'self'", "data:", "blob:")
		dirs = set(dirs, c, "connect-src")
	}
	var out []string
	for _, d := range dirs {
		if d.name == "require-trusted-types-for" || d.name == "trusted-types" {
			continue
		}
		out = append(out, d.String())
	}
	return strings.Join(out, "; ")
}

type directive struct {
	name    string
	sources []string
}

func (d *directive) has(s string) bool {
	for _, x := range d.sources {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func (d *directive) hasPrefix(p string) bool {
	for _, x := range d.sources {
		if strings.HasPrefix(strings.ToLower(x), p) {
			return true
		}
	}
	return false
}

func (d *directive) drop(s string) {
	keep := d.sources[:0]
	for _, x := range d.sources {
		if !strings.EqualFold(x, s) {
			keep = append(keep, x)
		}
	}
	d.sources = keep
}

func (d *directive) add(ss ...string) {
	for _, s := range ss {
		if !d.has(s) {
			d.sources = append(d.sources, s)
		}
	}
}

func (d directive) String() string {
	return strings.Join(append([]string{d.name}, d.sources...), " ")
}

func parseCSP(csp string) []*directive {
	var out []*directive
	for _, part := range strings.Split(csp, ";") {
		f := strings.Fields(part)
		if len(f) == 0 {
			continue
		}
		out = append(out, &directive{name: strings.ToLower(f[0]), sources: f[1:]})
	}
	return out
}

// pick returns a copy of the first of names the policy has: the directive
// that governs a kind of request.
func pick(dirs []*directive, names ...string) *directive {
	for _, n := range names {
		for _, d := range dirs {
			if d.name == n {
				return &directive{name: d.name, sources: append([]string(nil), d.sources...)}
			}
		}
	}
	return nil
}

// set writes d back into the policy: in its own place, or, when d was the
// default-src fallback, as a new directive named name.
func set(dirs []*directive, d *directive, name string) []*directive {
	if d.name != "default-src" {
		name = d.name
	}
	for _, x := range dirs {
		if x.name == name {
			x.sources = d.sources
			return dirs
		}
	}
	return append(dirs, &directive{name: name, sources: d.sources})
}
