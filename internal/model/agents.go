package model

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// Agents are the agent CLIs (Claude Code, …) that run a project's tasks. A
// project picks one (Project.Agent; Claude Code by default), and its tasks
// go only to runners that can run it: each runner reports the agents it has
// (AgentStatus), and has a default label for each one that is installed
// and logged in (AgentLabels), which a project's labels then need too
// (Project.RunnerLabels).

// DefaultAgent is the agent provider a project without one runs on:
// Claude Code (agent.DefaultProvider).
const DefaultAgent = "claude"

// Agent features: what only some agent CLIs can do (the agent package's
// optional provider interfaces).
const (
	// FeatureResume: continues an earlier session (agent.Resumer).
	FeatureResume = "resume"
	// FeatureMCP: runs with MCP servers, e.g. Dash's task tools
	// (agent.MCPConfigurer).
	FeatureMCP = "mcp"
	// FeaturePermissionHook: asks Dash before a tool call it isn't
	// allowed to make outright (agent.PermissionHook).
	FeaturePermissionHook = "permission-hook"
	// FeatureToolRules: follows per-tool allow and deny rules, which
	// scans need to keep their reads in the checkout (agent.ToolRuler).
	FeatureToolRules = "tool-rules"
	// FeatureUsage: reports its account's usage limits
	// (agent.RateLimitReporter).
	FeatureUsage = "usage"
)

// Sign-in states of an installed agent CLI (AgentStatus.SignIn).
const (
	SignedIn  = "signed-in"
	SignedOut = "signed-out"
	// SignInUnknown: Dash couldn't tell, e.g. an ACP agent whose test
	// session failed for another reason than auth_required. It doesn't
	// keep tasks away (LoggedIn stays true), but isn't offered as ready.
	SignInUnknown = "unknown"
)

// AgentStatus is what a runner found about one agent CLI it can run.
type AgentStatus struct {
	// Name is the agent provider's registered name, e.g. "claude".
	Name string `json:"name"`
	// Title is the agent's own name for people, when it says one (an ACP
	// agent's initialize answer), e.g. "Gemini CLI".
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"` // "" when unknown
	// Installed: the CLI ran; LoggedIn: it isn't known to be signed out
	// (what runner labels go by).
	Installed bool `json:"installed"`
	LoggedIn  bool `json:"logged_in"`
	// SignIn is SignedIn, SignedOut or SignInUnknown ("" from an older
	// runner, or when it isn't installed: see SignInState).
	SignIn   string   `json:"sign_in,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	// Features are the optional abilities it has (FeatureResume, …).
	Features []string `json:"features,omitempty"`
	// CheckedAt is when it was last checked (zero: not yet, as on a
	// runner that just started).
	CheckedAt time.Time `json:"checked_at,omitzero"`
}

// Ready reports whether a can run tasks: installed and logged in, or not
// checked yet (a runner that just started doesn't lose its labels for a
// moment).
func (a AgentStatus) Ready() bool {
	return a.CheckedAt.IsZero() || a.Installed && a.LoggedIn
}

// SignInState is a's sign-in state (SignedIn, SignedOut or SignInUnknown),
// "" when it isn't installed or wasn't checked yet. From a runner older than
// SignIn it is read from LoggedIn.
func (a AgentStatus) SignInState() string {
	switch {
	case a.CheckedAt.IsZero() || !a.Installed:
		return ""
	case a.SignIn != "":
		return a.SignIn
	case a.LoggedIn:
		return SignedIn
	}
	return SignedOut
}

// Has reports whether a has feature f.
func (a AgentStatus) Has(f string) bool { return slices.Contains(a.Features, f) }

// AgentLabel is the default label of runners that can run agent provider
// name: the name itself, e.g. "claude".
func AgentLabel(name string) string { return cmp.Or(name, DefaultAgent) }

// AgentLabels are the default labels of a runner whose agents are agents:
// one per agent that is Ready. reported is false for a runner that hasn't
// said which agents it has (a runner older than agent reports): it runs
// Claude Code, as every runner did, so it keeps DefaultAgent's label.
func AgentLabels(agents []AgentStatus, reported bool) []string {
	if !reported {
		return []string{AgentLabel(DefaultAgent)}
	}
	out := []string{}
	for _, a := range agents {
		if a.Name != "" && a.Ready() && !HasLabel(out, AgentLabel(a.Name)) {
			out = append(out, AgentLabel(a.Name))
		}
	}
	return out
}

// FindAgent is the agent called name in agents (nil: none).
func FindAgent(agents []AgentStatus, name string) *AgentStatus {
	for i := range agents {
		if strings.EqualFold(agents[i].Name, name) {
			return &agents[i]
		}
	}
	return nil
}

// AgentName is the agent provider p's tasks run on (DefaultAgent when it
// picked none).
func (p Project) AgentName() string { return cmp.Or(p.Agent, DefaultAgent) }

// RunnerLabels are the labels a runner needs to work on p: the ones it
// runs on (RunsOn) and its agent's label (AgentLabel). Nil when RunsOn is
// empty: such a project runs on no runner.
func (p Project) RunnerLabels() []string {
	if len(p.RunsOn) == 0 {
		return nil
	}
	out := slices.Clone(p.RunsOn)
	if l := AgentLabel(p.AgentName()); !HasLabel(out, l) {
		out = append(out, l)
	}
	return out
}

// OnDefaultAgent is p for work that always runs on Claude Code
// (DefaultAgent) whatever agent p's tasks run on, such as scans and
// project chat: its RunnerLabels need DefaultAgent's label, not p's
// agent's.
func (p Project) OnDefaultAgent() Project {
	p.Agent = ""
	return p
}

// CleanAgent checks a project's agent provider name: "" (the default) or
// a name of up to 32 lowercase letters, digits and dashes, as provider
// names are. Whether it is a known provider is the caller's to check.
func CleanAgent(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if len(name) > 32 {
		return "", false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return "", false
		}
	}
	return name, true
}
