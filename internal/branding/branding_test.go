package branding

import (
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

func TestFooterEscapes(t *testing.T) {
	f := string(Config{SourceURL: `https://x.example/"><script>`, Contact: "a@b.example"}.WithDefaults().Footer())
	if strings.Contains(f, "<script>") || !strings.Contains(f, `href="mailto:a@b.example"`) {
		t.Fatal(f)
	}
}
