package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// tagged reads POST /tags's answer, wanting code.
func tagged(t *testing.T, w *httptest.ResponseRecorder, code int) taggedRelease {
	t.Helper()
	var out taggedRelease
	if w.Code != code || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("tag: %d, want %d: %s", w.Code, code, w.Body)
	}
	return out
}

// Deploy's picker: "Tag 1.4.1 and deploy" tags the tip of origin's branch,
// pushes the tag and deploys that commit to the environment asked for,
// once. The environments that deploy each release tag deploy it on their
// own; asking one of them for the deploy queues no second one.
func TestTagAndDeploy(t *testing.T) {
	parallel(t)
	rr := newReleaseRepo(t, "X.Y.Z")
	holdDeploys(rr.s)
	rr.p = withEnvs(t, rr.s, rr.p,
		model.Environment{Name: "staging", DeployCommand: "true"},
		model.Environment{Name: "production", DeployCommand: "true", Trigger: model.EnvTriggerRelease})
	rgit(t, rr.repo, "tag", "1.0.0")
	rgit(t, rr.repo, "push", "-q", "origin", "1.0.0")
	sha := rr.commit(t, "Add login", "", true)

	// The picker's facts: the tip is one commit past 1.0.0, untagged.
	info := rr.info(t, "")
	if info.SHA != sha || info.Since == nil || *info.Since != 1 || len(info.ShaTags) != 0 || info.Suggestions.Patch != "1.0.1" {
		t.Fatalf("info %+v", info)
	}

	w := rr.call(t, "POST", "/tags", `{"bump":"patch","deploy":"staging"}`)
	out := tagged(t, w, http.StatusCreated)
	if out.Tag != "1.0.1" || out.SHA != sha || out.Triggered || out.DeployError != "" || out.Deploy == nil {
		t.Fatalf("tag and deploy: %s", w.Body)
	}
	if d := out.Deploy; d.Environment != "staging" || d.Ref != sha || d.Tag != "1.0.1" || d.Trigger != model.DeployByUser || d.Status != model.DeployQueued {
		t.Errorf("staging deploy: %+v", d)
	}
	if got := rgit(t, rr.origin, "rev-parse", "1.0.1^{commit}"); got != sha {
		t.Errorf("origin's 1.0.1 is %s", got)
	}
	staging, prod := raw(rr.s).ListDeploys(rr.p.ID, "staging"), raw(rr.s).ListDeploys(rr.p.ID, "production")
	if len(staging) != 1 || len(prod) != 1 || prod[0].Trigger != model.DeployByRelease || prod[0].TriggerRef != "1.0.1" {
		t.Fatalf("deploys: staging %+v, production %+v", staging, prod)
	}
	// Now tagged: the picker offers no new release.
	if info := rr.info(t, ""); strings.Join(info.ShaTags, ",") != "1.0.1" || info.Since == nil || *info.Since != 0 {
		t.Errorf("tagged tip: %+v", info)
	}

	// Production deploys each release tag: its trigger's deploy is the one.
	sha2 := rr.commit(t, "Fix logout", "", true)
	w = rr.call(t, "POST", "/tags", `{"bump":"patch","deploy":"production"}`)
	out = tagged(t, w, http.StatusCreated)
	if out.Tag != "1.0.2" || !out.Triggered || out.Deploy == nil || out.Deploy.Trigger != model.DeployByRelease || out.Deploy.Ref != sha2 {
		t.Fatalf("release to production: %s", w.Body)
	}
	if prod := raw(rr.s).ListDeploys(rr.p.ID, "production"); len(prod) != 2 {
		t.Errorf("production deploys: %d, want 2 (one per release)", len(prod))
	}
	if n := len(raw(rr.s).ListDeploys(rr.p.ID, "staging")); n != 1 {
		t.Errorf("staging deploys: %d", n)
	}

	// An environment the project hasn't: refused before anything is tagged.
	rr.commit(t, "More", "", true)
	if w := rr.call(t, "POST", "/tags", `{"bump":"patch","deploy":"nowhere"}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown environment: %d %s", w.Code, w.Body)
	}
	if strings.Contains(rgit(t, rr.repo, "ls-remote", "--tags", "origin"), "1.0.3") {
		t.Error("tagged for an unknown environment")
	}
}

// The deploy of a release is refused, after the tag, for what needs the
// tagged commit (a requirement it doesn't meet): the tag stays, and the
// answer says why there was no deploy.
func TestTagAndDeployRefusedAfterTagging(t *testing.T) {
	parallel(t)
	rr := newReleaseRepo(t, "X.Y.Z")
	holdDeploys(rr.s)
	rr.p = withEnvs(t, rr.s, rr.p,
		model.Environment{Name: "staging", DeployCommand: "true"},
		model.Environment{Name: "production", DeployCommand: "true", RequireLiveOn: "staging"})
	rr.commit(t, "Add login", "", true)
	w := rr.call(t, "POST", "/tags", `{"bump":"minor","deploy":"production"}`)
	out := tagged(t, w, http.StatusCreated)
	if out.Tag != "0.1.0" || out.Deploy != nil || !strings.Contains(out.DeployError, "has not been live on staging") {
		t.Fatalf("refused deploy: %s", w.Body)
	}
}

// Who may tag and deploy: tagging needs an owner or admin, the deploy the
// environment's deploy role; both are checked before anything is tagged,
// and a freeze refuses it too.
func TestTagAndDeployPermissions(t *testing.T) {
	parallel(t)
	rr := newReleaseRepo(t, "X.Y.Z")
	holdDeploys(rr.s)
	rr.p = withEnvs(t, rr.s, rr.p,
		model.Environment{Name: "staging", DeployCommand: "true"},
		model.Environment{Name: "vault", DeployCommand: "true", DeployRole: model.RoleOwner},
		model.Environment{Name: "frozen", DeployCommand: "true",
			Freezes: []model.Freeze{{From: "2000-01-01 00:00", To: "2999-01-01 00:00", Note: "code freeze"}}})
	rr.commit(t, "Add login", "", true)
	owner := testOwner(t, rr.s)
	member(t, rr.s, owner.Org, "m", store.RoleMember)
	member(t, rr.s, owner.Org, "a", store.RoleAdmin)
	m := loginAs(t, rr.h, "m", "long-enough-m")
	a := loginAs(t, rr.h, "a", "long-enough-a")
	path := "/api/projects/" + rr.p.ID + "/tags"

	// A member may deploy to staging, but not tag: refused, and told why in the picker.
	if w := send(m, "POST", path, `{"bump":"patch","deploy":"staging"}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "owners and admins") {
		t.Errorf("member: %d %s", w.Code, w.Body)
	}
	var info releaseInfo
	if w := send(m, "GET", "/api/projects/"+rr.p.ID+"/release", ""); json.Unmarshal(w.Body.Bytes(), &info) != nil || info.CanTag || info.CantTag == "" {
		t.Errorf("member's info: %d %s", w.Code, w.Body)
	}
	// An admin may tag, but not deploy where only owners may.
	if w := send(a, "POST", path, `{"bump":"patch","deploy":"vault"}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "deploy to vault") {
		t.Errorf("admin to vault: %d %s", w.Code, w.Body)
	}
	// Frozen: nobody.
	if w := rr.call(t, "POST", "/tags", `{"bump":"patch","deploy":"frozen"}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "code freeze") {
		t.Errorf("frozen: %d %s", w.Code, w.Body)
	}
	if tags := rgit(t, rr.repo, "ls-remote", "--tags", "origin"); tags != "" {
		t.Fatalf("tagged anyway: %s", tags)
	}
	if n := len(raw(rr.s).ListDeploys(rr.p.ID, "")); n != 0 {
		t.Errorf("deploys queued: %d", n)
	}
	// The admin to staging: tagged and deployed.
	w := send(a, "POST", path, `{"bump":"patch","deploy":"staging"}`)
	if out := tagged(t, w, http.StatusCreated); out.Deploy == nil || out.Deploy.Environment != "staging" {
		t.Errorf("admin to staging: %s", w.Body)
	}
}

