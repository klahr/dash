package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

// static serves the frontend. Everything is revalidated on every load
// (no-cache), and index.html gets the build version stamped in.
func (s *Server) static() http.Handler {
	files := http.FileServerFS(s.Static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			b, err := fs.ReadFile(s.Static, "index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			b = bytes.ReplaceAll(b, []byte("{{DASH_VERSION}}"), []byte(s.version()))
			b = bytes.ReplaceAll(b, []byte("{{DASH_TIME_LOCALE}}"), []byte(timeLocale()))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(b))
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
