package agent

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
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

func TestCleanRefined(t *testing.T) {
	for in, want := range map[string]string{
		"  Add a button.\n":                    "Add a button.",
		"```markdown\n- Add a button\n```\n":   "- Add a button",
		"Run:\n```sh\ngo test\n```\nThen fix.": "Run:\n```sh\ngo test\n```\nThen fix.",
	} {
		if got, err := cleanRefined(in); err != nil || got != want {
			t.Errorf("cleanRefined(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := cleanRefined(" \n"); err == nil {
		t.Error("empty output should be an error")
	}
}

func TestParseParts(t *testing.T) {
	out := "Here you go:\n```json\n[" +
		`{"title": "\"Add the users table.\"", "prompt": " Create the table. "},` +
		`{"title": "Skipped", "prompt": "  "},` +
		`{"title": "Add the signup form", "prompt": "Build the form.", "after": [1, 1, 2, 3, 4, 0]},` +
		`{"title": "", "prompt": "Write the docs.", "after": [3]}` +
		"]\n```\nDone."
	got, err := parseParts(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []Part{
		{Title: "Add the users table", Prompt: "Create the table."},
		{Title: "Add the signup form", Prompt: "Build the form.", After: []int{0}},
		{Prompt: "Write the docs.", After: []int{1}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseParts:\n got %+v\nwant %+v", got, want)
	}
	for _, bad := range []string{"", "no JSON here", "[]", `[{"title": "x", "prompt": ""}]`, "[{oops}]"} {
		if _, err := parseParts(bad); err == nil {
			t.Errorf("parseParts(%q): no error", bad)
		}
	}
	var many strings.Builder
	many.WriteString("[")
	for i := range MaxParts + 3 {
		if i > 0 {
			many.WriteString(",")
		}
		fmt.Fprintf(&many, `{"title": "Part %d", "prompt": "Do %d.", "after": [%d]}`, i, i, MaxParts+1)
	}
	many.WriteString("]")
	if got, err := parseParts(many.String()); err != nil || len(got) != MaxParts || got[MaxParts-1].After != nil {
		t.Errorf("parseParts of %d parts: %d parts, %v", MaxParts+3, len(got), err)
	}
}

// Calls the real CLI; opt in with DASH_REAL_CLAUDE=1.
func TestSplitReal(t *testing.T) {
	if os.Getenv("DASH_REAL_CLAUDE") == "" {
		t.Skip("set DASH_REAL_CLAUDE=1 to call the claude CLI")
	}
	parts, err := Titler{}.Split(context.Background(), "Add user accounts: a users table with email and password hash, "+
		"a signup form and a login form, sessions in a cookie, and a page to change the password. Document it in docs/accounts.md.")
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range parts {
		t.Logf("%d. %s (after %v)\n%s", i+1, p.Title, p.After, p.Prompt)
	}
}
