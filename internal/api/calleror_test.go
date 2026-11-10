package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/store"
)

// A signed-in user with no org gets 403 from every caller check, not the 401
// that would sign their browser out; a signed-out request gets 401.
func TestCallerChecksKeepOrglessUsersSignedIn(t *testing.T) {
	s := &Server{}
	checks := map[string]func(w http.ResponseWriter, r *http.Request) bool{
		"callerOr":      func(w http.ResponseWriter, r *http.Request) bool { _, ok := s.callerOr(w, r); return ok },
		"service":       func(w http.ResponseWriter, r *http.Request) bool { _, ok := s.service(w, r); return ok },
		"managedRunner": func(w http.ResponseWriter, r *http.Request) bool { _, _, ok := s.managedRunner(w, r); return ok },
		"ownedRunner":   func(w http.ResponseWriter, r *http.Request) bool { _, _, ok := s.ownedRunner(w, r); return ok },
		"wallAdmin":     func(w http.ResponseWriter, r *http.Request) bool { _, ok := s.wallAdmin(w, r); return ok },
	}
	for name, check := range checks {
		for _, tc := range []struct {
			who  string
			req  func(*http.Request) *http.Request
			want int
		}{
			{"signed out", func(r *http.Request) *http.Request { return r }, http.StatusUnauthorized},
			{"no org", func(r *http.Request) *http.Request {
				return r.WithContext(auth.WithSession(r.Context(), store.Session{User: store.User{ID: 1}}))
			}, http.StatusForbidden},
		} {
			w := httptest.NewRecorder()
			if check(w, tc.req(httptest.NewRequest("GET", "/", nil))) {
				t.Errorf("%s, %s: allowed", name, tc.who)
			}
			if w.Code != tc.want {
				t.Errorf("%s, %s: status %d, want %d", name, tc.who, w.Code, tc.want)
			}
		}
	}
}
