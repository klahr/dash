package preview

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func htmlResponse(t *testing.T, dest, ctype, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest("GET", "http://localhost:3000/", nil)
	if dest != "" {
		req.Header.Set("Sec-Fetch-Dest", dest)
	}
	res := &http.Response{StatusCode: 200, Header: http.Header{}, Request: req, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
	res.Header.Set("Content-Type", ctype)
	res.Header.Set("Content-Length", "123")
	for k, v := range hdr {
		res.Header.Set(k, v)
	}
	return res
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInjectCapture(t *testing.T) {
	t.Parallel()

	page := "<!doctype html><html lang=en><HEAD><title>x</title></HEAD><body>hi</body></html>"
	res := htmlResponse(t, "iframe", "text/html; charset=utf-8", page, nil)
	if err := injectCapture(res); err != nil {
		t.Fatal(err)
	}
	got := readBody(t, res)
	if !strings.HasPrefix(got, `<!doctype html><html lang=en><HEAD><script src="/__dash/capture.js" nonce="`) || !strings.HasSuffix(got, `async></script><title>x</title></HEAD><body>hi</body></html>`) {
		t.Fatalf("injected: %s", got)
	}
	if res.Header.Get("Content-Length") != "" || res.ContentLength != -1 {
		t.Fatalf("length kept: %v %d", res.Header, res.ContentLength)
	}

	// Gzipped pages are decoded and sent plain.
	var z bytes.Buffer
	zw := gzip.NewWriter(&z)
	io.WriteString(zw, page)
	zw.Close()
	res = htmlResponse(t, "document", "text/html", z.String(), map[string]string{"Content-Encoding": "gzip"})
	if err := injectCapture(res); err != nil {
		t.Fatal(err)
	}
	if got := readBody(t, res); !strings.Contains(got, capturePath) || res.Header.Get("Content-Encoding") != "" {
		t.Fatalf("gzip: %q %v", got, res.Header)
	}

	// Left alone: fragments, other resources, fetches, encodings it can't read.
	for name, res := range map[string]*http.Response{
		"fragment": htmlResponse(t, "document", "text/html", "<div>partial</div>", nil),
		"script":   htmlResponse(t, "script", "text/html", page, nil),
		"fetch":    htmlResponse(t, "empty", "text/html", page, nil),
		"json":     htmlResponse(t, "document", "application/json", page, nil),
		"brotli":   htmlResponse(t, "document", "text/html", page, map[string]string{"Content-Encoding": "br"}),
	} {
		before := res.Header.Get("Content-Length")
		if err := injectCapture(res); err != nil {
			t.Fatal(err)
		}
		if got := readBody(t, res); strings.Contains(got, capturePath) || res.Header.Get("Content-Length") != before {
			t.Errorf("%s: changed: %q", name, got)
		}
	}
}

func TestAllowCapture(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ in, want string }{
		// script-src gains 'self', images and fetches what rendering needs.
		{"default-src 'self'; script-src 'self' 'unsafe-inline' cdn.example",
			"default-src 'self'; script-src 'self' 'unsafe-inline' cdn.example; img-src 'self' data: blob:; connect-src 'self' data: blob:"},
		// A nonce-based policy gets our nonce instead ('self' wouldn't count).
		{"script-src 'nonce-abc' 'strict-dynamic'; img-src https:; connect-src 'none'",
			"script-src 'nonce-abc' 'strict-dynamic' 'nonce-N'; img-src https: data: blob:; connect-src 'self' data: blob:"},
		// 'none' is replaced; Trusted Types go.
		{"default-src 'none'; require-trusted-types-for 'script'; trusted-types x",
			"default-src 'none'; script-src 'self'; img-src data: blob:; connect-src 'self' data: blob:"},
		// script-src-elem governs <script src>.
		{"script-src-elem 'sha256-x'; style-src 'self'",
			"script-src-elem 'sha256-x' 'nonce-N'; style-src 'self'"},
	} {
		if got := allowCapture(c.in, "N"); got != c.want {
			t.Errorf("allowCapture(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestParentOrigins(t *testing.T) {
	t.Parallel()

	m, _, _ := setup(t, "preview.dash.example.com")
	m.SetOrigin(httptest.NewRequest("GET", "http://dash.example.com:7788/api/previews", nil))
	o, h := m.parentOrigins("")
	if len(o) != 1 || o[0] != "http://dash.example.com:7788" || len(h) != 1 || h[0] != "dash.example.com" {
		t.Fatalf("origins %v hosts %v", o, h)
	}
	// Never a bare TLD.
	m2, _, _ := setup(t, "preview.com")
	if _, h := m2.parentOrigins(""); len(h) != 0 {
		t.Fatalf("hosts for preview.com: %v", h)
	}
	w := httptest.NewRecorder()
	m.serveCapture(w, "")
	js := w.Body.String()
	if !strings.Contains(js, `var CFG = {"hosts":["dash.example.com"],"origins":["http://dash.example.com:7788"]}`) || !strings.Contains(js, "dash:capture") || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("capture.js: %s…", js[:200])
	}
}
