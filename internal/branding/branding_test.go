package branding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	c := Config{Name: "  ", TermsURL: " https://a.example/terms "}.WithDefaults()
	if c.Name != DefaultName || c.SourceURL != DefaultSourceURL || c.TermsURL != "https://a.example/terms" {
		t.Fatalf("%+v", c)
	}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	if c.LogoURL() != "/favicon.svg" {
		t.Fatal(c.LogoURL())
	}
}

func TestCheck(t *testing.T) {
	err := Config{SourceURL: "javascript:alert(1)", PrivacyURL: "example.com/p", Contact: "Ann <ann@example.com>", Logo: "/nonexistent/logo.png"}.Check()
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{"source URL", "privacy URL", "contact email", "logo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("no complaint about %s: %v", want, err)
		}
	}
}

func TestSignupNote(t *testing.T) {
	if n := (Config{}).WithDefaults().SignupNote(); n != "" {
		t.Fatalf("note without terms or privacy: %q", n)
	}
	both := string(Config{TermsURL: "https://a.example/terms", PrivacyURL: "https://a.example/privacy"}.WithDefaults().SignupNote())
	for _, want := range []string{`href="https://a.example/terms"`, `href="https://a.example/privacy"`, "you accept the", "privacy policy"} {
		if !strings.Contains(both, want) {
			t.Errorf("no %s in %s", want, both)
		}
	}
	only := string(Config{PrivacyURL: "https://a.example/privacy"}.WithDefaults().SignupNote())
	if strings.Contains(only, "accept") || !strings.Contains(only, "privacy policy") {
		t.Fatal(only)
	}
	page := string(Config{TermsURL: "https://a.example/terms"}.WithDefaults().Page([]byte("<form><!--DASH_SIGNUP_NOTE--></form>")))
	if !strings.Contains(page, `class="signup-note"`) {
		t.Fatal(page)
	}
}

func TestMissingLegal(t *testing.T) {
	if m := (Config{}).MissingLegal(); len(m) != 3 {
		t.Fatal(m)
	}
	if m := (Config{TermsURL: "https://a.example/t", PrivacyURL: "https://a.example/p", Contact: "a@b.example"}).MissingLegal(); len(m) != 0 {
		t.Fatal(m)
	}
}

func TestFooterEscapes(t *testing.T) {
	f := string(Config{SourceURL: `https://x.example/"><script>`, Contact: "a@b.example"}.WithDefaults().Footer())
	if strings.Contains(f, "<script>") || !strings.Contains(f, `href="mailto:a@b.example"`) {
		t.Fatal(f)
	}
}

func TestLegalFiles(t *testing.T) {
	f := filepath.Join(t.TempDir(), "terms.html")
	if err := os.WriteFile(f, []byte("<p>x</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Config{TermsFile: f, PrivacyFile: f}.WithDefaults()
	if c.TermsURL != TermsPath || c.PrivacyURL != PrivacyPath {
		t.Fatalf("files should link the hub's own pages: %q %q", c.TermsURL, c.PrivacyURL)
	}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	if m := c.MissingLegal(); len(m) != 1 {
		t.Fatal(m) // only the contact
	}
	if err := (Config{TermsFile: f, TermsURL: "https://a.example/terms"}).WithDefaults().Check(); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("a URL and a file: %v", err)
	}
	if err := (Config{PrivacyFile: filepath.Join(t.TempDir(), "nope.html")}).WithDefaults().Check(); err == nil || !strings.Contains(err.Error(), "privacy file") {
		t.Errorf("a missing file: %v", err)
	}
}
