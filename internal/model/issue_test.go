package model

import (
	"strings"
	"testing"
)

func TestIssueTransitions(t *testing.T) {
	for _, c := range []struct {
		from, to IssueStatus
		ok       bool
	}{
		{IssueOpen, IssueTaskCreated, true},
		{IssueOpen, IssueDismissed, true},
		{IssueTaskCreated, IssueResolved, true},
		{IssueResolved, IssueOpen, true},
		{IssueDismissed, IssueOpen, true},
		{IssueDismissed, IssueResolved, false},
		{IssueDismissed, IssueTaskCreated, false},
		{IssueResolved, IssueTaskCreated, false},
		{IssueResolved, IssueDismissed, false},
	} {
		if got := CanTransitionIssue(c.from, c.to); got != c.ok {
			t.Errorf("%s → %s: %v, want %v", c.from, c.to, got, c.ok)
		}
	}
}

func TestFingerprintIgnoresLineAndFormatting(t *testing.T) {
	a := Issue{Type: IssueBug, File: "./src/a.go", Line: 3, Title: "Off by one"}
	b := Issue{Type: IssueBug, File: "src/a.go", Line: 9, Title: " off  BY one "}
	if a.ComputeFingerprint() != b.ComputeFingerprint() {
		t.Fatal("same issue, different fingerprints")
	}
	c := b
	c.File = "src/b.go"
	if c.ComputeFingerprint() == b.ComputeFingerprint() {
		t.Fatal("different files, same fingerprint")
	}
}

func TestTaskFromIssues(t *testing.T) {
	title, prompt := TaskFromIssues([]Issue{{
		Title: "SQL injection", Type: IssueVuln, Severity: SevHigh, File: "db.go", Line: 42,
		Details: "query built with Sprintf", Fix: "use placeholders", Source: "scan:gosec",
	}})
	if title != "Fix: SQL injection" {
		t.Errorf("title %q", title)
	}
	for _, want := range []string{"SQL injection", "vuln", "high", "db.go:42", "query built with Sprintf", "use placeholders", "scan:gosec"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	title, prompt = TaskFromIssues([]Issue{{Title: "a"}, {Title: "b"}})
	if !strings.HasPrefix(title, "Fix 2 issues") || !strings.Contains(prompt, "## 2. b") {
		t.Errorf("combined: %q\n%s", title, prompt)
	}
}
