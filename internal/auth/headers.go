package auth

import "net/http"

// ContentSecurityPolicy is the default policy of Dash's own pages and API
// answers. Handlers that need another (the connector's consent page,
// uploads, a runner's answers) set their own, replacing it.
//
//   - Script only from Dash itself: no inline script or eval, so injected
//     markup can't run.
//   - Inline styles stay allowed: the editors and the landing page use them.
//   - Pictures from anywhere over HTTPS (avatars, images in Markdown).
//   - Frames from anywhere: a task's live preview may be on another host.
//   - Dash itself may only be framed by its own pages (clickjacking).
const ContentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: https:; " +
	"media-src 'self' data: blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"worker-src 'self' blob:; " +
	"frame-src 'self' https: http:; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"frame-ancestors 'self'"

// HSTS is the Strict-Transport-Security of an internet-facing Dash: a year,
// for its own host only (org and preview subdomains decide for themselves).
const HSTS = "max-age=31536000"

// Headers adds Dash's security headers to every answer of next: a
// Content-Security-Policy and nosniff always, and HSTS when Dash is
// internet-facing (Public; browsers ignore it over plain HTTP). Handlers
// may replace them.
func (a *Auth) Headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		if a.Public {
			h.Set("Strict-Transport-Security", HSTS)
		}
		next.ServeHTTP(w, r)
	})
}

// secureCookie reports whether cookies set on r's answer are Secure: always
// on an internet-facing Dash, else when r came over HTTPS.
func (a *Auth) secureCookie(r *http.Request) bool {
	return a.Public || a.Secure(r)
}
