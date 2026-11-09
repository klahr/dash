package main

import "testing"

func TestWebhookPrivateAllowed(t *testing.T) {
	for _, c := range []struct {
		v      string
		public bool
		want   bool
	}{
		{"", false, true},  // invite-only self-hosted
		{"", true, false},  // billing or open signup
		{"on", true, true}, // explicit opt-in
		{"off", false, false},
		{"TRUE", true, true},
		{"0", false, false},
	} {
		got, err := webhookPrivateAllowed(c.v, c.public)
		if err != nil || got != c.want {
			t.Errorf("webhookPrivateAllowed(%q, %v) = %v, %v; want %v", c.v, c.public, got, err, c.want)
		}
	}
	if _, err := webhookPrivateAllowed("maybe", false); err == nil {
		t.Error("webhookPrivateAllowed(maybe) accepted")
	}
}
