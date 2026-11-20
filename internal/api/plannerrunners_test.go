package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// plannerFleet is a hub that doesn't run Claude, with a runner of a
// planner's (pat, online) and one of the owner's (not connected).
type plannerFleet struct {
	hub, runner       *Server
	small             *fakePlanner
	project           model.Project
	rh                *tunnel.Hub
	planner           http.Handler // the hub's routes, signed in as pat
	plannerID         int64
	plannerWS         string // ws:// URL of pat's /ws/chat
	patsID, ownersID  string // the runners' ids
	owner             http.Handler
	plannerRunnerName string
}

func newPlannerFleet(t *testing.T) *plannerFleet {
	t.Helper()
	f := &plannerFleet{small: &fakePlanner{replies: []string{featureRound}}, plannerRunnerName: "pats-mac"}
	hub, _, p := boardServer(t)
	f.hub, f.project = hub, p
	f.rh = &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: f.rh}
	hub.Titler, hub.Refiner, hub.Chat = &fakeSmall{where: "hub"}, &fakeSmall{where: "hub"}, noChat{t}
	srv := httptest.NewServer(hub.Routes())
	t.Cleanup(srv.Close)

	owner := testOwner(t, hub)
	f.plannerID = member(t, hub, owner.Org, "pat", store.RolePlanner)
	f.owner = loginAs(t, hub.Routes(), "u", "p")
	f.planner = loginAs(t, hub.Routes(), "pat", "long-enough-pat")
	ps := httptest.NewServer(f.planner)
	t.Cleanup(ps.Close)
	f.plannerWS = "ws" + strings.TrimPrefix(ps.URL, "http") + "/ws/chat"

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// The owner's runner: registered, never connected.
	code, _, err := f.rh.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	oc, err := tunnel.Register(ctx, srv.URL, code, "owners-box", "")
	if err != nil {
		t.Fatal(err)
	}
	f.ownersID = oc.ID

	// pat pairs a runner the way members do: a code from the Runners sheet.
	var out struct{ Code string }
	if w := send(f.planner, "POST", "/api/runners/codes", ""); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("planner's code: %d %s", w.Code, w.Body)
	}
	creds, err := tunnel.Register(ctx, srv.URL, out.Code, f.plannerRunnerName, "")
	if err != nil {
		t.Fatal(err)
	}
	if creds.UserID != f.plannerID {
		t.Fatalf("runner registered for %d, want pat %d", creds.UserID, f.plannerID)
	}
	f.patsID = creds.ID
	runner, h, _ := runnerSide(t, creds.OrgID)
	runner.HubUser, runner.Small, runner.Chat = creds.UserID, f.small, &echoChat{}
	runner.ChatDir = filepath.Join(t.TempDir(), "chat")
	f.runner = runner
	client := &tunnel.Client{Creds: creds, Handler: h, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "pat's runner to come online", func() bool { r, _ := f.rh.Get(creds.ID); return r.Online })
	return f
}