// The chain of environments: staging promotes to production, so
// production's picker offers what is live on staging when it is ahead,
// and staging (first in the chain) has nothing to promote from.
func TestPromoteChain(t *testing.T) {
	parallel(t)
	rr := newReleaseRepo(t, "X.Y.Z")
	holdDeploys(rr.s)
	rr.p = withEnvs(t, rr.s, rr.p,
		model.Environment{Name: "staging", DeployCommand: "true"},
		model.Environment{Name: "production", DeployCommand: "true"})
	sha1 := rr.commit(t, "One", "", true)
	sha2 := rr.commit(t, "Two", "", true)
	envs := func() map[string]envStatus {
		t.Helper()
		w := rr.call(t, "GET", "/environments", "")
		var list []envStatus
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil {
			t.Fatalf("environments: %d %s", w.Code, w.Body)
		}
		out := map[string]envStatus{}
		for _, e := range list {
			out[e.Name] = e
		}
		return out
	}
	now := time.Now()

	// Nothing live anywhere: nothing to promote.
	if e := envs(); e["staging"].Policy.PromoteFrom != "" || e["production"].Policy.PromoteFrom != "staging" || e["production"].Policy.Promote != nil {
		t.Fatalf("empty chain: %+v", e)
	}
	// Live on staging only: production may promote it.
	liveDeploy(t, rr.s, rr.p, "staging", sha1, now.Add(-time.Hour))
	if p := envs()["production"].Policy.Promote; p == nil || p.From != "staging" || p.Deploy.Commit != sha1 {
		t.Fatalf("staging only: %+v", p)
	}
	// The same on both: nothing to promote.
	liveDeploy(t, rr.s, rr.p, "production", sha1, now.Add(-30*time.Minute))
	if p := envs()["production"].Policy.Promote; p != nil {
		t.Fatalf("same commit: %+v", p)
	}
	// Staging ahead: production is offered it.
	liveDeploy(t, rr.s, rr.p, "staging", sha2, now.Add(-10*time.Minute))
	e := envs()
	if p := e["production"].Policy.Promote; p == nil || p.Deploy.Commit != sha2 {
		t.Fatalf("staging ahead: %+v", p)
	}
	if e["staging"].Policy.Promote != nil {
		t.Errorf("staging offered a promotion: %+v", e["staging"].Policy.Promote)
	}
	// Production ahead (a direct release): staging's older commit isn't offered.
	liveDeploy(t, rr.s, rr.p, "staging", sha1, now.Add(-5*time.Minute))
	liveDeploy(t, rr.s, rr.p, "production", sha2, now.Add(-time.Minute))
	if p := envs()["production"].Policy.Promote; p != nil {
		t.Errorf("production ahead: %+v", p)
	}
}

func TestPromoteSource(t *testing.T) {
	envs := []model.Environment{{Name: "dev"}, {Name: "staging"}, {Name: "production", RequireLiveOn: "staging"}}
	for i, want := range []string{"", "dev", "staging"} {
		if got := promoteSource(envs, i); got != want {
			t.Errorf("promoteSource(%s) = %q, want %q", envs[i].Name, got, want)
		}
	}
}
