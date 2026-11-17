package model

import (
	"slices"
	"strings"
	"testing"
)

// Every runner has its system and architecture as labels, in GitHub's
// spelling, and nothing else by default.
func TestDefaultRunnerLabels(t *testing.T) {
	for _, c := range []struct {
		goos, goarch string
		want         []string
	}{
		{"linux", "amd64", []string{"Linux", "X64"}},
		{"darwin", "arm64", []string{"macOS", "ARM64"}},
		{"windows", "386", []string{"Windows", "X86"}},
		{"linux", "arm", []string{"Linux", "ARM"}},
		{"", "", []string{}}, // a runner too old to say
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
	// A project may run on "any"; a runner can't be given it.
	if got, err := CleanLabels([]string{"any"}); err != nil || !slices.Equal(got, []string{"any"}) {
		t.Errorf("CleanLabels(any) = %v, %v", got, err)
	}
	if got, err := CleanRunnerLabels([]string{" gpu ", "GPU"}); err != nil || !slices.Equal(got, []string{"gpu"}) {
		t.Errorf("CleanRunnerLabels = %v, %v", got, err)
	}
	for _, bad := range [][]string{{"any"}, {"gpu", "ANY"}, {"two words"}} {
		if _, err := CleanRunnerLabels(bad); err == nil {
			t.Errorf("runner labels %q were accepted", bad)
		}
	}
}

// A runner works on a project when it has every label the project runs on,
// whatever their case; every runner matches "any", even one without labels;
// a project on no labels runs on no runner.
func TestLabelsMatch(t *testing.T) {
	have := []string{"macOS", "ARM64", "gpu"}
	for _, c := range []struct {
		want []string
		ok   bool
	}{
		{[]string{"any"}, true},
		{[]string{"Any", "gpu"}, true},
		{[]string{"any", "Linux"}, false},
		{[]string{"self-hosted"}, false},
		{[]string{"macos", "arm64"}, true},
		{[]string{"gpu", "Linux"}, false},
		{nil, false},
	} {
		if got := LabelsMatch(have, c.want); got != c.ok {
			t.Errorf("LabelsMatch(%v) = %v", c.want, got)
		}
	}
	if !LabelsMatch(nil, []string{"any"}) || !LabelsMatch([]string{}, []string{"ANY"}) {
		t.Error("a runner without labels doesn't match any")
	}
	if got := MissingLabels(have, []string{"GPU", "any", "Linux", "X64"}); !slices.Equal(got, []string{"Linux", "X64"}) {
		t.Errorf("MissingLabels = %v", got)
	}
	if !(Project{RunsOn: []string{"any"}}).OnRunners() || (Project{}).OnRunners() {
		t.Error("OnRunners doesn't follow RunsOn")
	}
}
