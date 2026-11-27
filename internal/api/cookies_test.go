package api

import (
	"net"
	"net/http"
	"net/url"

	"github.com/klahr/dash/internal/auth"
)

// Over plain HTTP the session cookie is named after the port Dash listens on.
// A session signed in through a ResponseRecorder (no listener) has the
// port-less name; these helpers rename it for a real test server.

// addCookies adds cs to r, which a test server accepted.
func addCookies(r *http.Request, cs []*http.Cookie) {
	port := ""
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		_, port, _ = net.SplitHostPort(a.String())
	}
	for _, c := range cs {
		r.AddCookie(forPort(c, port))
	}
}

// cookieHeader is a Cookie header carrying cs to the server at serverURL.
func cookieHeader(serverURL string, cs []*http.Cookie) http.Header {
	u, _ := url.Parse(serverURL)
	hdr := http.Header{}
	for _, c := range cs {
		c = forPort(c, u.Port())
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	return hdr
}

func forPort(c *http.Cookie, port string) *http.Cookie {
	if c.Name != auth.PlainCookieName {
		return c
	}
	return &http.Cookie{Name: auth.PlainCookieNameFor(port), Value: c.Value}
}
