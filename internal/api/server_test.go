package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/perm"
)

// Routes panics at startup if two patterns conflict; build it here.
func TestRoutesRegister(t *testing.T) {
	a, err := auth.New("u", "p")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Hub: hub.New(), Auth: a, Perms: perm.NewBroker(nil), Static: fstest.MapFS{"index.html": {Data: []byte("ok")}}}
	h := s.Routes()

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/projects", http.StatusUnauthorized},     // needs a session
		{"POST", "/mcp/permissions", http.StatusUnauthorized}, // needs a run token, not a session
		{"GET", "/mcp/permissions", http.StatusMethodNotAllowed},
		{"GET", "/login.html", http.StatusNotFound}, // public, just missing from this test FS
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.want {
			t.Errorf("%s %s: want %d, got %d", tc.method, tc.path, tc.want, w.Code)
		}
	}
}
