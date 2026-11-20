package claudecli

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
)

// How Claude Code signs in to Claude (Auth.Method).
const (
	// AuthAPIKey: an Anthropic API key, billed to the account that owns it.
	AuthAPIKey = "api_key"
	// AuthSubscription: a Claude subscription login (Pro, Max, Team or
	// Enterprise), from `claude login` or a CLAUDE_CODE_OAUTH_TOKEN. It is
	// one person's.
	AuthSubscription = "subscription"
	// AuthNone: not signed in.
	AuthNone = "none"
)

// KeyEnv is the variable a shared runner's API key is in: the only key
// source Dash takes for other members' work, as it can pass it on to Claude
// Code with nothing else of the runner's owner.
const KeyEnv = "ANTHROPIC_API_KEY"

// LoginEnv are the variables that hold a subscription login (or name the
// config folder Claude Code reads its login from): other members' work runs
// without them (SharedEnv).
var LoginEnv = []string{
	"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_REFRESH_TOKEN", "CLAUDE_CODE_OAUTH_SCOPES",
	"ANTHROPIC_AUTH_TOKEN", "CLAUDE_CONFIG_DIR",
}

// Auth is how the Claude Code CLI on a runner signs in, as `claude auth
// status` reports it. Checked with Claude Code 2.1.326: its JSON has
// "authMethod" ("api_key", "claude.ai", "oauth_token" or "none"),
// "apiKeySource" for a key (e.g. "ANTHROPIC_API_KEY") and
// "subscriptionType" for a login. With a key set it says nothing of a
// login stored next to it, so the runner asks a second time without the
// key (Login).
type Auth struct {
	Method string `json:"method"` // AuthAPIKey, AuthSubscription or AuthNone
	// KeySource is where an API key comes from (Method AuthAPIKey), e.g.
	// "ANTHROPIC_API_KEY" or "apiKeyHelper".
	KeySource string `json:"key_source,omitempty"`
	// Subscription is the plan of a subscription login (Method
	// AuthSubscription): "pro", "max", "team", "enterprise", or "" when
	// Claude Code doesn't say.
	Subscription string `json:"subscription,omitempty"`
	// Login is, for an API key, the subscription login Claude Code (or a
	// project command calling claude) falls back to without the key: its
	// plan, or "subscription" when it doesn't say which ("": none).
	Login string `json:"login,omitempty"`
}

// ParseAuth reads `claude auth status` output. It returns nil when it
// doesn't understand it.
func ParseAuth(out []byte) *Auth {
	var st struct {
		LoggedIn     *bool  `json:"loggedIn"`
		Method       string `json:"authMethod"`
		KeySource    string `json:"apiKeySource"`
		Subscription string `json:"subscriptionType"`
	}
	if json.Unmarshal(out, &st) != nil || st.LoggedIn == nil {
		return nil
	}
	if !*st.LoggedIn {
		return &Auth{Method: AuthNone}
	}
	switch st.Method {
	case "api_key":
		return &Auth{Method: AuthAPIKey, KeySource: st.KeySource}
	case "none":
		return &Auth{Method: AuthNone}
	case "":
		if st.KeySource != "" && st.KeySource != "none" {
			return &Auth{Method: AuthAPIKey, KeySource: st.KeySource}
		}
	}
	// claude.ai, oauth_token, or anything else that is signed in without
	// an API key: count it as a subscription, never as shareable.
	return &Auth{Method: AuthSubscription, Subscription: strings.ToLower(st.Subscription)}
}

// APIKeyOnly reports whether a is an API key in KeyEnv with no subscription
// login to fall back to: the only runner that may do other members' work.
func (a *Auth) APIKeyOnly() bool {
	return a != nil && a.Method == AuthAPIKey && a.KeySource == KeyEnv && a.Login == ""
}

// NotShared says why a runner signed in as a can't do other members' work
// ("" when it can: APIKeyOnly).
func (a *Auth) NotShared() string {
	switch {
	case a == nil:
		return "`claude auth status` gave no answer Dash understands"
	case a.APIKeyOnly():
		return ""
	case a.Method == AuthSubscription:
		return "it uses " + a.Plan() + "; a subscription is one person's and is never shared"
	case a.Method == AuthNone:
		return "Claude isn't signed in on it"
	case a.Login != "":
		return "it also has a " + planName(a.Login) + " login Claude could fall back to; log out of it (claude auth logout)"
	case a.KeySource != KeyEnv:
		return "its API key comes from " + a.KeySource + "; set it in " + KeyEnv + " instead"
	}
	return "it doesn't use an API key"
}

// Plan names a's sign-in for people: "an API key", "a Claude Max
// subscription", "no login".
func (a *Auth) Plan() string {
	switch {
	case a == nil:
		return "unknown"
	case a.Method == AuthAPIKey:
		return "an API key"
	case a.Method == AuthNone:
		return "no login"
	}
	return "a " + planName(a.Subscription)
}

func planName(sub string) string {
	switch strings.ToLower(sub) {
	case "pro", "max", "team", "enterprise":
		return "Claude " + strings.ToUpper(sub[:1]) + strings.ToLower(sub[1:]) + " subscription"
	}
	return "Claude subscription"
}

// SharedEnv is env (a process environment) for other members' work on a
// shared runner: without LoginEnv, with configDir as Claude Code's config
// folder (CLAUDE_CONFIG_DIR), so it can't find the owner's login, and
// keeping KeyEnv. Project commands of that work get it too.
func SharedEnv(env []string, configDir string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if slices.Contains(LoginEnv, k) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "CLAUDE_CONFIG_DIR="+configDir)
}

// withoutKey is env without KeyEnv: how `claude auth status` shows the
// login Claude Code would fall back to.
func withoutKey(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool { return strings.HasPrefix(kv, KeyEnv+"=") })
}

// HasKey reports whether this process has an API key in KeyEnv.
func HasKey() bool { return strings.TrimSpace(os.Getenv(KeyEnv)) != "" }
