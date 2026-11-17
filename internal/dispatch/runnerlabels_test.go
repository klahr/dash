package dispatch

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
)

// A hub's projects that named their runners run on labels after the hub
// starts, on the same runners: one linked to every runner of its org on
// "any", any other on a label of its own that its runners are
// given. Projects of another org, and projects without runners, are left
// as they were.
func TestMigrateRunnerLinksKeepsEachProjectsRunners(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	rh := &tunnel.Hub{Accounts: fx.st.Accounts()}
	register := registerer(t, fx, rh)
	a, b, beta := register("a", fx.orgA), register("b", fx.orgA), register("beta", fx.orgB)
	mk := func(name string, org int64, runners ...string) model.Project {
		t.Helper()
		p, err := fx.st.CreateProject(model.Project{Name: name, OrgID: org, RemoteURL: "https://github.com/x/" + name, LegacyRunners: runners})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	all := mk("All of them", fx.orgA, a, b)
	one := mk("Web site!", fx.orgA, a)
	twin := mk("web site", fx.orgA, b) // the same label, but for the "-2"
	other := mk("beta's", fx.orgB, beta)
	gone := mk("gone", fx.orgA, "revoked-runner")
	none := mk("none", fx.orgA)

	MigrateRunnerLinks(fx.st, rh)

	get := func(p model.Project) model.Project {
		t.Helper()
		p, err := fx.st.GetProject(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.LegacyRunners) != 0 {
			t.Errorf("%s still names runners: %v", p.Name, p.LegacyRunners)
		}
		return p
	}
	serving := func(p model.Project) []string {
		var out []string
		for _, r := range tunnel.Serving(rh.List(), p) {
			out = append(out, r.Name)
		}
		return out
	}
	for _, c := range []struct {
		p       model.Project
		runsOn  []string
		runners []string
	}{
		{all, []string{"any"}, []string{"a", "b"}},
		{one, []string{"project-web-site"}, []string{"a"}},
		{twin, []string{"project-web-site-2"}, []string{"b"}},
		{other, []string{"any"}, []string{"beta"}},
		{gone, []string{"project-gone"}, nil},
		{none, nil, nil},
	} {
		p := get(c.p)
		if !slices.Equal(p.RunsOn, c.runsOn) || !slices.Equal(serving(p), c.runners) {
			t.Errorf("%s runs on %v (%v), want %v (%v)", p.Name, p.RunsOn, serving(p), c.runsOn, c.runners)
		}
	}

	// Once is enough: starting again changes nothing.
	before, _ := rh.Get(a)
	MigrateRunnerLinks(fx.st, rh)
	if after, _ := rh.Get(a); !slices.Equal(after.Labels, before.Labels) || !slices.Equal(before.Labels, []string{"project-web-site"}) {
		t.Errorf("a's labels: %v, then %v", before.Labels, after.Labels)
	}
}

// registerer registers runners of the user with fx's environment on rh,
// returning each one's id.
func registerer(t *testing.T, fx *fixture, rh *tunnel.Hub) func(name string, org int64) string {
	u, _ := fx.st.Accounts().EnvUser()
	return func(name string, org int64) string {
		t.Helper()
		code, _, err := rh.NewCode(model.Owner{User: u.ID, Org: org})
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]string{"code": code, "name": name})
		w := httptest.NewRecorder()
		rh.ServeRegister(w, httptest.NewRequest("POST", "/runner/register", bytes.NewReader(body)))
		var res struct{ ID string }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &res) != nil {
			t.Fatalf("register %s: %d %s", name, w.Code, w.Body)
		}
		return res.ID
	}
}

// Projects and runners move off "self-hosted", which every runner had
// before "any" took its place, when the hub starts: a project on it alone
// runs on "any", one on other labels as well drops it, and a runner given
// it by hand loses it. Every runner of the org still works on a project
// that ran on it alone.
func TestMigrateRunnerLinksDropsSelfHosted(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	rh := &tunnel.Hub{Accounts: fx.st.Accounts()}
	register := registerer(t, fx, rh)
	a, b := register("a", fx.orgA), register("b", fx.orgA)
	if err := rh.SetLabels(a, []string{"gpu", "Self-Hosted"}); err != nil {
		t.Fatal(err)
	}
	if err := rh.SetLabels(b, []string{"self-hosted"}); err != nil {
		t.Fatal(err)
	}
	mk := func(name string, runsOn ...string) model.Project {
		t.Helper()
		p, err := fx.st.CreateProject(model.Project{Name: name, OrgID: fx.orgA, RemoteURL: "https://github.com/x/" + name, RunsOn: runsOn})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	alone := mk("alone", "self-hosted")
	more := mk("more", "SELF-HOSTED", "gpu")
	gpu := mk("gpu", "gpu")
	local := mk("local")

	MigrateRunnerLinks(fx.st, rh)

	for _, c := range []struct {
		p       model.Project
		runsOn  []string
		runners []string
	}{
		{alone, []string{"any"}, []string{"a", "b"}},
		{more, []string{"gpu"}, []string{"a"}},
		{gpu, []string{"gpu"}, []string{"a"}},
		{local, nil, nil},
	} {
		p, err := fx.st.GetProject(c.p.ID)
		if err != nil {
			t.Fatal(err)
		}
		var serving []string
		for _, r := range tunnel.Serving(rh.List(), p) {
			serving = append(serving, r.Name)
		}
		if !slices.Equal(p.RunsOn, c.runsOn) || !slices.Equal(serving, c.runners) {
			t.Errorf("%s runs on %v (%v), want %v (%v)", p.Name, p.RunsOn, serving, c.runsOn, c.runners)
		}
	}
	if r, _ := rh.Get(a); !slices.Equal(r.Labels, []string{"gpu"}) {
		t.Errorf("a's labels: %v", r.Labels)
	}
	if r, _ := rh.Get(b); len(r.Labels) != 0 {
		t.Errorf("b's labels: %v", r.Labels)
	}
}
