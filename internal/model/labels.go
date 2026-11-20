package model

import (
	"fmt"
	"slices"
	"strings"
)

// Runner labels pick which of a hub's runners work on a project, as GitHub
// Actions' self-hosted runner labels pick where a job runs: a project runs
// on the labels in Project.RunsOn, and a runner of its org works on it when
// it has every one of them. Labels compare without case ("macos" is
// "macOS"). Every runner has the default labels (DefaultRunnerLabels): its
// system and architecture, as GitHub names them; its owner or an org admin
// adds labels of their own ("gpu", "big-disk").

// LabelAny is the label every runner matches without having it: a project
// that runs on it alone runs on any runner of its org. No runner lists it as
// one of its labels, and none can be given it (CleanRunnerLabels).
const LabelAny = "any"

// LabelSelfHosted is the label every runner had before LabelAny took its
// place; the hub moves stored projects and runners off it when it starts
// (dispatch.MigrateRunnerLinks).
const LabelSelfHosted = "self-hosted"

// MaxLabels caps a runner's own labels and a project's RunsOn.
const MaxLabels = 32

// maxLabelLen caps one label's length.
const maxLabelLen = 64

// DefaultRunnerLabels are the labels every runner on goos and goarch (Go's
// names, as the runner reports them) has without being given them: its
// system ("Linux", "macOS", "Windows") and its architecture ("X64", "ARM64",
// "ARM", "X86"), as on GitHub. A runner too old to say what it runs on has
// none.
func DefaultRunnerLabels(goos, goarch string) []string {
	out := []string{}
	switch goos {
	case "linux":
		out = append(out, "Linux")
	case "darwin":
		out = append(out, "macOS")
	case "windows":
		out = append(out, "Windows")
	}
	switch goarch {
	case "amd64":
		out = append(out, "X64")
	case "arm64":
		out = append(out, "ARM64")
	case "arm":
		out = append(out, "ARM")
	case "386":
		out = append(out, "X86")
	}
	return out
}

// CleanLabels checks and tidies a list of labels: each trimmed, empty ones
// dropped, and one of each (without case: the first spelling stays). A
// label is up to 64 letters, digits and "-_.:+/" (no spaces or commas), and
// a list holds up to MaxLabels of them.
func CleanLabels(labels []string) ([]string, error) {
	out := []string{}
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" || HasLabel(out, l) {
			continue
		}
		if len(l) > maxLabelLen {
			return nil, fmt.Errorf("label %q is longer than %d characters", l, maxLabelLen)
		}
		for _, r := range l {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.:+/", r)) {
				return nil, fmt.Errorf("label %q has %q in it: use letters, digits and - _ . : + /", l, r)
			}
		}
		out = append(out, l)
	}
	if len(out) > MaxLabels {
		return nil, fmt.Errorf("at most %d labels", MaxLabels)
	}
	return out, nil
}

// CleanRunnerLabels is CleanLabels for a runner's own labels: LabelAny is
// refused, as every runner matches it already.
func CleanRunnerLabels(labels []string) ([]string, error) {
	out, err := CleanLabels(labels)
	if err != nil {
		return nil, err
	}
	if HasLabel(out, LabelAny) {
		return nil, fmt.Errorf("%q isn't a runner's label: a project that runs on it runs on any runner", LabelAny)
	}
	return out, nil
}

// HasLabel reports whether labels has l, without case.
func HasLabel(labels []string, l string) bool {
	return slices.ContainsFunc(labels, func(x string) bool { return strings.EqualFold(x, l) })
}

// LabelsMatch reports whether a runner with the labels have works on a
// project that runs on want: it has every one of them (every runner matches
// LabelAny). Nothing matches an empty want: such a project runs on no
// runner.
func LabelsMatch(have, want []string) bool {
	if len(want) == 0 {
		return false
	}
	for _, l := range want {
		if !strings.EqualFold(l, LabelAny) && !HasLabel(have, l) {
			return false
		}
	}
	return true
}

// MissingLabels are the labels in want that have lacks (never LabelAny,
// which every runner matches).
func MissingLabels(have, want []string) []string {
	var out []string
	for _, l := range want {
		if !strings.EqualFold(l, LabelAny) && !HasLabel(have, l) {
			out = append(out, l)
		}
	}
	return out
}

// ShowLabels is labels for a message: joined with ", ", without LabelAny,
// which is a wildcard and no runner has; "any runner" when that leaves
// none.
func ShowLabels(labels []string) string {
	if shown := MissingLabels(nil, labels); len(shown) > 0 {
		return strings.Join(shown, ", ")
	}
	return "any runner"
}

// SameLabels reports whether a and b are the same labels, in any order and
// case.
func SameLabels(a, b []string) bool {
	for _, l := range a {
		if !HasLabel(b, l) {
			return false
		}
	}
	for _, l := range b {
		if !HasLabel(a, l) {
			return false
		}
	}
	return true
}
