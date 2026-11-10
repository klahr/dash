package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/tunnel"
)

// The hub refuses projects on its own disk, and answers the
// working-tree routes of a project no runner can serve with a clear error,
// never by running git itself (its Git would fail the test: no repository).
func TestHubForwardsOrRefusesWorkingTreeRoutes(t *testing.T) {
	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	rh := &tunnel.Hub{Accounts: s.Auth.Accounts()}
	s.Peers = &peer.Hub{Self: "hub", Runners: rh}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest(method, path, strings.NewReader(body))))
		return w
	}

	for _, body := range []string{`{"name":"x"}`, `{"name":"x","path":"/tmp/x","existing":true}`, `{"name":"x","remote_url":"https://example.com/x.git"}`} {
		if w := call("POST", "/api/projects", body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "keeps no projects") {
			t.Errorf("POST /api/projects %s: %d %s", body, w.Code, w.Body)
		}
	}

	// p has no runner linked: everything that needs its working tree says so.
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/graph", ""}, {"GET", "/files", ""}, {"GET", "/file?path=README.md", ""}, {"GET", "/branches", ""},
		{"GET", "/remote", ""}, {"POST", "/push", ""}, {"GET", "/release", ""}, {"POST", "/processes", `{"preset":"test"}`},
		{"PUT", "/branch", `{"branch":"dev"}`}, {"POST", "/cleanup", ""},
	} {
		w := call(rt.method, "/api/projects/"+p.ID+rt.path, rt.body)
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "No runner is linked to this project") {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}

	// Linked to a runner that is offline: "no runner online".
	owner := testOwner(t, s)
	code, _, err := rh.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := rh.Register(code, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	s.Dispatch = nil // projectRunner needs a dispatcher to link: none online either way
	if _, err := raw(s).UpdateProject(p.ID, func(x *model.Project) error {
		x.Runners, x.RemoteURL = []string{id}, "https://example.com/web.git"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/api/projects/"+p.ID+"/graph", ""); w.Code != http.StatusServiceUnavailable ||
		!strings.Contains(w.Body.String(), "No runner is online for this project. Start one with `dash runner`.") {
		t.Errorf("graph with the runner offline: %d %s", w.Code, w.Body)
	}
	if w := call("DELETE", "/api/projects/"+p.ID, `{"password":"p","delete_files":true}`); w.Code != http.StatusBadRequest {
		t.Errorf("deleting a hub project's files: %d %s", w.Code, w.Body)
	}
}
