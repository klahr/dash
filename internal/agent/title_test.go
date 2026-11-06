package agent

import (
	"context"
	"os"
	"testing"
)

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Add rate limiting to login\n":         "Add rate limiting to login",
		"\"Fix currency rounding.\"":           "Fix currency rounding",
		"**Validate payment requests**\nextra": "Validate payment requests",
	} {
		if got, err := cleanTitle(in); err != nil || got != want {
			t.Errorf("cleanTitle(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := cleanTitle("  \n"); err == nil {
		t.Error("empty output should be an error")
	}
}

// Calls the real CLI; opt in with DASH_REAL_CLAUDE=1.
func TestTitlerReal(t *testing.T) {
	if os.Getenv("DASH_REAL_CLAUDE") == "" {
		t.Skip("set DASH_REAL_CLAUDE=1 to call the claude CLI")
	}
	title, err := Titler{}.Title(context.Background(), "The login handler should reject more than 5 failed attempts per IP within 15 minutes and return 429 with Retry-After.")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("title: %q", title)
}

func TestCleanSummary(t *testing.T) {
	if got, err := cleanSummary("\"Added a summary\nline to Done todos.\"\n"); err != nil || got != "Added a summary line to Done todos." {
		t.Errorf("cleanSummary = %q, %v", got, err)
	}
	if _, err := cleanSummary(" "); err == nil {
		t.Error("empty output should be an error")
	}
}
