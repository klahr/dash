package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// A plan is its user's, in one org: another member, or the same user in
// another org, doesn't find it. It survives a restart, leaves the drafts
// once launched, and goes when its user leaves the org; their account
// export has it.
func TestPlansArePerUserAndOrg(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	acc := s.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-pass"); err != nil {
		t.Fatal(err)
	}
	admin, _ := acc.EnvUser()
	def, _ := acc.DefaultOrg()
	ann := addMember(t, acc, admin.ID, def.ID, "ann", RoleMember)
	other, err := acc.CreateOrg(ann, "Other")
	if err != nil {
		t.Fatal(err)
	}
	mine := s.ForOrg(def.ID).AsUser(ann)

	if _, err := s.ForOrg(def.ID).ListPlans(); !errors.Is(err, ErrNoUser) {
		t.Errorf("without a user: %v", err)
	}
	a, err := mine.CreatePlan(model.Plan{Idea: "Recipes", Step: "idea"})
	if err != nil || a.ID == "" || a.CreatedAt.IsZero() {
		t.Fatalf("create: %+v %v", a, err)
	}
	b, _ := mine.CreatePlan(model.Plan{Idea: "Plants", Step: "idea"})
	for name, o := range map[string]*OrgStore{"another member": s.ForOrg(def.ID).AsUser(admin.ID), "another org": s.ForOrg(other.ID).AsUser(ann)} {
		if _, err := o.GetPlan(a.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s reads it: %v", name, err)
		}
		if _, err := o.UpdatePlan(a.ID, func(p *model.Plan) error { p.Idea = "x"; return nil }); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s changes it: %v", name, err)
		}
		if err := o.DeletePlan(a.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s deletes it: %v", name, err)
		}
		if l, _ := o.ListPlans(); len(l) != 0 {
			t.Errorf("%s lists %+v", name, l)
		}
	}

	// Changing one: the most recent first; an error changes nothing.
	time.Sleep(1100 * time.Millisecond) // updated_at is in seconds
	if _, err := mine.UpdatePlan(a.ID, func(p *model.Plan) error { p.Brief = "# Recipes"; p.ID = "hijack"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := mine.UpdatePlan(a.ID, func(p *model.Plan) error { p.Brief = "lost"; return errors.New("no") }); err == nil {
		t.Fatal("fn's error was ignored")
	}
	s.Close()
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	mine = s.ForOrg(def.ID).AsUser(ann)
	acc = s.Accounts()
	l, err := mine.ListPlans()
	if err != nil || len(l) != 2 || l[0].ID != a.ID || l[0].Brief != "# Recipes" || l[1].ID != b.ID {
		t.Fatalf("drafts: %+v %v", l, err)
	}

	// Launched: no longer a draft, still readable.
	at := time.Now()
	if _, err := mine.UpdatePlan(b.ID, func(p *model.Plan) error { p.LaunchedAt = &at; return nil }); err != nil {
		t.Fatal(err)
	}
	if l, _ := mine.ListPlans(); len(l) != 1 || l[0].ID != a.ID {
		t.Errorf("drafts after launching: %+v", l)
	}
	if p, err := mine.GetPlan(b.ID); err != nil || p.LaunchedAt == nil {
		t.Errorf("launched plan: %+v %v", p, err)
	}

	x, err := acc.ExportUser(ann, time.Now())
	if err != nil || len(x.Plans) != 2 || x.Plans[0].OrgID != def.ID || !strings.Contains(string(x.Plans[0].Plan), `"idea"`) {
		t.Errorf("export: %+v %v", x.Plans, err)
	}

	// Leaving the org takes their plans there.
	if _, err := s.ForOrg(other.ID).AsUser(ann).CreatePlan(model.Plan{Idea: "Elsewhere", Step: "idea"}); err != nil {
		t.Fatal(err)
	}
	if err := acc.RemoveMember(ann, def.ID, ann); err != nil {
		t.Fatal(err)
	}
	if _, err := mine.GetPlan(a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after leaving: %v", err)
	}
	if l, _ := s.ForOrg(other.ID).AsUser(ann).ListPlans(); len(l) != 1 {
		t.Errorf("plans in another org went too: %+v", l)
	}
}
