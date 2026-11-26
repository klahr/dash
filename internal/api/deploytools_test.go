package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
)

// holdDeploys keeps s's deploy queue from running anything: the tests look
// at what is queued, not at deploys running.
func holdDeploys(s *Server) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := s.deployer()
	e.mu.Lock()
	e.ctx = ctx
	e.mu.Unlock()
}

// withEnvs gives p the environments envs, as stored (cleaned).
func withEnvs(t *testing.T, s *Server, p model.Project, envs ...model.Environment) model.Project {
	t.Helper()
	clean, err := model.CleanEnvironments(envs)
	if err != nil {
		t.Fatal(err)
	}
	p, err = raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.Environments = clean; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// merged marks task id merged at sha (through queued: a draft can't be).
func merged(t *testing.T, s *Server, id, sha string) model.Todo {
	t.Helper()
	if _, err := raw(s).UpdateTodo(id, func(x *model.Todo) error { x.Status = model.StatusQueued; return nil }); err != nil {
		t.Fatal(err)
	}
	td, err := raw(s).UpdateTodo(id, func(x *model.Todo) error { x.Status, x.MergedCommit = model.StatusMerged, sha; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return td
}

// liveDeploy records a deploy of commit to env that went live at at,
// shipping tasks; prev is the deploy it replaced (superseded now), if any.
func liveDeploy(t *testing.T, s *Server, p model.Project, env, commit string, at time.Time, tasks ...string) model.Deploy {
	t.Helper()
	for _, d := range raw(s).ListDeploys(p.ID, env) {
		if d.Status == model.DeployLive {
			_, _ = raw(s).UpdateDeploy(d.ID, func(x *model.Deploy) error { x.SetStatus(model.DeploySuperseded, at); return nil })
		}
	}
	// Deploys list newest first by when they were made, ties by random ID:
	// on a coarse clock (Windows) two made in a row could swap. Wait for the
	// clock to pass the newest, so this one is newer than all before it.
	if ds := raw(s).ListDeploys(p.ID, env); len(ds) > 0 {
		for !time.Now().After(ds[0].CreatedAt) {
			time.Sleep(time.Millisecond)
		}
	}
	d, err := raw(s).CreateDeploy(model.Deploy{ProjectID: p.ID, Environment: env, Ref: commit, Trigger: model.DeployByUser})
	if err != nil {
		t.Fatal(err)
	}
	d, err = raw(s).UpdateDeploy(d.ID, func(x *model.Deploy) error {
		x.Commit = commit
		for _, id := range tasks {
			x.Shipped = append(x.Shipped, model.DeployShipped{Kind: model.LinkTask, ID: id, Title: "task " + id})
		}
		x.SetStatus(model.DeployDeploying, at.Add(-time.Minute))
		x.SetStatus(model.DeployLive, at)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// Claude's deploy tools: it may deploy, roll back and promote only where
// "Who may deploy" is member, whatever its user's role (here the org's
// owner), its deploys wait for approvals like anyone's, and it can never
// approve one, through any tool or route.
func TestDeployToolsAgentRules(t *testing.T) {
	s, _, p := boardServer(t)
	holdDeploys(s)
	h := s.Routes()
	p = withEnvs(t, s, p,
		model.Environment{Name: "staging", DeployCommand: "true"},                          // members deploy, no approval
		model.Environment{Name: "production", DeployCommand: "true"},                       // admins deploy and approve
		model.Environment{Name: "qa", DeployCommand: "true", ApproveRole: model.RoleAdmin}) // members deploy, an admin approves
	token, release := s.Board.Register(chatCaller(t, s), p.ID, nil)
	defer release()
	call := func(tool, args string) (string, bool) { return toolCall(h, token, tool, args) }
	const sha = "0123456789abcdef0123456789abcdef01234567"

	// The tools are there; nothing to approve with.
	var list struct {
		Result struct{ Tools []struct{ Name string } }
	}
	_ = json.Unmarshal(rpc(h, token, "tools/list", "{}").Body.Bytes(), &list)
	var names []string
	for _, tl := range list.Result.Tools {
		names = append(names, tl.Name)
	}
	for _, want := range []string{"list_deploys", "deploy", "rollback", "promote"} {
		if !slices.Contains(names, want) {
			t.Errorf("no %s tool in %v", want, names)
		}
	}
	for _, n := range names {
		if strings.Contains(n, "approve") && strings.Contains(n, "deploy") {
			t.Errorf("an approving tool: %s", n)
		}
	}

	// Where only admins may deploy, Claude may not, though its user is the owner.
	if out, isErr := call("deploy", `{"environment":"production"}`); !isErr || !strings.Contains(out, "Claude may deploy only where any member may") {
		t.Errorf("deploy to production: %v %s", isErr, out)
	}
	// Where members may, it may: recorded as Claude's, for its user.
	out, isErr := call("deploy", `{"environment":"staging","commit":"`+sha+`"}`)
	if isErr {
		t.Fatalf("deploy to staging: %s", out)
	}
	var row mcptools.DeployRow
	_ = json.Unmarshal([]byte(out), &row)
	if row.Status != string(model.DeployQueued) || row.Trigger != model.DeployByAgent || row.TriggerRef != "chat" || row.By != "u" || row.Commit != sha {
		t.Errorf("staging deploy: %+v", row)
	}
	// Where a deploy needs an approval, Claude's waits for a person.
	out, isErr = call("deploy", `{"environment":"qa"}`)
	if isErr {
		t.Fatalf("deploy to qa: %s", out)
	}
	_ = json.Unmarshal([]byte(out), &row)
	if row.Status != string(model.DeployAwaiting) || row.Needs != model.RoleAdmin {
		t.Errorf("qa deploy: %+v", row)
	}
	// It can't approve it, though its user could.
	d, err := raw(s).GetDeploy(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	owner := testOwner(t, s)
	if _, err := s.decide(Caller{Org: owner.Org, User: owner.User, Agent: true}, p, d, true, ""); err == nil || !strings.Contains(err.Error(), "can't approve") {
		t.Errorf("an agent approved: %v", err)
	}
	if _, err := s.decide(Caller{Org: owner.Org, User: owner.User, Agent: true}, p, d, false, "no"); err == nil {
		t.Error("an agent rejected")
	}
	if d, _ = raw(s).GetDeploy(row.ID); d.Status != model.DeployAwaiting {
		t.Errorf("after the agent tried: %s", d.Status)
	}

	// Promote: to production (staging's next) no; to qa yes, awaiting approval.
	liveDeploy(t, s, p, "staging", sha, time.Now().Add(-time.Hour))
	if out, isErr := call("promote", `{"from":"staging"}`); !isErr || !strings.Contains(out, "production") {
		t.Errorf("promote to production: %v %s", isErr, out)
	}
	out, isErr = call("promote", `{"from":"staging","to":"qa"}`)
	if isErr {
		t.Fatalf("promote to qa: %s", out)
	}
	_ = json.Unmarshal([]byte(out), &row)
	if row.Status != string(model.DeployAwaiting) || row.PromotedEnv != "staging" || row.Trigger != model.DeployByAgent {
		t.Errorf("promotion: %+v", row)
	}

	// Rollback: in production no; in staging yes, to the last good deploy.
	liveDeploy(t, s, p, "production", sha, time.Now().Add(-2*time.Hour))
	liveDeploy(t, s, p, "production", strings.Repeat("b", 40), time.Now().Add(-time.Hour))
	if out, isErr := call("rollback", `{"environment":"production"}`); !isErr || !strings.Contains(out, "Claude may deploy only") {
		t.Errorf("rollback of production: %v %s", isErr, out)
	}
	// In staging: a newer deploy goes live, and Claude rolls it back (the
	// queued deploy from above doesn't stand in the way of a rollback).
	newer := strings.Repeat("c", 40)
	liveDeploy(t, s, p, "staging", newer, time.Now().Add(-time.Minute))
	out, isErr = call("rollback", `{"environment":"staging"}`)
	if isErr {
		t.Fatalf("rollback of staging: %s", out)
	}
	_ = json.Unmarshal([]byte(out), &row)
	if !row.Rollback || row.Commit != sha || row.Trigger != model.DeployByAgent || row.TriggerRef != "chat" {
		t.Errorf("rollback: %+v", row)
	}

	// list_deploys says where Claude may deploy.
	out, isErr = call("list_deploys", `{}`)
	if isErr {
		t.Fatalf("list_deploys: %s", out)
	}
	var listed struct {
		Environments []mcptools.EnvRow
		Deploys      []mcptools.DeployRow
	}
	_ = json.Unmarshal([]byte(out), &listed)
	may := map[string]bool{}
	for _, e := range listed.Environments {
		may[e.Name] = e.AgentMayDeploy
	}
	if !may["staging"] || !may["qa"] || may["production"] || len(listed.Environments) != 3 {
		t.Errorf("claude_may_deploy: %v", may)
	}
	if len(listed.Deploys) == 0 {
		t.Error("no deploys listed")
	}

	// A person, through the API, still deploys to production.
	ownerH := loggedIn(t, h)
	if w := do(t, ownerH, "POST", "/api/projects/"+p.ID+"/deploys", `{"environment":"production"}`); w.Code != http.StatusCreated {
		t.Errorf("the owner's deploy to production: %d %s", w.Code, w.Body)
	}
}

// A member's chat: Claude can't do more than its user may, either.
func TestDeployToolsMemberRole(t *testing.T) {
	s, _, p := boardServer(t)
	holdDeploys(s)
	h := s.Routes()
	p = withEnvs(t, s, p, model.Environment{Name: "staging", DeployCommand: "true", DeployRole: model.RoleAdmin})
	token, release := s.Board.Register(chatCaller(t, s), p.ID, nil)
	defer release()
	if out, isErr := toolCall(h, token, "deploy", `{"environment":"staging"}`); !isErr || !strings.Contains(out, "only an admin") {
		t.Errorf("deploy where admins deploy: %v %s", isErr, out)
	}
}

// Triggers: a merge deploys the environments that deploy on merge (one
// waiting deploy moves on to newer merges), a release tag those that
// deploy on release, through approvals and freezes like a person's deploy.
func TestDeployTriggers(t *testing.T) {
	s, _, p := boardServer(t)
	holdDeploys(s)
	p = withEnvs(t, s, p,
		model.Environment{Name: "staging", DeployCommand: "true", Trigger: model.EnvTriggerMerge},
		model.Environment{Name: "frozen", DeployCommand: "true", Trigger: model.EnvTriggerMerge,
			Freezes: []model.Freeze{{From: "2000-01-01 00:00", To: "2999-01-01 00:00", Note: "code freeze"}}},
		model.Environment{Name: "production", DeployCommand: "true", Trigger: model.EnvTriggerRelease},
		model.Environment{Name: "manual", DeployCommand: "true"})
	if e, _ := p.Environment("manual"); e.Trigger != model.EnvTriggerManual {
		t.Errorf("default trigger: %q", e.Trigger)
	}
	if _, err := model.CleanEnvironments([]model.Environment{{Name: "x", DeployCommand: "true", Trigger: "push"}}); err == nil {
		t.Error("an unknown trigger was taken")
	}

	old, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "old"})
	if err != nil {
		t.Fatal(err)
	}
	old = merged(t, s, old.ID, strings.Repeat("0", 40))
	s.primeMerges() // as at start: merged already, deploys nothing
	s.ObserveDeploys(model.Event{Type: model.EvTodoUpdated, Data: old})

	merge := func(prompt, sha string) model.Todo {
		t.Helper()
		td, err := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Prompt: prompt})
		if err != nil {
			t.Fatal(err)
		}
		td = merged(t, s, td.ID, sha)
		s.ObserveDeploys(model.Event{Type: model.EvTodoUpdated, Data: td})
		return td
	}
	sha1, sha2 := strings.Repeat("1", 40), strings.Repeat("2", 40)
	t1 := merge("one", sha1)
	var staging []model.Deploy
	eventually(t, "the merge's deploys", func() bool {
		staging = raw(s).ListDeploys(p.ID, "staging")
		return len(staging) == 1 && len(raw(s).ListDeploys(p.ID, "frozen")) == 1
	})
	if d := staging[0]; d.Status != model.DeployQueued || d.Trigger != model.DeployByMerge || d.Ref != sha1 || d.TriggerRef != t1.ID || d.By != 0 {
		t.Errorf("merge deploy: %+v", d)
	}
	if d := raw(s).ListDeploys(p.ID, "frozen")[0]; d.Status != model.DeployFailed || !strings.Contains(d.Error, "frozen") || !strings.Contains(d.Error, "code freeze") {
		t.Errorf("frozen merge deploy: %+v", d)
	}
	// The same task again: nothing more.
	s.ObserveDeploys(model.Event{Type: model.EvTodoUpdated, Data: t1})
	// Another merge while the first still waits: it moves on.
	t2 := merge("two", sha2)
	eventually(t, "the waiting deploy to move on", func() bool {
		staging = raw(s).ListDeploys(p.ID, "staging")
		return len(staging) == 1 && staging[0].Ref == sha2
	})
	if staging[0].TriggerRef != t2.ID {
		t.Errorf("moved on: %+v", staging[0])
	}
	if n := len(raw(s).ListDeploys(p.ID, "production")) + len(raw(s).ListDeploys(p.ID, "manual")); n != 0 {
		t.Errorf("a merge deployed elsewhere: %d", n)
	}

	// A release: production deploys the tag's commit, waiting for an admin
	// (production's default), recorded with the tag and who tagged.
	owner := testOwner(t, s)
	s.deployRelease(p, "1.0.0", sha2, Caller{Org: owner.Org, User: owner.User})
	prod := raw(s).ListDeploys(p.ID, "production")
	if len(prod) != 1 {
		t.Fatalf("release deploys: %+v", prod)
	}
	if d := prod[0]; d.Status != model.DeployAwaiting || d.Trigger != model.DeployByRelease || d.TriggerRef != "1.0.0" || d.Ref != sha2 || d.ByName != "u" {
		t.Errorf("release deploy: %+v", d)
	}
	if n := len(raw(s).ListDeploys(p.ID, "staging")); n != 1 {
		t.Errorf("a release deployed staging: %d", n)
	}
	// A runner's checkout of its hub's project triggers nothing: the hub does.
	linked, _ := raw(s).UpdateProject(p.ID, func(x *model.Project) error { x.HubProjectID, x.HubCheckout = "hp", "/x"; return nil })
	s.deployRelease(linked, "1.0.1", sha2, Caller{Org: owner.Org})
	if n := len(raw(s).ListDeploys(p.ID, "production")); n != 1 {
		t.Errorf("a linked project's release deployed: %d", n)
	}
}

// What is live where: History's markers, a task's "Live in", and a
// planning card's, from the deploy records alone (as on a hub).
func TestLiveIn(t *testing.T) {
	s, _, p := boardServer(t)
	holdDeploys(s)
	h := loggedIn(t, s.Routes())
	p = withEnvs(t, s, p, model.Environment{Name: "staging", DeployCommand: "true"}, model.Environment{Name: "production", DeployCommand: "true"})
	a, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "a", Title: "A"})
	b, _ := raw(s).CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "b", Title: "B"})
	for _, td := range []model.Todo{a, b} {
		merged(t, s, td.ID, strings.Repeat("a", 40))
	}
	day := func(n int) time.Time { return time.Date(2026, 11, n, 12, 0, 0, 0, time.UTC) }
	liveDeploy(t, s, p, "staging", strings.Repeat("1", 40), day(2), a.ID)
	liveDeploy(t, s, p, "staging", strings.Repeat("2", 40), day(3), b.ID)
	liveDeploy(t, s, p, "production", strings.Repeat("1", 40), day(4), a.ID)

	w := do(t, h, "GET", "/api/projects/"+p.ID+"/deploys/live", "")
	if w.Code != http.StatusOK {
		t.Fatalf("live: %d %s", w.Code, w.Body)
	}
	var live projectLive
	_ = json.Unmarshal(w.Body.Bytes(), &live)
	if len(live.Environments) != 2 || live.Environments[0].Commit != strings.Repeat("2", 40) || live.Environments[1].Commit != strings.Repeat("1", 40) {
		t.Errorf("live commits: %+v", live.Environments)
	}
	if got := live.Tasks[a.ID]; len(got) != 2 || got[1].Environment != "production" || !got[1].Since.Equal(day(4)) {
		t.Errorf("task A: %+v", got)
	}
	if got := live.Tasks[b.ID]; len(got) != 1 || got[0].Environment != "staging" {
		t.Errorf("task B: %+v", got)
	}

	// A card with A alone is live in production; with B too, only where both are.
	card := func(title string, tasks ...string) cardView {
		t.Helper()
		w := do(t, h, "POST", "/api/planning/cards", `{"title":"`+title+`"}`)
		var c cardView
		_ = json.Unmarshal(w.Body.Bytes(), &c)
		for _, id := range tasks {
			if w := do(t, h, "POST", "/api/planning/cards/"+c.ID+"/links", `{"kind":"task","id":"`+id+`"}`); w.Code >= 300 {
				t.Fatalf("link: %d %s", w.Code, w.Body)
			}
		}
		w = do(t, h, "GET", "/api/planning/cards/"+c.ID, "")
		_ = json.Unmarshal(w.Body.Bytes(), &c)
		return c
	}
	if c := card("only A", a.ID); c.Status.Text != "Live in production since 4 Nov 2026" || c.Status.Tone != "live" || c.Status.Live == nil {
		t.Errorf("card A: %+v", c.Status)
	} else if c.Links[0].State != "Live in production since 4 Nov 2026" {
		t.Errorf("card A's task: %+v", c.Links[0])
	}
	if c := card("A and B", a.ID, b.ID); c.Status.Text != "Live in staging since 3 Nov 2026" {
		t.Errorf("card A+B: %+v", c.Status)
	}
}
