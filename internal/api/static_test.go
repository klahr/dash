package api

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/branding"
)

// The app's own paths (a view, a task, a card) serve the app's page, as /
// does, so a reload or a shared link opens it; any other path still 404s,
// and the API, assets and other pages are what they were.
func TestAppPathsServeTheApp(t *testing.T) {
	s, h := brandedServer(t, branding.Config{})
	h = loggedIn(t, h)
	root := get(t, h, "/")
	if root.Code != http.StatusOK || !strings.Contains(root.Body.String(), `src="/app.js"`) {
		t.Fatalf("/: %d", root.Code)
	}
	for _, p := range []string{
		"/p/abc", "/p/abc/board", "/p/abc/history", "/p/abc/tasks/t1", "/p/abc/goals/g1", "/p/abc/history/0123abc",
		"/p/abc/issues/i1", "/p/abc@other%20computer/files", "/planning", "/planning/cards/c1", "/p/abc/board?board=b1",
	} {
		w := get(t, h, p)
		if w.Code != http.StatusOK || w.Body.String() != root.Body.String() {
			t.Errorf("%s: %d, the app's page: %v", p, w.Code, w.Body.String() == root.Body.String())
			continue
		}
		if !strings.Contains(w.Body.String(), s.version()) || w.Header().Get("Cache-Control") != "no-cache" ||
			!strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%s: not stamped and revalidated as /: %q %q", p, w.Header().Get("Cache-Control"), w.Header().Get("Content-Type"))
		}
		if w.Header().Get(AppShellHeader) != "1" {
			t.Errorf("%s: not marked as the app's shell", p)
		}
	}
	// The service worker caches only the app's page as its shell.
	if w := get(t, h, "/login.html"); w.Code != http.StatusOK || w.Header().Get(AppShellHeader) != "" {
		t.Errorf("/login.html: %d, marked as the shell: %q", w.Code, w.Header().Get(AppShellHeader))
	}
	for _, p := range []string{"/p", "/p/", "/p/abc/nope", "/p/abc/tasks", "/p/abc/tasks/t1/more", "/planning/", "/planning/cards/", "/nope", "/app/nope.js"} {
		if w := get(t, h, p); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, w.Code)
		}
	}
	if w := get(t, h, "/api/nope"); w.Code == http.StatusOK && strings.Contains(w.Body.String(), "<html") {
		t.Errorf("/api/nope served the app")
	}
	if w := get(t, h, "/style.css"); w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/css") {
		t.Errorf("/style.css: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w := get(t, h, "/app/router.js"); w.Code != http.StatusOK {
		t.Errorf("/app/router.js: %d", w.Code)
	}
}

// Signed out, a link into the app goes to the sign-in page, which comes
// back to it (path and query) afterwards.
func TestAppPathSignedOutGoesToLogin(t *testing.T) {
	_, h := brandedServer(t, branding.Config{})
	for _, p := range []string{"/p/abc/tasks/t1", "/planning/cards/c1?tag=ux", "/p/abc/board?board=b1&sheet=settings", "/?project=abc&task=t1"} {
		w := get(t, h, p)
		if want := "/login.html?next=" + url.QueryEscape(p); w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
			t.Errorf("%s: %d %q, want %q", p, w.Code, w.Header().Get("Location"), want)
		}
	}
	// Not for a plain / or a path that isn't the app's.
	if w := get(t, h, "/"); w.Header().Get("Location") != "/login.html" {
		t.Errorf("/: %q", w.Header().Get("Location"))
	}
	if w := get(t, h, "/p/abc/nope"); w.Header().Get("Location") != "/login.html" {
		t.Errorf("/p/abc/nope: %q", w.Header().Get("Location"))
	}
}

// A planner opening a link into the app gets their Planning page, with
// the card the link opens.
func TestAppPathPlanner(t *testing.T) {
	s, _, _ := boardServer(t)
	h := s.Routes()
	ph, _ := plannerOf(t, s, h)
	for p, want := range map[string]string{
		"/planning":           "/planning.html",
		"/planning/cards/c1":  "/planning.html?card=c1",
		"/p/abc/tasks/t1":     "/planning.html",
		"/planning/cards/a&b": "/planning.html?card=a%26b",
	} {
		if w := do(t, ph, "GET", p, ""); w.Code != http.StatusFound || w.Header().Get("Location") != want {
			t.Errorf("a planner's %s: %d %q, want %q", p, w.Code, w.Header().Get("Location"), want)
		}
	}
}
