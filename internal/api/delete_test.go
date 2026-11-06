package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeleteProjectNeedsPassword(t *testing.T) {
	s, _, p := boardServer(t)
	r := httptest.NewRequest("DELETE", "/api/projects/"+p.ID, strings.NewReader(`{"password":"wrong"}`))
	r.SetPathValue("pid", p.ID)
	w := httptest.NewRecorder()
	s.deleteProject(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d %s", w.Code, w.Body)
	}
	if _, err := s.Store.GetProject(p.ID); err != nil {
		t.Fatalf("project gone after a wrong password: %v", err)
	}

	// For a peer's project the hub checks the password and doesn't forward a wrong one.
	_, hub, _, work := remoteServer(t)
	r = httptest.NewRequest("DELETE", "/peer/work/api/projects/"+work.ID, strings.NewReader(`{"password":"wrong"}`))
	r.SetPathValue("name", "work")
	r.SetPathValue("pid", work.ID)
	w = httptest.NewRecorder()
	hub.confirmThenForward(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("hub: want 403, got %d %s", w.Code, w.Body)
	}
}
