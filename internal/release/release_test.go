package release

import (
	"strings"
	"testing"
)

func TestParseFormatValid(t *testing.T) {
	for _, c := range []struct{ format, tag string }{
		{"", "1.2.3"},
		{"X.Y.Z", "1.2.3"},
		{"vX.Y.Z", "v1.2.3"},
		{"release-X.Y.Z", "release-1.2.3"},
		{"app/X.Y.Z", "app/1.2.3"},
		{"X_Y_Z-final", "1_2_3-final"},
	} {
		f, err := ParseFormat(c.format)
		if err != nil {
			t.Errorf("%q: %v", c.format, err)
			continue
		}
		if got := f.Render(Version{1, 2, 3}); got != c.tag {
			t.Errorf("%q renders %q, want %q", c.format, got, c.tag)
		}
		if v, ok := f.Parse(c.tag); !ok || v != (Version{1, 2, 3}) {
			t.Errorf("%q parses %q as %v %v", c.format, c.tag, v, ok)
		}
	}
}

func TestParseFormatInvalid(t *testing.T) {
	for format, want := range map[string]string{
		"X.Y":        "Z exactly once",
		"X.Y.Z.Z":    "Z exactly once",
		"Z.Y.X":      "in that order",
		"XY.Z":       "something between",
		"v1X.Y.Z":    "digit",
		"X.Y.Z2":     "digit",
		"X0Y.Z":      "digit",
		"v X.Y.Z":    "valid git tag",
		"X.Y.Z.lock": "valid git tag",
		"-X.Y.Z":     "valid git tag",
		"X..Y.Z":     "valid git tag",
		"X.Y.Z~":     "valid git tag",
		"/X.Y.Z":     "valid git tag",
		"rel:X.Y.Z":  "valid git tag",
	} {
		_, err := ParseFormat(format)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want an error about %q", format, err, want)
		}
	}
}

func TestParseIgnoresOtherFormats(t *testing.T) {
	f, _ := ParseFormat("vX.Y.Z")
	for _, tag := range []string{"1.2.3", "v1.2", "v1.2.3-rc.1", "v01.2.3", "xv1.2.3", "v1.2.3.4", "va.b.c"} {
		if v, ok := f.Parse(tag); ok {
			t.Errorf("%q parsed as %v", tag, v)
		}
	}
}

func TestLatestIsHighestVersion(t *testing.T) {
	f, _ := ParseFormat("vX.Y.Z")
	// Listed in creation order: the newest tag isn't the highest version.
	tags := []string{"v0.9.0", "v1.10.0", "v1.2.0", "2.0.0", "v1.9.9", "nightly"}
	l, ok := f.Latest(tags)
	if !ok || l.Name != "v1.10.0" {
		t.Fatalf("latest %v %v", l, ok)
	}
	if p, ok := f.Previous(tags, Version{1, 10, 0}); !ok || p.Name != "v1.9.9" {
		t.Errorf("previous %v %v", p, ok)
	}
	if _, ok := f.Latest([]string{"1.0.0"}); ok {
		t.Error("a tag in another format counted")
	}
	got := f.SortTags(tags)
	if len(got) != 4 || got[0].Name != "v1.10.0" || got[3].Name != "v0.9.0" {
		t.Errorf("sorted %v", got)
	}
}

func TestBump(t *testing.T) {
	v := Version{1, 4, 2}
	for kind, want := range map[string]Version{Patch: {1, 4, 3}, Minor: {1, 5, 0}, Major: {2, 0, 0}} {
		if got, err := v.Bump(kind); err != nil || got != want {
			t.Errorf("%s: %v %v", kind, got, err)
		}
	}
	if _, err := v.Bump("huge"); err == nil {
		t.Error("unknown bump accepted")
	}
}

func TestSuggest(t *testing.T) {
	f, _ := ParseFormat("release-X.Y.Z")
	if s := f.Suggest(nil); s.First != "release-0.1.0" || s.Patch != "" {
		t.Errorf("first: %+v", s)
	}
	s := f.Suggest([]string{"release-1.4.2", "release-1.3.9", "v9.9.9"})
	if s.Latest != "release-1.4.2" || s.Patch != "release-1.4.3" || s.Minor != "release-1.5.0" || s.Major != "release-2.0.0" {
		t.Errorf("suggest: %+v", s)
	}
}

func TestResolve(t *testing.T) {
	f, _ := ParseFormat("vX.Y.Z")
	tags := []string{"v1.2.3"}
	for _, c := range []struct{ version, bump, want string }{
		{"", Patch, "v1.2.4"},
		{"", Minor, "v1.3.0"},
		{"", Major, "v2.0.0"},
		{"2.5.0", "", "v2.5.0"},
		{"v2.5.0", "", "v2.5.0"},
	} {
		got, err := f.Resolve(tags, c.version, c.bump)
		if err != nil || got.Name != c.want {
			t.Errorf("%q/%q: %v %v, want %s", c.version, c.bump, got, err, c.want)
		}
	}
	if got, err := f.Resolve(nil, "", Minor); err != nil || got.Name != "v0.1.0" {
		t.Errorf("first release: %v %v", got, err)
	}
	for _, c := range []struct{ version, bump string }{{"", ""}, {"1.0.0", Patch}, {"release-1.0.0", ""}, {"1.0", ""}, {"", "huge"}} {
		if _, err := f.Resolve(tags, c.version, c.bump); err == nil {
			t.Errorf("%q/%q accepted", c.version, c.bump)
		}
	}
}

