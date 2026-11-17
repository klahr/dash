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
// "self-hosted", any other on a label of its own that its runners are
// given. Projects of another org, and projects without runners, are left
// as they were.
func TestMigrateRunnerLinksKeepsEachProjectsRunners(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	acc := fx.st.Accounts()
	u, _ := acc.EnvUser()
	rh := &tunnel.Hub{Accounts: acc}
	register := func(name string, org int64) string {
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
		{all, []string{"self-hosted"}, []string{"a", "b"}},
		{one, []string{"project-web-site"}, []string{"a"}},
		{twin, []string{"project-web-site-2"}, []string{"b"}},
		{other, []string{"self-hosted"}, []string{"beta"}},
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
