package runnerctl

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var exitRe = regexp.MustCompile(`(^|[;{]\s*)exit\b`)

func TestPowerShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "''",
		"https://x.example": "'https://x.example'",
		"it's":              "'it''s'",
		"$(rm -r ~) `x`":    "'$(rm -r ~) `x`'",
		"a’b":               "'a’’b'",
	} {
		if got := PowerShellQuote(in); got != want {
			t.Errorf("PowerShellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestPowerShellScriptRendering(t *testing.T) {
	b, err := PowerShellScript(ScriptParams{Hub: "https://dash.example.com/", Version: "1.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"$Hub = 'https://dash.example.com'\n",
		"$Version = '1.2.0'\n",
		"$Releases = '" + Releases + "'\n",
		"$LatestApi = '" + LatestAPI + "'\n",
		"irm https://dash.example.com/runner/install.ps1",
		`_windows_$arch.zip`,
		"[string]$NotesKey = \"\",",
		`if ($NotesKey) { $runArgs += @("--notes-key", $NotesKey) }`,
		"\nMain\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("install.ps1 lacks %q", want)
		}
	}
	// Run as a script block, exit would close the user's window: it throws.
	for _, l := range strings.Split(s, "\n") {
		if code := strings.TrimSpace(l); !strings.HasPrefix(code, "#") && exitRe.MatchString(code) {
			t.Errorf("install.ps1 exits instead of throwing: %s", l)
		}
	}
	// The script passes the key's path on and never opens the key itself.
	for _, l := range strings.Split(s, "\n") {
		if code := strings.TrimSpace(l); !strings.HasPrefix(code, "#") && strings.Contains(code, "$NotesKey") &&
			regexp.MustCompile(`(?i)Get-Content|ReadAll|Test-Path|Copy-Item|Get-Item`).MatchString(code) {
			t.Errorf("install.ps1 touches the notes key: %s", l)
		}
	}
	if _, err := PowerShellScript(ScriptParams{Hub: "http://dash.example.com"}); err == nil {
		t.Error("plain http hub accepted")
	}
}

func TestPowerShellHandler(t *testing.T) {
	h := PowerShellHandlerFor(func(r *http.Request) (string, error) { return HubURL(r, "", false, false) }, "1.2.0")
	get := func(host string) string {
		r := httptest.NewRequest("GET", "/runner/install.ps1", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Body.String()
	}
	if s := get("localhost:7788"); !strings.Contains(s, "$Hub = 'http://localhost:7788'") {
		t.Errorf("loopback hub:\n%.300s", s)
	}
	if s := get("dash.example.org"); !strings.HasPrefix(s, "throw 'dash install:") || !strings.Contains(s, "HTTPS") {
		t.Errorf("plain http hub got:\n%s", s)
	}
}
