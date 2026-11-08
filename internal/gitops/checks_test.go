package gitops

import "testing"

func TestNotAboutCode(t *testing.T) {
	for name, want := range map[string]bool{
		"CLA": true, "license/cla": true, "cla-bot": true, "DCO": true, "Contributor License Agreement": true,
		"build": false, "test (ubuntu)": false, "clang-tidy": false, "declare": false, "lint": false,
	} {
		if got := NotAboutCode(PRCheck{Name: name}); got != want {
			t.Errorf("NotAboutCode(%q) = %v, want %v", name, got, want)
		}
	}
}
