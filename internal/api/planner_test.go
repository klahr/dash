package api

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/store"
)

// planner makes a planner, pat, in the default org, and returns the router
// signed in as them.
func plannerOf(t *testing.T, s *Server, h http.Handler) (http.Handler, store.User) {
	t.Helper()
	acc := raw(s).Accounts()
	owner := ownerSession(t, s)
	tok, _, err := acc.InviteWith(owner.User.ID, owner.Org.ID, "pat@example.com", store.RolePlanner, true)
	if err != nil {
		t.Fatal(err)
	}
	u, _, err := acc.RegisterWithInvitation(tok, "pat", "pat-password")
	if err != nil {
		t.Fatal(err)
	}
	return loginAs(t, h, "pat", "pat-password"), u
}

var wildcard = regexp.MustCompile(`\{[^}]*\}`)

// TestPlannerGateRefusesEveryOtherRoute walks every route the API registers
// and calls it as a planner: each route that isn't in plannerRoutes must be
// refused by the gate (403, code "planner"), so a route added later is
// refused to planners until it is added there on purpose. Routes that take
// no session at all (webhooks with their own secrets, sign-in pages) answer
// a signed-out caller as they answer a planner: the gate doesn't change
// what anyone may do there.
func TestPlannerGateRefusesEveryOtherRoute(t *testing.T) {
	s, _, _ := boardServer(t)
	h := s.Routes()
	ph, _ := plannerOf(t, s, h)
	if len(s.routes) < 100 {
		t.Fatalf("only %d routes recorded", len(s.routes))
	}
	refused := 0
	for _, pattern := range s.routes {
		if plannerRoutes[pattern] {
			continue
		}
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			method, path = "GET", pattern
		}
		path = wildcard.ReplaceAllString(path, "x")
		w := do(t, ph, method, path, "{}")
		if w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), `"code":"planner"`) {
			refused++
			continue
		}
		// Not refused: only fine for a route anyone reaches signed out.
		if anon := do(t, h, method, path, "{}"); anon.Code == http.StatusUnauthorized && strings.Contains(anon.Body.String(), "login required") {
			t.Errorf("%s: a planner got %d %.200s", pattern, w.Code, w.Body)
		}
	}
	if refused < 100 {
		t.Errorf("only %d routes refused", refused)
	}

	// Every route a planner may use is a route (no typo leaves one out),
	// except those that depend on how this Dash is set up.
	registered := map[string]bool{}
	for _, p := range s.routes {
		registered[p] = true
	}
	for p := range plannerRoutes {
		if !registered[p] && !strings.Contains(p, "/auth/google") {
			t.Errorf("%s is allowed for planners but isn't a route", p)
		}
	}
}

// A planner lands on Planning and uses it; projects, tasks, chats and the
// remote MCP tools are refused, and a member is unaffected.
func TestPlannerUsesPlanningOnly(t *testing.T) {
	s, _, p := boardServer(t)
	h := s.Routes()
	ph, _ := plannerOf(t, s, h)

	w := do(t, ph, "GET", "/", "")
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/planning.html" {
		t.Errorf("a planner's /: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := do(t, loggedIn(t, h), "GET", "/", ""); w.Code == http.StatusFound {
		t.Errorf("a member's / redirects too")
	}
	for _, e := range []endpoint{
		{"GET", "/api/projects", ""},
		{"GET", "/api/projects/" + p.ID + "/todos", ""},
		{"POST", "/api/projects/" + p.ID + "/todos", `{"title":"x"}`},
		{"GET", "/api/chat", ""},
		{"GET", "/api/goals", ""},
		{"GET", "/api/planning/work", ""},
		{"POST", "/api/planning/cards/x/links", `{"kind":"task","id":"x"}`},
		{"GET", "/api/machines", ""},
		{"GET", "/api/previews", ""},
		{"GET", "/api/tokens", ""},
	} {
		if w := do(t, ph, e.method, e.path, e.body); w.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d %.200s", e.method, e.path, w.Code, w.Body)
		}
	}
	if w := do(t, ph, "GET", "/api/me", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"role":"planner"`) {
		t.Errorf("/api/me: %d %.300s", w.Code, w.Body)
	}
	if w := do(t, ph, "POST", "/api/planning/cards", `{"title":"Export as PDF","project":"`+p.ID+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("create card: %d %s", w.Code, w.Body)
	}
	if w := do(t, ph, "GET", "/api/planning/projects", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), p.Name) {
		t.Errorf("projects: %d %s", w.Code, w.Body)
	}
}

// A planner has notification settings: card news, pushed. No summary,
// which tells of tasks they don't see.
func TestPlannerNotifySettings(t *testing.T) {
	s, _, _ := boardServer(t)
	h := s.Routes()
	ph, _ := plannerOf(t, s, h)
	w := do(t, ph, "GET", "/api/me/notifications", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"planner":true`) || !strings.Contains(w.Body.String(), `"cards":true`) {
		t.Fatalf("get: %d %.300s", w.Code, w.Body)
	}
	w = do(t, ph, "PUT", "/api/me/notifications", `{"cards":false,"summary":"daily","email":true}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"cards":false`) || !strings.Contains(w.Body.String(), `"summary":"off"`) {
		t.Fatalf("put: %d %.300s", w.Code, w.Body)
	}
	if w := do(t, ph, "POST", "/api/me/notifications/test-summary", ""); w.Code != http.StatusForbidden {
		t.Errorf("test summary: %d", w.Code)
	}
}
