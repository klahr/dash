package claudecli

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// What `claude auth status` printed (Claude Code 2.1.326) with an API key,
// a claude.ai login, a CLAUDE_CODE_OAUTH_TOKEN and nothing.
const (
	statusKey   = `{"loggedIn":true,"authMethod":"api_key","apiProvider":"firstParty","apiKeySource":"ANTHROPIC_API_KEY"}`
	statusMax   = `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"ann@example.com","subscriptionType":"max","storedClaudeAiLogin":true}`
	statusToken = `{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty"}`
	statusNone  = `{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}`
)

func TestParseAuth(t *testing.T) {
	cases := []struct {
		out  string
		want *Auth
	}{
		{statusKey, &Auth{Method: AuthAPIKey, KeySource: KeyEnv}},
		{statusMax, &Auth{Method: AuthSubscription, Subscription: "max"}},
		{statusToken, &Auth{Method: AuthSubscription}},
		{statusNone, &Auth{Method: AuthNone}},
		{`{"loggedIn":true,"authMethod":"something new"}`, &Auth{Method: AuthSubscription}}, // never taken for a key
		{"not json", nil},
	}
	for _, c := range cases {
		got := ParseAuth([]byte(c.out))
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("ParseAuth(%s) = %+v, want %+v", c.out, got, c.want)
		}
	}
}

// Only an API key in ANTHROPIC_API_KEY with no subscription login to fall
// back to makes a runner shareable; the monitor asks again without the key
// to find such a login, which `claude auth status` hides next to a key.
func TestAuthFindsALoginBehindTheKey(t *testing.T) {
	behind := statusMax
	m := &Monitor{
		run: func(_ context.Context, _ string, args ...string) (string, error) {
			if args[0] == "--version" {
				return "2.1.326 (Claude Code)", nil
			}
			if args[0] == "auth" {
				return statusKey, nil
			}
			return "Error: Input must be provided", errExit1
		},
		runNoKey: func(context.Context, string, ...string) (string, error) { return behind, nil },
	}
	st := m.Check(context.Background())
	if st.Auth == nil || st.Auth.Method != AuthAPIKey || st.Auth.Login != "max" || st.Auth.APIKeyOnly() {
		t.Fatalf("a key with a Max login behind it: %+v", st.Auth)
	}
	if why := st.Auth.NotShared(); !strings.Contains(why, "Claude Max subscription login") {
		t.Errorf("why not shared: %q", why)
	}
	behind = statusToken
	if a := m.Status(); a.Auth == nil {
		t.Fatal("no auth kept")
	}
	m.CheckAccount(context.Background())
	if a := m.Status().Auth; a.Login != AuthSubscription || a.APIKeyOnly() {
		t.Errorf("a key with an OAuth token next to it: %+v", a)
	}
	behind = statusNone
	m.CheckAccount(context.Background())
	if a := m.Status().Auth; !a.APIKeyOnly() || a.NotShared() != "" || a.Plan() != "an API key" {
		t.Errorf("a key alone: %+v (%q)", a, a.NotShared())
	}
}

func TestNotShared(t *testing.T) {
	cases := []struct {
		a    *Auth
		want string
	}{
		{nil, "hasn't said"},
		{&Auth{Method: AuthSubscription, Subscription: "team"}, "Claude Team subscription; a subscription is one person's"},
		{&Auth{Method: AuthNone}, "isn't signed in"},
		{&Auth{Method: AuthAPIKey, KeySource: "apiKeyHelper"}, "set it in ANTHROPIC_API_KEY"},
	}
	for _, c := range cases {
		if got := c.a.NotShared(); !strings.Contains(got, c.want) {
			t.Errorf("%+v: %q, want %q", c.a, got, c.want)
		}
	}
}

// SharedEnv keeps the key and drops every login, with a config folder of
// its own.
func TestSharedEnv(t *testing.T) {
	env := SharedEnv([]string{"PATH=/bin", "ANTHROPIC_API_KEY=sk", "CLAUDE_CODE_OAUTH_TOKEN=tok", "ANTHROPIC_AUTH_TOKEN=x",
		"CLAUDE_CONFIG_DIR=/home/owner/.claude"}, "/data/shared-claude")
	want := []string{"PATH=/bin", "ANTHROPIC_API_KEY=sk", "CLAUDE_CONFIG_DIR=/data/shared-claude"}
	if !slices.Equal(env, want) {
		t.Errorf("SharedEnv = %q, want %q", env, want)
	}
}
