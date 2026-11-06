package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"sort"
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
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(b))
			return
		}
		if r.URL.Path == "/sw.js" {
			w.Header().Set("Service-Worker-Allowed", "/")
		}
		files.ServeHTTP(w, r)
	})
}
