package api

import (
	"bytes"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/klahr/dash/internal/branding"
	"github.com/klahr/dash/internal/runnerctl"
	"github.com/klahr/dash/web"
)

var (
	landingPage = template.Must(template.New("landing").Parse(web.Landing))
	legalPage   = template.Must(template.New("legal").Parse(web.Legal))
)

// landingData is what the landing page shows.
type landingData struct {
	branding.Config
	Logo        string
	Signup      bool // open signup: show Sign up
	Origin      string
	SourceLabel string
	Footer      template.HTML
}

// landing serves the landing page (s.Brand.Landing): at / to visitors who
// aren't signed in. It has no scripts and nothing from other sites.
func (s *Server) landing() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		b := s.Brand.WithDefaults()
		origin, _ := runnerctl.HubURL(r, s.PublicURL, s.Auth.Secure(r), s.Auth.TrustProxy)
		label := "Source code"
		if u, err := url.Parse(b.SourceURL); err == nil && (u.Host == "github.com" || strings.HasSuffix(u.Host, ".github.com")) {
			label = "Source code on GitHub"
		}
		var buf bytes.Buffer
		if err := landingPage.Execute(&buf, landingData{
			Config: b, Logo: b.LogoURL(), Signup: s.Auth.SignupOpen(), Origin: origin, SourceLabel: label, Footer: b.Footer(),
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Signing in changes what / is: never serve this from a cache.
		w.Header().Set("Cache-Control", "no-cache, private")
		w.Header().Set("Vary", "Cookie")
		http.ServeContent(w, r, "landing.html", time.Time{}, bytes.NewReader(buf.Bytes()))
	})
}

// logo serves the configured logo (branding.LogoPath), or 404 without one.
func (s *Server) logo(w http.ResponseWriter, r *http.Request) {
	if s.Brand.Logo == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, s.Brand.Logo)
}

// legal serves one of the hub's own legal pages (branding.TermsPath,
// branding.PrivacyPath): the HTML file the operator wrote, in a page with
// the brand and the footer, or 404 without one. The file is read on every
// request, so an edit shows without a restart.
func (s *Server) legal(title string, file func(branding.Config) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b := s.Brand.WithDefaults()
		path := file(b)
		if path == "" {
			http.NotFound(w, r)
			return
		}
		body, err := os.ReadFile(path)
		if err != nil {
			slog.Error("legal page", "page", title, "err", err)
			http.Error(w, "this page can't be read right now", http.StatusInternalServerError)
			return
		}
		var buf bytes.Buffer
		if err := legalPage.Execute(&buf, struct {
			branding.Config
			Title  string
			Logo   string
			Body   template.HTML // the operator's own file, trusted like the binary's flags
			Footer template.HTML
		}{b, title, b.LogoURL(), template.HTML(body), b.Footer()}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "legal.html", time.Time{}, bytes.NewReader(buf.Bytes()))
	}
}
