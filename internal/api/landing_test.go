package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/branding"
	"github.com/klahr/dash/web"
)

// brandedServer is boardServer with the real frontend and brand b.
func brandedServer(t *testing.T, b branding.Config) (*Server, http.Handler) {
	t.Helper()
	s, _, _ := boardServer(t)
	s.Static, s.Brand = web.Static, b.WithDefaults()
	return s, s.Routes()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestLandingOffByDefault(t *testing.T) {
	_, h := brandedServer(t, branding.Config{})
	w := get(t, h, "/")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login.html" {
		t.Fatalf("want / to go to the sign-in page, got %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestLandingOn(t *testing.T) {
	_, h := brandedServer(t, branding.Config{Name: "Acme Agents", Landing: true, TeamPrice: "€8 per seat / month",
		TermsURL: "https://acme.example/terms", PrivacyURL: "https://acme.example/privacy"})
	w := get(t, h, "/")
	if w.Code != http.StatusOK {
		t.Fatalf("landing: %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{
		"<title>Acme Agents: ", "no open ports", "€8 per seat / month", "AGPL",
		`href="` + branding.DefaultSourceURL + `"`, "Source code on GitHub",
		`href="https://acme.example/terms"`, `href="https://acme.example/privacy"`, `href="/login.html"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("landing page lacks %q", want)
		}
	}
	// Signup is off here: no Sign up button.
	if strings.Contains(body, "#signup") {
		t.Error("landing page offers signup while it's closed")
	}
	// Nothing from other sites: no fonts, scripts or trackers.
	for _, bad := range []string{"<script", "fonts.googleapis", "cdn.", "<link rel=\"stylesheet\""} {
		if strings.Contains(body, bad) {
			t.Errorf("landing page has %q", bad)
		}
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-cache") {
		t.Errorf("landing page may be cached: %q", w.Header().Get("Cache-Control"))
	}
	// Its screenshots are public, like the page.
	for _, img := range []string{"board.webp", "board-crop.webp", "review.webp", "phone.webp"} {
		if !strings.Contains(body, `/img/landing/`+img) {
			t.Errorf("landing page doesn't show %s", img)
		}
		if w := get(t, h, "/img/landing/"+img); w.Code != http.StatusOK {
			t.Errorf("/img/landing/%s without a session: %d", img, w.Code)
		}
	}
	// Other pages still need a session.
	if w := get(t, h, "/index.html"); w.Code != http.StatusSeeOther {
		t.Errorf("/index.html without a session: %d", w.Code)
	}
}

func TestLandingSeePricingWithoutPrice(t *testing.T) {
	_, h := brandedServer(t, branding.Config{Landing: true, PricingURL: "https://acme.example/pricing"})
	body := get(t, h, "/").Body.String()
	if !strings.Contains(body, `<a href="https://acme.example/pricing">see pricing</a>`) {
		t.Errorf("want a see-pricing link:\n%s", body)
	}
	if !strings.Contains(body, "<title>Dash: ") {
		t.Error("want the default name")
	}
}

func TestSignedInBypassesLanding(t *testing.T) {
	_, h := brandedServer(t, branding.Config{Name: "Acme Agents", Landing: true})
	w := get(t, loggedIn(t, h), "/")
	if w.Code != http.StatusOK {
		t.Fatalf("board: %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `id="view-board"`) || strings.Contains(body, "no open ports") {
		t.Fatal("a signed-in user should get the board, not the landing page")
	}
	if !strings.Contains(body, "<title>Acme Agents</title>") {
		t.Error("board title isn't branded")
	}
}

func TestPagesCarryTheBrand(t *testing.T) {
	logo := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(logo, []byte("\x89PNG\r\n\x1a\nfake"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, h := brandedServer(t, branding.Config{Name: "Acme <Agents>", Logo: logo, Contact: "help@acme.example",
		SourceURL: "https://git.acme.example/dash", TermsURL: "https://acme.example/terms"})
	for _, path := range []string{"/login.html", "/invite.html"} {
		w := get(t, h, path)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, w.Code)
		}
		body := w.Body.String()
		for _, want := range []string{
			"Acme &lt;Agents&gt;", `src="` + branding.LogoPath + `"`, `class="site-footer"`,
			`href="https://git.acme.example/dash"`, `href="https://acme.example/terms"`, `href="mailto:help@acme.example"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %q", path, want)
			}
		}
		if strings.Contains(body, "{{DASH_") || strings.Contains(body, "<!--DASH_FOOTER-->") || strings.Contains(body, "<span>Dash</span>") {
			t.Errorf("%s has an unbranded placeholder or name", path)
		}
		if strings.Contains(body, "Privacy") {
			t.Errorf("%s links a privacy policy that isn't set", path)
		}
	}
	// The logo is public, like the sign-in page.
	if w := get(t, h, branding.LogoPath); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("logo: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w := get(t, h, "/manifest.webmanifest"); !strings.Contains(w.Body.String(), `"name": "Acme <Agents>"`) {
		t.Errorf("manifest: %s", w.Body)
	}
}

func TestDefaultFooterHasSourceLink(t *testing.T) {
	_, h := brandedServer(t, branding.Config{})
	body := get(t, h, "/login.html").Body.String()
	if !strings.Contains(body, `<a href="`+branding.DefaultSourceURL+`" rel="noopener">Source code</a></footer>`) {
		t.Errorf("want only the source link in the footer:\n%s", body)
	}
	if w := get(t, h, branding.LogoPath); w.Code != http.StatusNotFound {
		t.Errorf("logo without one: %d", w.Code)
	}
}
