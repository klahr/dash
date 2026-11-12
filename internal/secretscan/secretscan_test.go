package secretscan

import (
	"strings"
	"testing"
)

func TestScanFindsSecrets(t *testing.T) {
	for _, s := range []string{
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----",
		"key AKIAIOSFODNN7EXAMPLE here",
		"GITHUB_TOKEN=ghp_" + strings.Repeat("a1B2", 9),
		"use sk-ant-api03-" + strings.Repeat("x", 40),
		"xoxb-123456789012-abcdefghijkl",
		"stripe sk_live_" + strings.Repeat("Z", 24),
		"runner token dr_" + strings.Repeat("Q", 26),
		"git clone https://bob:hunter2hunter2@example.com/r.git",
		"AIza" + strings.Repeat("b", 35),
	} {
		if _, ok := Has(s); !ok {
			t.Errorf("missed a secret in %q", s)
		}
	}
}

func TestScanIgnoresOrdinaryNotes(t *testing.T) {
	for _, s := range []string{
		"The build needs GO_FLAGS=-tags=sqlite; see docs/build.md.",
		"Use sk- prefixes for keys, and ask the user for one.",
		"https://example.com/path?x=1 and git@github.com:org/repo.git",
		"The API token lives in ~/.dash/runner.json; never paste it.",
		"commit 5a42752e9bc0f1d3 fixed the dash_ prefix check",
	} {
		if k, ok := Has(s); ok {
			t.Errorf("%q flagged as %s", s, k)
		}
	}
}

func TestFindingsAreInOrder(t *testing.T) {
	text := "a AKIAIOSFODNN7EXAMPLE b -----BEGIN PRIVATE KEY-----"
	fs := Scan(text)
	if len(fs) != 2 || fs[0].Kind != "AWS access key" || fs[1].Kind != "private key" || fs[0].Offset != 2 {
		t.Fatalf("findings: %+v", fs)
	}
}
