package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
)

// The planning tools are served to the chats, dash-mcp and the connector.
var (
	_ mcptools.CardBoard = board{}
	_ mcptools.CardBoard = projectBoard{}
	_ mcptools.CardBoard = mcptools.Fleet{}
	_ mcptools.CardBoard = mcptools.RESTComputer{}
)

// handlerAPI is dash-mcp's REST client, calling h in process.
type handlerAPI struct{ h http.Handler }

func (a handlerAPI) Call(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.h.ServeHTTP(w, r)
	if w.Code >= 400 {
		return fmt.Errorf("%s %s: %d %s", method, path, w.Code, strings.TrimSpace(w.Body.String()))
	}
	if out == nil || w.Code == http.StatusNoContent {
		return nil
	}
	return json.Unmarshal(w.Body.Bytes(), out)
}

// runTool runs the tool named name of tools with args (JSON).
func runTool(t *testing.T, tools []mcptools.Tool, name, args string) (any, error) {
	t.Helper()
	for _, tl := range tools {
		if tl.Name == name {
			var a mcptools.Args
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				t.Fatal(err)
			}
			return tl.Run(a)
		}
	}
	t.Fatalf("no tool %s", name)
	return nil, nil
}

// dash-mcp plans over the REST API: it adds, reads, changes, moves, comments
// on and starts cards, as the user it logged in as.
func TestDashMCPPlanningTools(t *testing.T) {
	s, _, p := boardServer(t)
	s.Peers = &peer.Hub{Self: "home"}
	tools := mcptools.FleetTools(mcptools.Fleet{API: handlerAPI{loggedIn(t, s.Routes())}})
	for _, name := range mcptools.PlanningToolNames {
		if !slices.ContainsFunc(tools, func(tl mcptools.Tool) bool { return tl.Name == name }) {
			t.Errorf("dash-mcp has no %s", name)
		}
	}

	out, err := runTool(t, tools, "create_card", `{"title":"Export as PDF","project":"web","priority":"high","tags":["reports"]}`)
	if err != nil {
		t.Fatal(err)
	}
	c := out.(mcptools.CardRow)
	if c.Column != "ideas" || c.Project != p.Name || c.Priority != "high" || strings.Join(c.Tags, ",") != "reports" {
		t.Fatalf("new card %+v", c)
	}
	if out, err = runTool(t, tools, "update_card", `{"card":"`+c.ID+`","description":"For **reports**.","column":"planned","position":0}`); err != nil {
		t.Fatal(err)
	}
	if got := out.(mcptools.CardRow); got.Column != "planned" {
		t.Errorf("moved card %+v", got)
	}
	if _, err := runTool(t, tools, "update_card", `{"card":"`+c.ID+`"}`); err == nil {
		t.Error("update_card with nothing to change")
	}
	if _, err := runTool(t, tools, "update_card", `{"card":"`+c.ID+`","column":"later"}`); err == nil {
		t.Error("update_card to no column")
	}
	if _, err := runTool(t, tools, "comment_card", `{"card":"`+c.ID+`","text":"Customers ask weekly"}`); err != nil {
		t.Fatal(err)
	}
	if out, err = runTool(t, tools, "start_card", `{"card":"`+c.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	st := out.(mcptools.CardStarted)
	if st.Kind != "task" || st.ID == "" || st.Card.Column != "in_progress" || st.Card.Links != 1 {
		t.Fatalf("started %+v", st)
	}
	td, err := raw(s).GetTodo(st.ID)
	if err != nil || td.ProjectID != p.ID || !strings.Contains(td.Prompt, "For **reports**.") {
		t.Fatalf("the task %+v %v", td, err)
	}

	if out, err = runTool(t, tools, "get_card", `{"card":"`+c.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	d := out.(mcptools.CardDetail)
	if d.Description != "For **reports**." || len(d.Work) != 1 || d.Work[0].ID != st.ID || d.Work[0].Kind != "task" ||
		len(d.Comments) != 1 || d.Comments[0].Text != "Customers ask weekly" || d.Comments[0].Author != "u" {
		t.Errorf("card %+v", d)
	}
	if out, err = runTool(t, tools, "list_cards", `{"project":"web","column":"in_progress"}`); err != nil {
		t.Fatal(err)
	}
	if rows := out.([]mcptools.CardRow); len(rows) != 1 || rows[0].ID != c.ID {
		t.Errorf("list_cards %+v", rows)
	}
	if _, err := runTool(t, tools, "get_card", `{"card":"nope"}`); err == nil {
		t.Error("get_card of no card")
	}
}

// A project's chat reads, changes and starts its project's cards and cards
// of no project, never another project's; it starts work in its own project.
func TestProjectChatPlanningTools(t *testing.T) {
	s, _, p := boardServer(t)
	h := s.Routes()
	other, _, _ := otherProject(t, s)
	o := s.Store.ForOrg(ownerSession(t, s).Org.ID)
	secret, err := o.CreateCard(model.Card{Title: "Secret card", ProjectID: other.ID, Column: model.CardIdeas})
	if err != nil {
		t.Fatal(err)
	}
	loose, err := o.CreateCard(model.Card{Title: "Loose card", Column: model.CardIdeas})
	if err != nil {
		t.Fatal(err)
	}
	token, release := s.Board.Register(chatCaller(t, s), p.ID)
	defer release()

	for _, call := range []struct{ tool, args string }{
		{"get_card", `{"card":"` + secret.ID + `"}`},
		{"update_card", `{"card":"` + secret.ID + `","title":"Planted"}`},
		{"comment_card", `{"card":"` + secret.ID + `","text":"Planted"}`},
		{"start_card", `{"card":"` + secret.ID + `"}`},
		{"start_card", `{"card":"` + loose.ID + `","project":"` + other.Name + `"}`},
	} {
		if text, isErr := toolCall(h, token, call.tool, call.args); !isErr || strings.Contains(text, "Secret") {
			t.Errorf("%s %s: %s", call.tool, call.args, text)
		}
	}
	if c, _ := o.GetCard(secret.ID); c.Title != "Secret card" || c.Column != model.CardIdeas {
		t.Errorf("another project's card changed: %+v", c)
	}

	if text, isErr := toolCall(h, token, "update_card", `{"card":"`+loose.ID+`","target":"1.4","column":"planned"}`); isErr {
		t.Fatalf("update_card of a card of no project: %s", text)
	}
	text, isErr := toolCall(h, token, "start_card", `{"card":"`+loose.ID+`","kind":"goal"}`)
	if isErr {
		t.Fatalf("start_card: %s", text)
	}
	var st mcptools.CardStarted
	if err := json.Unmarshal([]byte(text), &st); err != nil || st.Kind != "goal" || st.ID == "" {
		t.Fatalf("started %s %v", text, err)
	}
	if g, err := raw(s).GetGoal(st.ID); err != nil || g.ProjectID != p.ID {
		t.Errorf("the goal %+v %v", g, err)
	}
	if c, _ := o.GetCard(loose.ID); c.ProjectID != p.ID || c.Column != model.CardInProgress || c.Target != "1.4" {
		t.Errorf("started card %+v", c)
	}
}

// The connector (/mcp) has the planning tools too; a read-only connection
// only reads the plan.
func TestConnectorPlanningTools(t *testing.T) {
	s, _, _ := boardServer(t)
	sv := s.as(ownerCaller(t, s))
	tools := mcptools.FleetTools(mcptools.Fleet{Computers: func() ([]mcptools.Machine, error) {
		return sv.computers(t.Context()), nil
	}})
	out, err := runTool(t, tools, "create_card", `{"title":"Dark mode"}`)
	if err != nil {
		t.Fatal(err)
	}
	id := out.(mcptools.CardRow).ID
	ro := mcptools.ReadOnly(tools, mcptools.FleetReadTools, errReadOnly)
	if _, err := runTool(t, ro, "get_card", `{"card":"`+id+`"}`); err != nil {
		t.Errorf("read-only get_card: %v", err)
	}
	if _, err := runTool(t, ro, "list_cards", `{}`); err != nil {
		t.Errorf("read-only list_cards: %v", err)
	}
	for _, name := range []string{"create_card", "update_card", "comment_card", "start_card", "link_card"} {
		if _, err := runTool(t, ro, name, `{"card":"`+id+`","title":"x","text":"x","todo":"x"}`); err != errReadOnly {
			t.Errorf("read-only %s: %v", name, err)
		}
	}
}
