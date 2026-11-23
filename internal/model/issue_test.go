package model

import (
	"slices"
	"strings"
	"testing"
)

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
	}}, nil)
	if title != "Fix: SQL injection" {
		t.Errorf("title %q", title)
	}
	for _, want := range []string{"SQL injection", "vuln", "high", "db.go:42", "query built with Sprintf", "use placeholders", "scan:gosec"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	// Reporters are named by the reporter func, and repeat reports counted.
	_, prompt = TaskFromIssues([]Issue{{Title: "x", Source: "agent:t1", Reports: 15, LastSource: "agent:t2"}}, func(src string) string {
		if src == "agent:t1" {
			return `task t1, "First"`
		}
		return ""
	})
	for _, want := range []string{`Reported by: task t1, "First"`, "Reported 15 times, last by: agent:t2"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	title, prompt = TaskFromIssues([]Issue{{Title: "a"}, {Title: "b"}}, nil)
	if !strings.HasPrefix(title, "Fix 2 issues") || !strings.Contains(prompt, "## 2. b") {
		t.Errorf("combined: %q\n%s", title, prompt)
	}
}

func TestIssueAddReport(t *testing.T) {
	i := Issue{Source: "scan:gosec"}
	if i.AddReport("scan:gosec") || i.ReportCount() != 1 {
		t.Fatalf("the same reporter again counted: %+v", i)
	}
	if !i.AddReport("agent:a") || !i.AddReport("agent:b") || i.AddReport("agent:b") {
		t.Fatalf("other reporters not counted once each: %+v", i)
	}
	if i.ReportCount() != 3 || i.LatestSource() != "agent:b" || i.Source != "scan:gosec" {
		t.Fatalf("after reports: %+v", i)
	}
	if !slices.Equal(i.Sources, []string{"scan:gosec", "agent:a", "agent:b"}) {
		t.Fatalf("sources %q", i.Sources)
	}
}

func TestHasGeneratedPrompt(t *testing.T) {
	gen := &Todo{Prompt: "fix it", Finding: &Finding{PromptSum: PromptSum("fix it")}}
	for _, c := range []struct {
		name string
		t    *Todo
		want bool
	}{
		{"generated", gen, true},
		{"edited", &Todo{Prompt: "fix it now", Finding: gen.Finding}, false},
		{"no sum", &Todo{Prompt: "fix it", Finding: &Finding{}}, false},
		{"no finding", &Todo{Prompt: "fix it"}, false},
	} {
		if got := c.t.HasGeneratedPrompt(); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFinding(t *testing.T) {
	f := &Finding{Issues: []Issue{
		{Type: IssueBug, Severity: SevMedium, Source: "scan:lint", Fingerprint: "a", Reports: 2},
		{Type: IssueVuln, Severity: SevHigh, Source: "agent:t1", Fingerprint: "b"},
	}}
	if f.Severity() != SevHigh || f.Reports() != 3 || f.Index("b") != 1 || f.Index("c") != -1 {
		t.Fatalf("severity %s, reports %d", f.Severity(), f.Reports())
	}
	if !slices.Equal(f.Fingerprints(), []string{"a", "b"}) {
		t.Fatalf("fingerprints %q", f.Fingerprints())
	}
	// The worst severity replaces another severity tag; other tags stay.
	got := WithFindingTags([]string{"backend", "medium"}, f)
	if want := []string{"backend", "bug", "security", "high", "scan:lint", "reported"}; !slices.Equal(got, want) {
		t.Fatalf("tags %q, want %q", got, want)
	}
	cp := f.Clone()
	cp.Issues[0].Sources = append(cp.Issues[0].Sources, "x")
	cp.Issues[1].Title = "changed"
	if f.Issues[1].Title != "" || len(f.Issues[0].Sources) != 0 {
		t.Fatal("Clone shares issues with the original")
	}
	var none *Finding
	if none.Severity() != "" || none.Reports() != 0 || none.Index("a") != -1 || none.Clone() != nil {
		t.Fatal("a nil Finding isn't empty")
	}
}
