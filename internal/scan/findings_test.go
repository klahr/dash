package scan

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func TestParseFindings(t *testing.T) {
	t.Parallel()

	one := `{"title":"x"}`
	for name, answer := range map[string]string{
		"fenced":      "I looked around.\n\n```json\n{\"findings\": [" + one + "]}\n```\n",
		"last fence":  "```json\n{\"findings\": []}\n```\nOn second thought:\n```json\n{\"findings\": [" + one + "]}\n```",
		"bare":        `{"findings": [` + one + `]}`,
		"array":       "```\n[" + one + "]\n```",
		"in prose":    `Here you go: {"findings": [` + one + `]} — done.`,
		"fence first": "```json\n{\"findings\": [" + one + "]}\n```\nNote: use {braces} carefully.",
	} {
		fs, err := ParseFindings(answer)
		if err != nil || len(fs) != 1 || fs[0].Title != "x" {
			t.Errorf("%s: %+v %v", name, fs, err)
		}
	}
	fs, err := ParseFindings("```json\n{\"findings\": []}\n```")
	if err != nil || len(fs) != 0 {
		t.Errorf("empty: %+v %v", fs, err)
	}
	for _, bad := range []string{"", "No issues found.", `{"results": []}`, "```json\n{\"findings\": [\n```"} {
		if _, err := ParseFindings(bad); !errors.Is(err, ErrNoFindings) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// The answer of scan run 478b2012b849: a ```markdown block before the
// findings block, whose closing fence used to be taken for an opening one,
// and an example {"findings": []} in the prose.
func TestParseFindingsRunAnswer(t *testing.T) {
	t.Parallel()

	b, err := os.ReadFile("testdata/answer-478b2012b849.md")
	if err != nil {
		t.Fatal(err)
	}
	fs, err := ParseFindings(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].Fingerprint != "repo-scan-template-unrestricted-tools-autoschedule" || !strings.Contains(fs[0].Details, "```\n---\ntools: Bash") {
		t.Fatalf("%+v", fs)
	}
}

func TestParseFindingsFences(t *testing.T) {
	t.Parallel()

	one := `{"title":"x"}`
	obj := "{\"findings\": [" + one + "]}"
	for name, answer := range map[string]string{
		"other fences first":          "See:\n```go\nfunc f() {}\n```\nand\n```\n$ make\n```\n```json\n" + obj + "\n```\n",
		"markdown block with example": "```markdown\nanswer {\"findings\": []}.\n```\nDone.\n\n```json\n" + obj + "\n```",
		"longer fence nests":          "````markdown\n```json\n{\"findings\": []}\n```\n````\n```json\n" + obj + "\n```",
		"tilde fence":                 "~~~json\n" + obj + "\n~~~",
		"fence in string":             "```json\n{\"findings\": [{\"title\": \"x\", \"details\": \"Add:\\n```\\nrm -rf /\\n```\\nand ``` here\"}]}\n```",
		"pretty with fence in string": "```json\n{\"findings\": [\n  {\n    \"title\": \"x\",\n    \"fix\": \"```go\\nx()\\n```\"\n  }\n]}\n```\n",
		"inline fences in prose":      "Use ```x``` or ``` y ``` inline.\n```json\n" + obj + "\n```",
		"unclosed earlier fence":      "```go\nfunc f() {\n\nResult:\n```json\n" + obj + "\n```",
		"after the block":             "```json\n" + obj + "\n```\nAlso see `{\"findings\": []}` above.",
		"no fence, example before":    `Format: {"findings": []}. Mine: {"findings": [` + one + `]}`,
		"indented fence":              "  ```json\n  " + obj + "\n  ```",
		"crlf":                        "Hi\r\n```json\r\n" + obj + "\r\n```\r\n",
	} {
		fs, err := ParseFindings(answer)
		if err != nil || len(fs) != 1 || fs[0].Title != "x" {
			t.Errorf("%s: %+v %v", name, fs, err)
		}
	}
}

func TestValidateFinding(t *testing.T) {
	t.Parallel()

	parse := func(js string) Finding {
		fs, err := ParseFindings(`{"findings": [` + js + `]}`)
		if err != nil || len(fs) != 1 {
			t.Fatalf("%s: %v", js, err)
		}
		return fs[0]
	}
	is, err := parse(`{"title":"  SQL   injection\nin find ","type":"Vulnerability","severity":"HIGH","file":"/repo/db/find.go","line":"12",
		"details":"bad","suggested_fix":"bind","fingerprint_hint":"sqli:find"}`).Validate(model.IssueBug, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	want := model.Issue{Title: "SQL injection in find", Type: model.IssueVuln, Severity: model.SevHigh, File: "db/find.go", Line: 12, Details: "bad", Fix: "bind", Rule: "sqli:find"}
	if is.Title != want.Title || is.Type != want.Type || is.Severity != want.Severity || is.File != want.File ||
		is.Line != want.Line || is.Details != want.Details || is.Fix != want.Fix || is.Rule != want.Rule {
		t.Fatalf("got %+v", is)
	}

	// Defaults: the kind's type, medium severity, no location.
	is, err = parse(`{"title":"t"}`).Validate(model.IssueBug, "")
	if err != nil || is.Type != model.IssueBug || is.Severity != model.SevMedium || is.File != "" || is.Line != 0 {
		t.Fatalf("defaults: %+v %v", is, err)
	}
	// Location as an object or "file:line".
	is, _ = parse(`{"title":"t","location":{"file":"./a/b.go","line":7}}`).Validate(model.IssueBug, "")
	if is.File != "a/b.go" || is.Line != 7 {
		t.Fatalf("location object: %+v", is)
	}
	is, _ = parse(`{"title":"t","location":"a/b.go:9-12"}`).Validate(model.IssueBug, "")
	if is.File != "a/b.go" || is.Line != 9 {
		t.Fatalf("location string: %+v", is)
	}
	// A line without a file means nothing.
	is, _ = parse(`{"title":"t","line":5}`).Validate(model.IssueBug, "")
	if is.Line != 0 {
		t.Fatalf("line without file: %+v", is)
	}
	// A long title is cut.
	is, _ = parse(`{"title":"`+strings.Repeat("é", 300)+`"}`).Validate(model.IssueBug, "")
	if len(is.Title) > maxTitle+len("…") {
		t.Fatalf("title %d bytes", len(is.Title))
	}

	for name, js := range map[string]string{
		"no title":     `{"title":"  ","severity":"high"}`,
		"bad type":     `{"title":"t","type":"feature"}`,
		"bad severity": `{"title":"t","severity":"urgent"}`,
		"outside":      `{"title":"t","file":"../etc/passwd"}`,
		"abs outside":  `{"title":"t","file":"/etc/passwd"}`,
	} {
		if _, err := parse(js).Validate(model.IssueBug, "/repo"); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestIssuesDropsInvalidAndDuplicates(t *testing.T) {
	t.Parallel()

	tm := Template{Name: "sec", Kind: KindSecurity}
	fs, err := ParseFindings(`{"findings": [
		{"title":"Hardcoded key","file":"cfg.go","line":3,"fingerprint":"hardcoded-key"},
		{"title":"Hard-coded API key","file":"cfg.go","line":9,"fingerprint":"hardcoded-key"},
		{"title":"","severity":"high"},
		{"title":"Weak hash","severity":"catastrophic"},
		{"title":"Open redirect","file":"web/r.go","type":"bug"}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	issues, errs := Issues(tm, "", fs)
	if len(issues) != 2 || len(errs) != 2 {
		t.Fatalf("%d issues %v, %d errors %v", len(issues), issues, len(errs), errs)
	}
	if issues[0].Type != model.IssueVuln || issues[0].Source != "scan:sec" || issues[1].Type != model.IssueBug {
		t.Fatalf("issues: %+v", issues)
	}
}
