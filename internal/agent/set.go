package agent

import (
	"context"
	"time"

	"github.com/klahr/dash/internal/model"
)

// Set is one of every registered provider, built from one Config: the
// agents a Dash (a runner) can run, which it reports to its hub
// (Statuses), and the one each project's tasks run on (For).
type Set struct {
	list []Provider // in ProviderNames order
}

// NewSet builds every registered provider from cfg.
func NewSet(cfg Config) *Set {
	s := &Set{}
	for _, n := range ProviderNames() {
		if p, err := NewProvider(n, cfg); err == nil {
			s.list = append(s.list, p)
		}
	}
	return s
}

// All are the set's providers, by name.
func (s *Set) All() []Provider {
	if s == nil {
		return nil
	}
	return s.list
}

// Get is the provider called name ("": DefaultProvider), or nil.
func (s *Set) Get(name string) Provider {
	if name == "" {
		name = DefaultProvider
	}
	for _, p := range s.All() {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// For is the provider project p's tasks run on (model.Project.AgentName),
// or nil when this Dash doesn't have it.
func (s *Set) For(p model.Project) Provider { return s.Get(p.AgentName()) }

// Statuses are what a runner reports of each of its agents (StatusOf).
func (s *Set) Statuses() []model.AgentStatus {
	out := []model.AgentStatus{}
	for _, p := range s.All() {
		out = append(out, StatusOf(p))
	}
	return out
}

// Run checks every provider's CLI now and then every `every` until ctx
// ends (claudecli.Monitor.Run), calling changed(p) when what was found
// about p's changed. Providers without a monitor aren't checked.
func (s *Set) Run(ctx context.Context, every time.Duration, changed func(p Provider)) {
	for _, p := range s.All() {
		m := MonitorOf(p)
		if m == nil {
			continue
		}
		if changed != nil {
			m.OnChange(func() { changed(p) })
		}
		go m.Run(ctx, every)
	}
}