// A planner pairs, sees, renames and removes their own runner, and no one
// else's; it is marked Claude-only, and there is nothing else to set on it.
func TestPlannerSeesOnlyTheirOwnRunners(t *testing.T) {
	parallel(t)
	f := newPlannerFleet(t)

	got := runnerNames(t, send(f.planner, "GET", "/api/runners", ""))
	if len(got) != 1 || !got[f.plannerRunnerName].Mine || !got[f.plannerRunnerName].ClaudeOnly || !got[f.plannerRunnerName].CanManage {
		t.Fatalf("planner's runners: %+v", got)
	}
	// The owner sees both, pat's marked as a planner's.
	all := runnerNames(t, send(f.owner, "GET", "/api/runners", ""))
	if len(all) != 2 || !all[f.plannerRunnerName].ClaudeOnly || all["owners-box"].ClaudeOnly {
		t.Fatalf("owner's list: %+v", all)
	}

	// Another's runner is not there for a planner: not found.
	for _, x := range []struct{ method, path, body string }{
		{"PATCH", "/api/runners/" + f.ownersID, `{"name":"mine-now"}`},
		{"DELETE", "/api/runners/" + f.ownersID, ""},
	} {
		if w := send(f.planner, x.method, x.path, x.body); w.Code != http.StatusNotFound {
			t.Errorf("planner %s another's runner: %d %s", x.method, w.Code, w.Body)
		}
	}
	// Nor anything else of runners.
	for _, path := range []string{"/api/runners/" + f.patsID + "/links", "/api/runners/" + f.ownersID + "/repos"} {
		if w := send(f.planner, "GET", path, ""); w.Code != http.StatusForbidden {
			t.Errorf("planner GET %s: %d %s", path, w.Code, w.Body)
		}
	}

	// Their own: renamed, not tuned.
	if w := send(f.planner, "PATCH", "/api/runners/"+f.patsID, `{"labels":["gpu"]}`); w.Code != http.StatusBadRequest {
		t.Errorf("planner labels their runner: %d %s", w.Code, w.Body)
	}
	if w := send(f.planner, "PATCH", "/api/runners/"+f.patsID, `{"name":"owners-box"}`); w.Code != http.StatusConflict {
		t.Errorf("rename to a taken name: %d %s", w.Code, w.Body)
	}
	if w := send(f.planner, "PATCH", "/api/runners/"+f.patsID, `{"name":"Pat Air"}`); w.Code != http.StatusBadRequest {
		t.Errorf("rename to a bad name: %d %s", w.Code, w.Body)
	}
	if w := send(f.planner, "PATCH", "/api/runners/"+f.patsID, `{"name":"pat-air"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"pat-air"`) {
		t.Fatalf("rename: %d %s", w.Code, w.Body)
	}
	if r, _ := f.rh.Get(f.patsID); r.Name != "pat-air" || !r.Online {
		t.Errorf("after the rename: %+v", r)
	}
	// An admin can't make it take the org's work either.
	if w := send(f.owner, "PATCH", "/api/runners/"+f.patsID, `{"shared":true}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "planner") {
		t.Errorf("sharing a planner's runner: %d %s", w.Code, w.Body)
	}

	if w := send(f.planner, "DELETE", "/api/runners/"+f.patsID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
	if got := runnerNames(t, send(f.planner, "GET", "/api/runners", "")); len(got) != 0 {
		t.Errorf("after removing it: %+v", got)
	}
}

// With their runner online, a planner has the general chat, on it, and
// nothing else of the chats.
func TestPlannerChatsOnTheirRunner(t *testing.T) {
	parallel(t)
	f := newPlannerFleet(t)

	kinds, texts := chatTurn(t, f.plannerWS, nil)
	if kinds[0] != "runner" || texts[0] != f.plannerRunnerName || kinds[len(kinds)-1] != "done" {
		t.Fatalf("planner's chat: %v %q", kinds, texts)
	}
	eventually(t, "the hub to save the chat", func() bool {
		return len(raw(f.hub).Chat(testOwner(t, f.hub).Org, f.plannerID)) == 2
	})
	var info struct{ Runners []struct{ Name string } }
	if w := send(f.planner, "GET", "/api/claude", ""); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &info) != nil || len(info.Runners) != 1 {
		t.Fatalf("planner's /api/claude: %d %s", w.Code, w.Body)
	}
	// Another's runner isn't theirs to chat on.
	if kinds, texts := chatTurn(t, f.plannerWS, map[string]any{"runner": f.ownersID}); kinds[len(kinds)-1] != "error" || !strings.Contains(texts[len(texts)-1], "isn't yours") {
		t.Errorf("chat on the owner's runner: %v %q", kinds, texts)
	}
	// Project chats are members'.
	if kinds, texts := chatTurn(t, f.plannerWS, map[string]any{"project": f.project.ID}); kinds[len(kinds)-1] != "error" || !strings.Contains(texts[len(texts)-1], "general chat only") {
		t.Errorf("planner's project chat: %v %q", kinds, texts)
	}
}

// A planner's chat's Dash tools are Planning's: list, read, create,
// update, comment on and start cards. A member's are unchanged.
func TestPlannerChatHasPlanningToolsOnly(t *testing.T) {
	parallel(t)
	f := newPlannerFleet(t)
	org := testOwner(t, f.hub).Org
	tools := func(sc mcptools.Scope) []string {
		t.Helper()
		r := httptest.NewRequest("POST", "/mcp/dash", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		w := httptest.NewRecorder()
		f.hub.boardHandler().Serve(w, r, sc)
		var out struct {
			Result struct{ Tools []struct{ Name string } }
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("tools/list for %+v: %d %s", sc, w.Code, w.Body)
		}
		var names []string
		for _, tl := range out.Result.Tools {
			names = append(names, tl.Name)
		}
		return names
	}
	// The scope the hub gives a chat from pat's runner (runnerScope).
	x, _ := f.rh.Get(f.patsID)
	sc, err := f.hub.runnerScope(x, "", "", "")
	if err != nil || sc.User != f.plannerID {
		t.Fatalf("runner scope: %+v %v", sc, err)
	}
	if got := tools(sc); !slices.Equal(got, mcptools.PlannerToolNames) {
		t.Errorf("planner's tools: %v, want %v", got, mcptools.PlannerToolNames)
	}
	// Not in a project, even if asked.
	if _, err := f.hub.runnerScope(x, "", f.project.ID, ""); err == nil {
		t.Error("a planner's runner got a project's scope")
	}
	w := httptest.NewRecorder()
	f.hub.boardHandler().Serve(w, httptest.NewRequest("POST", "/mcp/dash", strings.NewReader(`{}`)), mcptools.Scope{Org: org, User: f.plannerID, Project: f.project.ID})
	if w.Code != http.StatusForbidden {
		t.Errorf("planner's project scope: %d %s", w.Code, w.Body)
	}
	member := tools(mcptools.Scope{Org: org, User: testOwner(t, f.hub).User})
	for _, want := range []string{"create_todo", "list_projects", "list_issues", "link_card", "create_goal"} {
		if !slices.Contains(member, want) {
			t.Errorf("a member's chat lost %s: %v", want, member)
		}
	}

	// They work, as pat: a card made and commented on.
	b, err := f.hub.boardHandler().Board(sc)
	if err != nil {
		t.Fatal(err)
	}
	pb := b.(mcptools.PlannerBoard)
	if _, err := runTool(t, mcptools.PlannerTools(pb), "create_card", `{"title":"Dark mode","project":"web"}`); err != nil {
		t.Fatal(err)
	}
	cards, _ := raw(f.hub).ForOrg(org).ListCards("")
	if len(cards) != 1 || cards[0].CreatedBy != f.plannerID {
		t.Errorf("cards: %+v", cards)
	}
}

// A planner writes a feature brief on their runner: the round runs there,
// with the project's context unless an owner turned that off. A project's
// brief stays members'.
func TestPlannerRunsFeatureBriefsOnTheirRunner(t *testing.T) {
	parallel(t)
	f := newPlannerFleet(t)

	if w := send(f.planner, "POST", "/api/plans", `{"idea":"A new app"}`); w.Code != http.StatusBadRequest {
		t.Errorf("planner's project brief: %d %s", w.Code, w.Body)
	}
	pl := decodePlan(t, send(f.planner, "POST", "/api/plans", `{"kind":"feature","project":"`+f.project.ID+`","idea":"Export the board"}`), http.StatusCreated)
	pl = decodePlan(t, send(f.planner, "POST", "/api/plans/"+pl.ID+"/interview", `{}`), http.StatusOK)
	if len(pl.Rounds) != 1 || len(f.small.inputs) != 1 || !strings.Contains(f.small.input(0), f.project.Name) {
		t.Fatalf("round on pat's runner: %+v, inputs %q", pl, f.small.inputs)
	}
	if w := send(f.planner, "POST", "/api/plans/"+pl.ID+"/feature", `{"start":"goal"}`); w.Code != http.StatusBadRequest {
		t.Errorf("planner starts a goal from a brief: %d %s", w.Code, w.Body)
	}

	// The setting: owners only; everyone reads it.
	if w := send(f.planner, "PUT", "/api/org/planner-settings", `{"project_context":false}`); w.Code != http.StatusForbidden {
		t.Errorf("planner changes the setting: %d %s", w.Code, w.Body)
	}
	if w := send(f.planner, "GET", "/api/org/planner-settings", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"project_context":true`) {
		t.Errorf("planner reads the setting: %d %s", w.Code, w.Body)
	}
	if w := send(f.owner, "PUT", "/api/org/planner-settings", `{"project_context":false}`); w.Code != http.StatusOK {
		t.Fatalf("owner turns it off: %d %s", w.Code, w.Body)
	}
	if w := send(f.planner, "GET", "/api/org/planner-settings", ""); !strings.Contains(w.Body.String(), `"project_context":false`) {
		t.Errorf("after: %s", w.Body)
	}
}

