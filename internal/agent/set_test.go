package agent

import (
	"slices"
	"testing"
	"time"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/model"
)

func TestFeatures(t *testing.T) {
	c, _ := NewProvider(DefaultProvider, Config{})
	want := []string{model.FeatureResume, model.FeatureMCP, model.FeaturePermissionHook, model.FeatureToolRules, model.FeatureUsage}
	if got := Features(c); !slices.Equal(got, want) {
		t.Errorf("Claude Code: %v, want %v", got, want)
	}
	if got := Features(bareProvider{}); len(got) != 0 {
		t.Errorf("bare provider: %v", got)
	}
	if got := ProviderFeatures("nobody"); got != nil {
		t.Errorf("unknown provider: %v", got)
	}
}

func TestStatusOf(t *testing.T) {
	// A provider that can't check its CLI isn't reported as installed.
	s := StatusOf(bareProvider{})
	if s.Name != "bare" || s.Installed || s.LoggedIn || len(s.Warnings) != 1 || s.CheckedAt.IsZero() || s.Ready() {
		t.Errorf("bare: %+v", s)
	}
	// Claude Code before its first check: not checked, so still ready.
	c, _ := NewProvider(DefaultProvider, Config{})
	s = StatusOf(c)
	if s.Name != "claude" || !s.CheckedAt.IsZero() || !s.Ready() || !s.Has(model.FeatureResume) {
		t.Errorf("unchecked claude: %+v", s)
	}
	now := time.Now()
	for _, x := range []struct {
		st                  claudecli.Status
		installed, loggedIn bool
	}{
		{claudecli.Status{CheckedAt: now, Warnings: []string{"Couldn't run `claude --version`"}}, false, false},
		{claudecli.Status{Version: "2.1.0", CheckedAt: now}, true, true}, // auth not understood: counts as logged in
		{claudecli.Status{Version: "2.1.0", CheckedAt: now, Auth: &claudecli.Auth{Method: claudecli.AuthNone}}, true, false},
		{claudecli.Status{Version: "2.1.0", CheckedAt: now, Auth: &claudecli.Auth{Method: claudecli.AuthAPIKey}}, true, true},
		{claudecli.Status{Version: "2.1.0", CheckedAt: now, Auth: &claudecli.Auth{Method: claudecli.AuthSubscription}}, true, true},
	} {
		s := ClaudeStatus(x.st)
		if s.Name != "claude" || s.Version != x.st.Version || s.Installed != x.installed || s.LoggedIn != x.loggedIn ||
			!slices.Equal(s.Warnings, x.st.Warnings) || !s.CheckedAt.Equal(now) || len(s.Features) != 5 {
			t.Errorf("%+v: %+v", x.st, s)
		}
		if s.Ready() != (x.installed && x.loggedIn) {
			t.Errorf("%+v: ready %v", x.st, s.Ready())
		}
	}
}

func TestSet(t *testing.T) {
	RegisterProvider("bare", func(Config) Provider { return bareProvider{} })
	defer UnregisterProvider("bare")
	s := NewSet(map[string]Config{"claude": {Bin: "/opt/claude"}})
	var names []string
	for _, p := range s.All() {
		names = append(names, p.Name())
	}
	if !slices.Equal(names, []string{"bare", "claude", "codex", "gemini"}) {
		t.Fatalf("set %v", names)
	}
	if c, ok := s.Get("").(*ClaudeCode); !ok || c.Bin != "/opt/claude" || s.Get("claude") != Provider(c) {
		t.Errorf("default %T", s.Get(""))
	}
	if s.For(model.Project{}) != s.Get("claude") || s.For(model.Project{Agent: "bare"}).Name() != "bare" || s.For(model.Project{Agent: "nobody"}) != nil {
		t.Error("For picks the wrong provider")
	}
	// Each provider gets its own config: Codex doesn't get Claude's binary.
	if x, ok := s.For(model.Project{Agent: "codex"}).(*Codex); !ok || x.Bin != "" || x.bin() != "codex" {
		t.Errorf("codex %+v", s.For(model.Project{Agent: "codex"}))
	}
	st := s.Statuses()
	if len(st) != 4 || st[0].Name != "bare" || st[1].Name != "claude" || st[2].Name != "codex" || st[3].Name != "gemini" || st[0].Installed {
		t.Errorf("statuses %+v", st)
	}
	var none *Set
	if none.All() != nil || none.Get("") != nil {
		t.Error("nil set")
	}
	infos := Providers()
	i := slices.IndexFunc(infos, func(p ProviderInfo) bool { return p.Name == "claude" })
	if i < 0 || infos[i].Title != "Claude Code" || !infos[i].Default || len(infos[i].Features) != 5 {
		t.Errorf("providers %+v", infos)
	}
}
