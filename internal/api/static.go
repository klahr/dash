package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	versionOnce sync.Once
	versionStr  string
)

// version identifies the frontend build: a hash of every static file. The
// page carries the version it was loaded with and asks /api/version now and
// then, so an installed app notices an update instead of running an old copy.
// It is not the release (buildinfo.Version, also in /api/version): a build
// from source is always "dev", and the version doubles as every static
// file's ETag, so it must change whenever a file does.
func (s *Server) version() string {
	versionOnce.Do(func() {
		h := sha256.New()
		var paths []string
		_ = fs.WalkDir(s.Static, ".", func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				paths = append(paths, p)
			}
			return nil
		})
		sort.Strings(paths)
		for _, p := range paths {
			b, _ := fs.ReadFile(s.Static, p)
			h.Write([]byte(p))
			h.Write(b)
		}
		versionStr = hex.EncodeToString(h.Sum(nil))[:12]
	})
	return versionStr
}

var localeTag = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// timeLocale is this machine's time format locale (LC_TIME) as a BCP 47 tag,
// e.g. "sv_SE.UTF-8" -> "sv-SE". Browsers format with their UI language and
// ignore it, so an English browser would show 12-hour times. "" when unset.
func timeLocale() string {
	v := os.Getenv("LC_ALL")
	if v == "" {
		v = os.Getenv("LC_TIME")
	}
	if v == "" {
		v = os.Getenv("LANG")
	}
	v, _, _ = strings.Cut(v, ".")
	v, _, _ = strings.Cut(v, "@")
	v = strings.ReplaceAll(v, "_", "-")
	if !localeTag.MatchString(v) {
		return ""
	}
	return v
}

// pages are the HTML files stamped with the version and branding, by path.
var pages = map[string]string{
	"/":            "index.html",
	"/index.html":  "index.html",
	"/login.html":  "login.html",
	"/invite.html": "invite.html",
}

// manifest serves the web app manifest with the product's name, so an
// installed app is called what the deployment is called.
func (s *Server) manifest(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.Static, "manifest.webmanifest")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err == nil {
		name := s.Brand.WithDefaults().Name
		m["name"], m["short_name"] = name, name
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if enc.Encode(m) == nil {
			b = out.Bytes()
		}
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	http.ServeContent(w, r, "manifest.webmanifest", time.Time{}, bytes.NewReader(b))
}

// static serves the frontend. Everything is revalidated on every load
// (no-cache), and index.html gets the build version stamped in.
func (s *Server) static() http.Handler {
	files := http.FileServerFS(s.Static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		if name, ok := pages[r.URL.Path]; ok {
			b, err := fs.ReadFile(s.Static, name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			b = bytes.ReplaceAll(b, []byte("{{DASH_VERSION}}"), []byte(s.version()))
			b = bytes.ReplaceAll(b, []byte("{{DASH_TIME_LOCALE}}"), []byte(timeLocale()))
			b = s.Brand.WithDefaults().Page(b)
			// No ETag: the branding can change without the files changing.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
			return
		}
		if r.URL.Path == "/manifest.webmanifest" {
			s.manifest(w, r)
			return
		}
		if r.URL.Path == "/sw.js" {
			w.Header().Set("Service-Worker-Allowed", "/")
		}
		// Any change to the frontend changes the version, so it validates
		// every file: a revalidation is a 304, not the whole (large) bundle.
		w.Header().Set("ETag", `"`+s.version()+`"`)
		files.ServeHTTP(w, r)
	})
}
