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
	s.deleteProject(w, asOwner(t, s, r))
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d %s", w.Code, w.Body)
	}
	if _, err := raw(s).GetProject(p.ID); err != nil {
		t.Fatalf("project gone after a wrong password: %v", err)
	}

	// For a runner's project the hub checks the password and doesn't forward a wrong one.
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

// The hub's password check must not depend on how the path is spelled: the
// proxy forwards the decoded path, so "projects%2F<pid>" still deletes.
func TestRunnerDeleteProjectNeedsPasswordHowEverSpelled(t *testing.T) {
	h, _, _, work := remoteServer(t)
	h = loggedIn(t, h)
	for _, p := range []string{
		"/peer/work/api/projects/" + work.ID,
		"/peer/work/api/projects%2F" + work.ID,
		"/peer/work/api%2Fprojects%2F" + work.ID,
		"/peer/work/api/projects/" + work.ID + "/",
	} {
		w := do(t, h, "DELETE", p, `{"password":"wrong","delete_files":true}`)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: want 403, got %d %s", p, w.Code, w.Body)
		}
		if w := do(t, h, "GET", "/peer/work/api/projects/"+work.ID, ""); w.Code != http.StatusOK {
			t.Fatalf("%s: project gone after a wrong password: %d %s", p, w.Code, w.Body)
		}
	}
	// Other deletes on a runner don't ask for the password.
	if w := do(t, h, "DELETE", "/peer/work/api/projects/"+work.ID+"/chat", ""); w.Code == http.StatusForbidden {
		t.Errorf("chat clear asked for a password: %d %s", w.Code, w.Body)
	}
}
