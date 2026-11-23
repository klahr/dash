package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/store"
)

func TestStarredProjectsAPI(t *testing.T) {
	s, _, p := boardServer(t)
	h := s.mux()
	acc := raw(s).Accounts()
	owner := ownerSession(t, s)
	tok, _, err := acc.InviteWith(owner.User.ID, owner.Org.ID, "c@example.com", store.RoleMember, true)
	if err != nil {
		t.Fatal(err)
	}
	cUser, _, err := acc.RegisterWithInvitation(tok, "c", "c-password")
	if err != nil {
		t.Fatal(err)
	}
	cm, err := acc.Membership(cUser.ID, owner.Org.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := store.Session{User: cUser, Org: &cm}

	call := func(sess store.Session, method, path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), sess)))
		return w
	}
	list := func(sess store.Session) []string {
		t.Helper()
		w := call(sess, "GET", "/api/me/starred-projects")
		var res struct {
			Projects []string `json:"projects"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.Projects == nil {
			t.Fatalf("list: %d %s", w.Code, w.Body)
		}
		return res.Projects
	}

	if got := list(owner); len(got) != 0 {
		t.Fatalf("none yet: %v", got)
	}
	// A project of this Dash, and a runner's (its slash escaped), twice.
	for _, path := range []string{"/api/me/starred-projects/" + p.ID, "/api/me/starred-projects/build-box%2Fabc123", "/api/me/starred-projects/" + p.ID} {
		if w := call(owner, "PUT", path); w.Code != http.StatusNoContent {
			t.Fatalf("PUT %s: %d %s", path, w.Code, w.Body)
		}
	}
	if got, want := list(owner), []string{p.ID, "build-box/abc123"}; !slices.Equal(got, want) {
		t.Fatalf("starred %v, want %v", got, want)
	}
	// A key that names no project is refused.
	if w := call(owner, "PUT", "/api/me/starred-projects/a%2Fb%2Fc"); w.Code != http.StatusBadRequest {
		t.Fatalf("bad key: %d %s", w.Code, w.Body)
	}

	// Another member neither sees nor changes them.
	if got := list(c); len(got) != 0 {
		t.Fatalf("c sees %v", got)
	}
	if w := call(c, "DELETE", "/api/me/starred-projects/"+p.ID); w.Code != http.StatusNoContent {
		t.Fatalf("c's DELETE: %d %s", w.Code, w.Body)
	}
	if w := call(c, "PUT", "/api/me/starred-projects/build-box%2Fother"); w.Code != http.StatusNoContent {
		t.Fatalf("c's PUT: %d %s", w.Code, w.Body)
	}
	if got, want := list(owner), []string{p.ID, "build-box/abc123"}; !slices.Equal(got, want) {
		t.Fatalf("after c: %v, want %v", got, want)
	}
	if got, want := list(c), []string{"build-box/other"}; !slices.Equal(got, want) {
		t.Fatalf("c's: %v, want %v", got, want)
	}

	// Unstarring.
	if w := call(owner, "DELETE", "/api/me/starred-projects/"+p.ID); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", w.Code, w.Body)
	}
	if got, want := list(owner), []string{"build-box/abc123"}; !slices.Equal(got, want) {
		t.Fatalf("after unstar %v, want %v", got, want)
	}

	// The person's API token works too.
	secret, _, err := acc.CreateToken(store.NewToken{Kind: store.TokenAPI, Name: "ci", UserID: owner.User.ID, OrgID: owner.Org.ID, Scopes: store.UserScopes}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PUT", "/api/me/starred-projects/"+p.ID, nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("with a token: %d %s", w.Code, w.Body)
	}
	if got, want := list(owner), []string{"build-box/abc123", p.ID}; !slices.Equal(got, want) {
		t.Fatalf("after the token's star %v, want %v", got, want)
	}
}
