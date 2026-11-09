package tunnel

import (
	"net/http"
	"testing"
)

func TestSafeRunnerHeader(t *testing.T) {
	cases := []struct{ in, want, disp string }{
		{"application/json", "application/json; charset=utf-8", ""},
		{"text/plain; charset=utf-8", "text/plain; charset=utf-8", ""},
		{"image/png", "image/png", ""},
		{"text/html; charset=utf-8", "application/octet-stream", "attachment"},
		{"image/svg+xml", "application/octet-stream", "attachment"},
		{"application/javascript", "application/octet-stream", "attachment"},
		{"TEXT/HTML", "application/octet-stream", "attachment"},
		{"", "application/octet-stream", "attachment"},
		{"text/plain; charset=utf-8; foo", "application/octet-stream", "attachment"},
	}
	for _, c := range cases {
		h := http.Header{}
		if c.in != "" {
			h.Set("Content-Type", c.in)
		}
		h.Set("Set-Cookie", "dash_session=x")
		h.Set("Content-Security-Policy", "script-src *")
		SafeRunnerHeader(h, http.StatusOK)
		if got := h.Get("Content-Type"); got != c.want {
			t.Errorf("%q: Content-Type %q, want %q", c.in, got, c.want)
		}
		if got := h.Get("Content-Disposition"); got != c.disp {
			t.Errorf("%q: Content-Disposition %q, want %q", c.in, got, c.disp)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != RunnerCSP || h.Get("Set-Cookie") != "" {
			t.Errorf("%q: header %v", c.in, h)
		}
	}
}
