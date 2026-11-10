package model

import "testing"

func TestCleanModel(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "  ": "", " opus ": "opus", "sonnet": "sonnet",
		"claude-sonnet-5-5": "claude-sonnet-5-5", "claude-opus-5-5[1m]": "claude-opus-5-5[1m]",
		"us.anthropic.claude-haiku-5-5:0": "us.anthropic.claude-haiku-5-5:0",
	} {
		if got, err := CleanModel(in); err != nil || got != want {
			t.Errorf("CleanModel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"--dangerously-skip-permissions", "-x", "opus sonnet", "op;us", "1opus", "op\nus"} {
		if _, err := CleanModel(bad); err == nil {
			t.Errorf("CleanModel(%q) accepted", bad)
		}
	}
}
