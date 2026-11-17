package dispatch

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// MigrateRunnerLinks moves a hub's projects from runner lists to labels,
// once, when the hub starts: before, a project named the runners it ran on
// (model.Project.LegacyRunners); now it runs on labels, and its runners are
// those that have them. Each project keeps the runners it had:
//
//   - one linked to every runner of its org runs on "self-hosted", which
//     every runner has (so runners added later work on it too);
//   - any other gets a label of its own ("project-<name>"), which its
//     runners are given, and runs on it.
func MigrateRunnerLinks(st *store.Store, rh *tunnel.Hub) {
	all := rh.List()
	used := map[int64][]string{} // org → labels given out here
	for _, p := range st.ListProjects() {
		if len(p.LegacyRunners) == 0 {
			continue
		}
		var org, linked []string
		for _, r := range all {
			if r.OrgID() != p.OrgID {
				continue
			}
			org = append(org, r.ID)
			if slices.Contains(p.LegacyRunners, r.ID) {
				linked = append(linked, r.ID)
			}
		}
		runsOn := []string{model.LabelSelfHosted}
		if len(linked) == 0 || len(linked) < len(org) {
			label := projectLabel(p, used[p.OrgID], all)
			used[p.OrgID] = append(used[p.OrgID], label)
			rh.AddLabel(linked, label)
			runsOn = []string{label}
		}
		if _, err := st.UpdateProject(p.ID, func(x *model.Project) error {
			x.RunsOn, x.LegacyRunners = runsOn, nil
			return nil
		}); err != nil {
			slog.Error("move a project's runners to labels", "project", p.Name, "err", err)
			continue
		}
		slog.Info("project now runs on runner labels", "project", p.Name, "runs_on", strings.Join(runsOn, ", "), "runners", len(linked))
	}
}

// projectLabel is a label of p's own for MigrateRunnerLinks:
// "project-<name>", in letters, digits and dashes, not one of taken nor one
// any runner of p's org has already.
func projectLabel(p model.Project, taken []string, runners []tunnel.Runner) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(p.Name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > 40 {
		name = strings.Trim(name[:40], "-")
	}
	if name == "" {
		name = p.ID
	}
	base := "project-" + name
	inUse := func(l string) bool {
		if model.HasLabel(taken, l) {
			return true
		}
		return slices.ContainsFunc(runners, func(r tunnel.Runner) bool { return r.OrgID() == p.OrgID && model.HasLabel(r.AllLabels(), l) })
	}
	label := base
	for i := 2; inUse(label); i++ {
		label = fmt.Sprintf("%s-%d", base, i)
	}
	return label
}
