package release

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkflowTrigger is a GitHub Actions workflow that runs when certain tags
// are pushed (its on.push.tags / tags-ignore filters).
type WorkflowTrigger struct {
	File       string   `json:"file"`
	Name       string   `json:"name,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	TagsIgnore []string `json:"tags_ignore,omitempty"`
	// all: a plain "on: push" runs on every pushed tag.
	all bool
}

// Matches reports whether pushing tag starts the workflow.
func (w WorkflowTrigger) Matches(tag string) bool {
	if w.all {
		return true
	}
	if len(w.TagsIgnore) > 0 && filterMatches(w.TagsIgnore, tag) {
		return false
	}
	if len(w.Tags) > 0 {
		return filterMatches(w.Tags, tag)
	}
	return len(w.TagsIgnore) > 0
}

// TagTriggers reads workflow files (name → YAML) and returns those whose push
// trigger filters tags. Workflows that ignore tag pushes, and files that
// don't parse, are left out.
func TagTriggers(files map[string][]byte) []WorkflowTrigger {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []WorkflowTrigger
	for _, n := range names {
		if w, ok := tagTrigger(n, files[n]); ok {
			out = append(out, w)
		}
	}
	return out
}

func tagTrigger(file string, content []byte) (WorkflowTrigger, bool) {
	var doc map[string]any
	if yaml.Unmarshal(content, &doc) != nil {
		return WorkflowTrigger{}, false
	}
	w := WorkflowTrigger{File: file}
	w.Name, _ = doc["name"].(string)
	on, ok := doc["on"]
	if !ok {
		on = doc["true"] // YAML 1.1 readers turn a bare `on` key into true
	}
	switch on := on.(type) {
	case string:
		// "on: push" runs on every push, tags included; not a tag filter.
		return w, false
	case []any:
		return w, false
	case map[string]any:
		push, has := on["push"]
		if !has {
			return w, false
		}
		m, _ := push.(map[string]any)
		w.Tags, w.TagsIgnore = strList(m["tags"]), strList(m["tags-ignore"])
		if len(w.Tags) == 0 && len(w.TagsIgnore) == 0 {
			return w, false // every tag (no filters) or none (branch filters only)
		}
		return w, true
	}
	return w, false
}

func strList(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			} else if x != nil {
				out = append(out, fmt.Sprint(x))
			}
		}
		return out
	}
	return nil
}

// filterMatches applies a list of GitHub filter patterns in order: the last
// pattern that matches decides, and "!" patterns exclude.
func filterMatches(patterns []string, ref string) bool {
	matched := false
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		if neg {
			p = p[1:]
		}
		if globRe(p).MatchString(ref) {
			matched = !neg
		}
	}
	return matched
}

// globRe translates a GitHub Actions filter pattern: * matches anything but
// /, ** anything, ? and + make the previous character optional or repeated,
// [] is a character class and \ escapes.
func globRe(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '*' && i+1 < len(p) && p[i+1] == '*':
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?' || c == '+':
			b.WriteByte(c)
		case c == '[':
			end := strings.IndexByte(p[i:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			b.WriteString(p[i : i+end+1])
			i += end
		case c == '\\' && i+1 < len(p):
			b.WriteString(regexp.QuoteMeta(p[i+1 : i+2]))
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return regexp.MustCompile(`^\b$`) // matches nothing
	}
	return re
}

// Hint explains why pushing tags in a format won't start a release workflow.
type Hint struct {
	Message string `json:"message"`
	// Suggest is a format whose tags would start the workflows, if one is
	// easy to tell (e.g. vX.Y.Z for "v*").
	Suggest   string            `json:"suggest,omitempty"`
	Workflows []WorkflowTrigger `json:"workflows"`
}

// WorkflowHint returns a hint when the repository has workflows triggered by
// tag pushes and none of them would run for tags in format f; nil otherwise.
func WorkflowHint(f Format, triggers []WorkflowTrigger) *Hint {
	if len(triggers) == 0 || f.re == nil {
		return nil
	}
	if starts(f, triggers) {
		return nil
	}
	sample := f.Render(Version{1, 2, 3})
	var pats []string
	for _, w := range triggers {
		label := w.File
		if len(w.Tags) > 0 {
			label += " (tags: " + strings.Join(w.Tags, ", ") + ")"
		} else {
			label += " (tags-ignore: " + strings.Join(w.TagsIgnore, ", ") + ")"
		}
		pats = append(pats, label)
	}
	h := &Hint{
		Message: fmt.Sprintf("Pushing a tag like %s won't start %s: %s.", sample,
			plural(len(triggers), "the release workflow", "any of the release workflows"), strings.Join(pats, "; ")),
		Workflows: triggers,
	}
	for _, w := range triggers {
		for _, p := range w.Tags {
			if strings.HasPrefix(p, "!") {
				continue
			}
			if cand, err := ParseFormat(literalPrefix(p) + "X.Y.Z"); err == nil && starts(cand, triggers) {
				h.Suggest = cand.String()
				return h
			}
		}
	}
	return h
}

// starts reports whether tags in format f start at least one of the workflows.
func starts(f Format, triggers []WorkflowTrigger) bool {
	for _, w := range triggers {
		ok := true
		for _, v := range []Version{{1, 2, 3}, {0, 1, 0}, {10, 20, 30}} {
			if !w.Matches(f.Render(v)) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// literalPrefix is the start of a filter pattern before any special character.
func literalPrefix(p string) string {
	if i := strings.IndexAny(p, `*?+[\!`); i >= 0 {
		return p[:i]
	}
	return p
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
