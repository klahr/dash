package auth

import (
	"strings"
	"testing"

	"github.com/klahr/dash/internal/branding"
)

func TestEmailsCarryTheBrand(t *testing.T) {
	t.Parallel()

	a, h, box := newSignupServer(t)
	a.Brand = branding.Config{Name: "Acme Agents", Contact: "help@acme.example"}
	anon := &client{t: t, h: h}
	signUp(anon, "ann@example.com", "a-long-password")
	m := box.next(t)
	if m.Subject != "Confirm your email address for Acme Agents" {
		t.Errorf("subject: %q", m.Subject)
	}
	for _, want := range []string{"signed up for Acme Agents at", "Questions? Write to help@acme.example"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, m.Body)
		}
	}
	if strings.Contains(m.Body, "Dash") {
		t.Errorf("body still says Dash:\n%s", m.Body)
	}
}

func TestEmailsDefaultToDash(t *testing.T) {
	t.Parallel()

	_, h, box := newSignupServer(t)
	signUp(&client{t: t, h: h}, "bob@example.com", "a-long-password")
	m := box.next(t)
	if m.Subject != "Confirm your email address for Dash" || strings.Contains(m.Body, "Questions?") {
		t.Errorf("unbranded email: %q\n%s", m.Subject, m.Body)
	}
}
