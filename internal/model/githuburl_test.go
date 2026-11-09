package model

import "testing"

func TestIsGitHubWebURL(t *testing.T) {
	for u, want := range map[string]bool{
		"https://github.com/o/r/pull/1":                true,
		"https://GitHub.com/o/r/actions/runs/2":        true,
		"https://gist.github.com/x":                    true,
		"http://github.com/o/r/pull/1":                 false,
		"https://hub.example.com/api/projects/p/graph": false,
		"javascript:alert(1)":                          false,
		"/api/projects/p/graph":                        false,
		"https://github.com.evil.com/x":                false,
		"https://evilgithub.com/x":                     false,
		"https://github.com@evil.com/x":                false,
		"https://x@github.com/x":                       false,
		"https://github.com:8443/x":                    false,
		"":                                             false,
	} {
		if got := IsGitHubWebURL(u); got != want {
			t.Errorf("IsGitHubWebURL(%q) = %v, want %v", u, got, want)
		}
	}
}