func TestCheckRefName(t *testing.T) {
	for _, ok := range []string{"v1.2.3", "app/1.2.3", "release-1.2.3", "a.b"} {
		if err := CheckRefName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "@", "a b", "a..b", "a/", "a.", ".a", "a/.b", "a.lock", "a@{b", "a\\b", "a^b", "a//b"} {
		if CheckRefName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestWorkflowHint(t *testing.T) {
	files := map[string][]byte{
		"ci.yml":      []byte("name: CI\non:\n  push:\n    branches: [main]\n  pull_request:\n"),
		"release.yml": []byte("name: Release\n\non:\n  push:\n    tags: [\"v*\"]\n"),
		"broken.yml":  []byte("on: [\n"),
	}
	triggers := TagTriggers(files)
	if len(triggers) != 1 || triggers[0].File != "release.yml" || triggers[0].Tags[0] != "v*" {
		t.Fatalf("triggers %+v", triggers)
	}
	plain, _ := ParseFormat("X.Y.Z")
	h := WorkflowHint(plain, triggers)
	if h == nil || h.Suggest != "vX.Y.Z" || !strings.Contains(h.Message, "1.2.3") || !strings.Contains(h.Message, "v*") {
		t.Fatalf("hint %+v", h)
	}
	v, _ := ParseFormat("vX.Y.Z")
	if h := WorkflowHint(v, triggers); h != nil {
		t.Errorf("vX.Y.Z matches v*, got %+v", h)
	}
	// A numeric trigger, as in Dash's own release.yml: X.Y.Z (and its
	// pre-releases) start it, vX.Y.Z doesn't.
	numeric := TagTriggers(map[string][]byte{"release.yml": []byte("on:\n  push:\n    tags: [\"[0-9]+.[0-9]+.[0-9]+*\"]\n")})
	if h := WorkflowHint(plain, numeric); h != nil {
		t.Errorf("X.Y.Z matches [0-9]+.[0-9]+.[0-9]+*, got %+v", h)
	}
	if h := WorkflowHint(v, numeric); h == nil || h.Suggest != "X.Y.Z" {
		t.Errorf("vX.Y.Z against a numeric trigger: %+v", h)
	}
	// No workflow filters tags: nothing to warn about.
	if h := WorkflowHint(plain, TagTriggers(map[string][]byte{"ci.yml": files["ci.yml"]})); h != nil {
		t.Errorf("no tag workflows: %+v", h)
	}
}

func TestWorkflowFilters(t *testing.T) {
	for _, c := range []struct {
		yaml, tag string
		want      bool
	}{
		{"on:\n  push:\n    tags: ['v[0-9]+.[0-9]+.[0-9]+']\n", "v1.22.3", true},
		{"on:\n  push:\n    tags: ['v[0-9]+.[0-9]+.[0-9]+']\n", "1.2.3", false},
		{"on:\n  push:\n    tags: ['[0-9]+.[0-9]+.[0-9]+*']\n", "0.1.1", true},
		{"on:\n  push:\n    tags: ['[0-9]+.[0-9]+.[0-9]+*']\n", "10.20.30-rc.1", true},
		{"on:\n  push:\n    tags: ['[0-9]+.[0-9]+.[0-9]+*']\n", "v0.1.1", false},
		{"on:\n  push:\n    tags: ['[0-9]+.[0-9]+.[0-9]+*']\n", "1.2", false},
		{"on:\n  push:\n    tags: ['*']\n", "app/1.2.3", false}, // * stops at /
		{"on:\n  push:\n    tags: ['**']\n", "app/1.2.3", true},
		{"on:\n  push:\n    tags: ['v*', '!v0.*']\n", "v0.1.0", false},
		{"on:\n  push:\n    tags: ['v*', '!v0.*']\n", "v1.1.0", true},
		{"on:\n  push:\n    tags-ignore: ['nightly*']\n", "1.2.3", true},
		{"on:\n  push:\n    tags-ignore: ['*']\n", "1.2.3", false},
		{"on:\n  push:\n    tags: release-*\n", "release-1.0.0", true},
	} {
		ts := TagTriggers(map[string][]byte{"w.yml": []byte(c.yaml)})
		if len(ts) != 1 {
			t.Errorf("%q: %d triggers", c.yaml, len(ts))
			continue
		}
		if got := ts[0].Matches(c.tag); got != c.want {
			t.Errorf("%q on %s: %v, want %v", c.yaml, c.tag, got, c.want)
		}
	}
}