// The setting decides what a planner's round reads of the project: its
// CLAUDE.md, README and files by default; only its name when owners turn
// it off. Members' rounds read them either way.
func TestPlannerFeatureContextSetting(t *testing.T) {
	s, p, _, _ := featureServer(t)
	owner := ownerCaller(t, s)
	pid := member(t, s, owner.Org, "pat", store.RolePlanner)
	pat := s.as(Caller{User: pid, Org: owner.Org, Planner: true})
	ctx := context.Background()

	if pc := pat.featureContext(ctx, p.ID); pc == nil || !strings.Contains(pc.ClaudeMD, "Use Go") || len(pc.Files) == 0 {
		t.Fatalf("planner's context by default: %+v", pc)
	}
	if err := raw(s).Accounts().SetPlannerContext(pid, owner.Org, false); err == nil {
		t.Error("a planner changed the setting")
	}
	if err := raw(s).Accounts().SetPlannerContext(owner.User, owner.Org, false); err != nil {
		t.Fatal(err)
	}
	if pc := pat.featureContext(ctx, p.ID); pc == nil || pc.Project != p.Name || pc.ClaudeMD != "" || pc.Readme != "" || len(pc.Files) != 0 {
		t.Errorf("planner's context turned off: %+v", pc)
	}
	if pc := s.as(owner).featureContext(ctx, p.ID); pc == nil || pc.ClaudeMD == "" {
		t.Errorf("a member's context with the setting off: %+v", pc)
	}
}
