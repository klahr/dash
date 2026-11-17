package model

import (
	"slices"
	"strings"
	"testing"
)

// Every runner has GitHub's default labels for a self-hosted runner: its
// system and architecture in GitHub's spelling.
func TestDefaultRunnerLabels(t *testing.T) {
	for _, c := range []struct {
		goos, goarch string
		want         []string
	}{
		{"linux", "amd64", []string{"self-hosted", "Linux", "X64"}},
		{"darwin", "arm64", []string{"self-hosted", "macOS", "ARM64"}},
		{"windows", "386", []string{"self-hosted", "Windows", "X86"}},
		{"linux", "arm", []string{"self-hosted", "Linux", "ARM"}},
		{"", "", []string{"self-hosted"}}, // a runner too old to say
	} {
		if got := DefaultRunnerLabels(c.goos, c.goarch); !slices.Equal(got, c.want) {
			t.Errorf("%s/%s: %v, want %v", c.goos, c.goarch, got, c.want)
		}
	}
}

// Labels are tidied (trimmed, one of each whatever its case, the first
// spelling kept) and checked: no spaces or commas, not too long, not too
// many.
func TestCleanLabels(t *testing.T) {
	got, err := CleanLabels([]string{" gpu ", "", "GPU", "big-disk", "os:linux/x64+v2", "a_b.c"})
	if err != nil || !slices.Equal(got, []string{"gpu", "big-disk", "os:linux/x64+v2", "a_b.c"}) {
		t.Errorf("CleanLabels = %v, %v", got, err)
	}
	if got, err := CleanLabels(nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("no labels: %#v, %v", got, err)
	}
	for _, bad := range [][]string{{"two words"}, {"a,b"}, {"é"}, {strings.Repeat("x", 65)}} {
		if _, err := CleanLabels(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	many := make([]string, MaxLabels+1)
	for i := range many {
		many[i] = strings.Repeat("x", i+1)
	}
	if _, err := CleanLabels(many); err == nil {
		t.Error("too many labels were accepted")
	}
}

// A runner works on a project when it has every label the project runs on,
// whatever their case; a project on no labels runs on no runner.
func TestLabelsMatch(t *testing.T) {
	have := []string{"self-hosted", "macOS", "ARM64", "gpu"}
	for _, c := range []struct {
		want []string
		ok   bool
	}{
		{[]string{"self-hosted"}, true},
		{[]string{"macos", "arm64"}, true},
		{[]string{"gpu", "Linux"}, false},
		{nil, false},
	} {
		if got := LabelsMatch(have, c.want); got != c.ok {
			t.Errorf("LabelsMatch(%v) = %v", c.want, got)
		}
	}
	if got := MissingLabels(have, []string{"GPU", "Linux", "X64"}); !slices.Equal(got, []string{"Linux", "X64"}) {
		t.Errorf("MissingLabels = %v", got)
	}
	if !(Project{RunsOn: []string{"self-hosted"}}).OnRunners() || (Project{}).OnRunners() {
		t.Error("OnRunners doesn't follow RunsOn")
	}
}
