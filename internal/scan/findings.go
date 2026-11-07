package scan

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/klahr/dash/internal/model"
)

// Finding is one problem as the scan agent reports it. Location can be given
// as file and line, as {"file", "line"} or as "file:line".
type Finding struct {
	Title           string          `json:"title"`
	Type            string          `json:"type"`
	Severity        string          `json:"severity"`
	File            string          `json:"file"`
	Line            json.RawMessage `json:"line"`
	Location        json.RawMessage `json:"location"`
	Details         string          `json:"details"`
	Fix             string          `json:"fix"`
	SuggestedFix    string          `json:"suggested_fix"`
	Fingerprint     string          `json:"fingerprint"`
	FingerprintHint string          `json:"fingerprint_hint"`
	Rule            string          `json:"rule"`
}

// ErrNoFindings is returned when the agent's answer holds no findings JSON.
var ErrNoFindings = errors.New(`the scan's answer has no findings JSON ({"findings": [...]})`)

var fenceRE = regexp.MustCompile("(?s)```(?:json|JSON)?[ \t]*\n(.*?)\n[ \t]*```")

// ParseFindings finds the findings JSON in the agent's final message: the
// last fenced JSON block, or the whole message, holding {"findings": [...]}
// or a bare array.
func ParseFindings(answer string) ([]Finding, error) {
	var candidates []string
	blocks := fenceRE.FindAllStringSubmatch(answer, -1)
	for i := len(blocks) - 1; i >= 0; i-- {
		candidates = append(candidates, blocks[i][1])
	}
	candidates = append(candidates, answer)
	if i, j := strings.Index(answer, "{"), strings.LastIndex(answer, "}"); i >= 0 && j > i {
		candidates = append(candidates, answer[i:j+1])
	}
	var lastErr error
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		var obj struct {
			Findings *[]Finding `json:"findings"`
		}
		if err := json.Unmarshal([]byte(c), &obj); err == nil && obj.Findings != nil {
			return *obj.Findings, nil
		} else if err != nil {
			lastErr = err
		}
		var arr []Finding
		if strings.HasPrefix(c, "[") && json.Unmarshal([]byte(c), &arr) == nil {
			return arr, nil
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoFindings, lastErr)
	}
	return nil, ErrNoFindings
}

const (
	maxTitle = 200
	maxText  = 20000
	maxRule  = 200
)

// Validate checks a finding and makes it an issue. defType is the type when
// the finding names none; root is the scanned checkout, so absolute paths
// inside it can be made relative.
func (f Finding) Validate(defType model.IssueType, root string) (model.Issue, error) {
	is := model.Issue{
		Title:   clip(oneLine(f.Title), maxTitle),
		Details: clip(strings.TrimSpace(f.Details), maxText),
		Fix:     clip(strings.TrimSpace(firstOf(f.Fix, f.SuggestedFix)), maxText),
		Rule:    clip(strings.TrimSpace(firstOf(f.Fingerprint, f.FingerprintHint, f.Rule)), maxRule),
	}
	if is.Title == "" {
		return is, fmt.Errorf("finding without a title")
	}
	var err error
	if is.Type, err = findingType(f.Type, defType); err != nil {
		return is, fmt.Errorf("%q: %w", is.Title, err)
	}
	if is.Severity, err = findingSeverity(f.Severity); err != nil {
		return is, fmt.Errorf("%q: %w", is.Title, err)
	}
	file, line := f.File, 0
	if l, ok := jsonInt(f.Line); ok {
		line = l
	}
	if len(f.Location) > 0 && string(f.Location) != "null" {
		var loc struct {
			File string          `json:"file"`
			Path string          `json:"path"`
			Line json.RawMessage `json:"line"`
		}
		var s string
		switch {
		case json.Unmarshal(f.Location, &loc) == nil:
			file = firstOf(file, loc.File, loc.Path)
			if l, ok := jsonInt(loc.Line); ok && line == 0 {
				line = l
			}
		case json.Unmarshal(f.Location, &s) == nil:
			p, l := splitLocation(s)
			file = firstOf(file, p)
			if line == 0 {
				line = l
			}
		}
	}
	if is.File, err = repoPath(file, root); err != nil {
		return is, fmt.Errorf("%q: %w", is.Title, err)
	}
	if line < 0 {
		line = 0
	}
	if is.File != "" {
		is.Line = line
	}
	return is, nil
}

func findingType(s string, def model.IssueType) (model.IssueType, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return def, nil
	case "bug", "defect", "error", "correctness":
		return model.IssueBug, nil
	case "vuln", "vulnerability", "security", "cve":
		return model.IssueVuln, nil
	case "issue", "other", "smell", "code_smell", "maintainability", "performance", "dependency", "warning":
		return model.IssueOther, nil
	}
	return "", fmt.Errorf("unknown type %q (bug, vuln or issue)", s)
}

func findingSeverity(s string) (model.Severity, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical", "crit", "blocker":
		return model.SevCritical, nil
	case "high", "major", "error":
		return model.SevHigh, nil
	case "", "medium", "moderate", "med":
		return model.SevMedium, nil
	case "low", "minor", "warning":
		return model.SevLow, nil
	case "info", "informational", "note", "none":
		return model.SevInfo, nil
	}
	return "", fmt.Errorf("unknown severity %q (critical, high, medium, low or info)", s)
}

// repoPath makes a reported path relative to the repository root and
// refuses one outside it.
func repoPath(p, root string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	if filepath.IsAbs(p) {
		if root == "" {
			return "", fmt.Errorf("absolute path %q", p)
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q is outside the repository", p)
		}
		p = rel
	}
	p = model.NormalizePath(p)
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", fmt.Errorf("path %q is outside the repository", p)
	}
	return p, nil
}

var locRE = regexp.MustCompile(`^(.*?):(\d+)(?:[:-]\d+)?$`)

func splitLocation(s string) (string, int) {
	s = strings.TrimSpace(s)
	if m := locRE.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[2])
		return m[1], n
	}
	return s, 0
}

func jsonInt(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return int(n), true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		s, _, _ = strings.Cut(strings.TrimSpace(s), "-")
		if v, err := strconv.Atoi(s); err == nil {
			return v, true
		}
	}
	return 0, false
}

func firstOf(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// Issues validates findings into issues for the template's source. Invalid
// findings are left out and described in errs; two findings that are the
// same issue (same fingerprint) count once.
func Issues(t Template, root string, fs []Finding) (issues []model.Issue, errs []error) {
	seen := map[string]bool{}
	for _, f := range fs {
		is, err := f.Validate(t.FindingType(), root)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		is.Source = t.Source()
		if err := is.Normalize(); err != nil {
			errs = append(errs, err)
			continue
		}
		fp := is.ComputeFingerprint()
		if seen[fp] {
			continue
		}
		seen[fp] = true
		issues = append(issues, is)
	}
	return issues, errs
}
