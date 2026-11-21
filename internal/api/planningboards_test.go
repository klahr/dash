package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/peer"
)

func decodeBoards(t *testing.T, w *httptest.ResponseRecorder) []boardView {
	t.Helper()
	var bs []boardView
	if err := json.Unmarshal(w.Body.Bytes(), &bs); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body, err)
	}
	return bs
}

func decodeCards(t *testing.T, w *httptest.ResponseRecorder) []cardView {
	t.Helper()
	var cs []cardView
	if err := json.Unmarshal(w.Body.Bytes(), &cs); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body, err)
	}
	return cs
}

// A planner manages the org's boards on the Planning page: adds, renames,
// reorders and deletes them. Cards go on the board given (the default
// one when none is), list by board, move between boards, depend on cards
// of other boards, and move to the default board when theirs is deleted.
func TestPlanningBoardsAPI(t *testing.T) {
	s, _, _ := boardServer(t)
	h := s.Routes()
	ph, _ := plannerOf(t, s, h)

	bs := decodeBoards(t, do(t, ph, "GET", "/api/planning/boards", ""))
	if len(bs) != 1 || !bs[0].Default || bs[0].Cards != 0 {
		t.Fatalf("an org's boards at first: %+v", bs)
	}
	main := bs[0]
	w := do(t, ph, "POST", "/api/planning/boards", `{"name":"Mobile"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("add a board: %d %s", w.Code, w.Body)
	}
	var mobile boardView
	_ = json.Unmarshal(w.Body.Bytes(), &mobile)
	if w := do(t, ph, "POST", "/api/planning/boards", `{"name":"mobile"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a name in use: %d %s", w.Code, w.Body)
	}
	if w := do(t, ph, "POST", "/api/planning/boards", `{"name":"X","color":"red"}`); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown field: %d %s", w.Code, w.Body)
	}

	a := decodeCard(t, do(t, ph, "POST", "/api/planning/cards", `{"title":"On main"}`))
	b := decodeCard(t, do(t, ph, "POST", "/api/planning/cards", `{"title":"On mobile","board":"`+mobile.ID+`","depends_on":["`+a.ID+`"]}`))
	c := decodeCard(t, do(t, ph, "POST", "/api/planning/cards", `{"title":"By name","board":"MOBILE"}`))
	if a.BoardID != main.ID || b.BoardID != mobile.ID || c.BoardID != mobile.ID || b.BoardName != "Mobile" || c.Position != 1 {
		t.Fatalf("cards' boards: %s %s %s (%s, at %d)", a.BoardID, b.BoardID, c.BoardID, b.BoardName, c.Position)
	}
	if w := do(t, ph, "POST", "/api/planning/cards", `{"title":"Nowhere","board":"nope"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a card on no board: %d %s", w.Code, w.Body)
	}
	// Dependencies cross boards, and name the other card's board.
	if len(b.Deps) != 1 || b.Deps[0].ID != a.ID || b.Deps[0].Board != main.Name {
		t.Errorf("a dependency on another board: %+v", b.Deps)
	}

	titles := func(q string) []string {
		t.Helper()
		var out []string
		for _, c := range decodeCards(t, do(t, ph, "GET", "/api/planning/cards"+q, "")) {
			out = append(out, c.Title)
		}
		return out
	}
	if got := titles(""); !slices.Equal(got, []string{"On main"}) {
		t.Errorf("no board given lists the default one: %v", got)
	}
	if got := titles("?board=" + mobile.ID); !slices.Equal(got, []string{"On mobile", "By name"}) {
		t.Errorf("the mobile board: %v", got)
	}
	if got := titles("?board=all"); len(got) != 3 {
		t.Errorf("every board: %v", got)
	}
	if w := do(t, ph, "GET", "/api/planning/cards?board=nope", ""); w.Code != http.StatusNotFound {
		t.Errorf("no such board: %d", w.Code)
	}

	// Moved to another board from the open card: to the end of its column.
	moved := decodeCard(t, do(t, ph, "PATCH", "/api/planning/cards/"+c.ID, `{"board":"`+main.ID+`"}`))
	if moved.BoardID != main.ID || moved.Position != 1 {
		t.Errorf("moved to the default board: %s at %d", moved.BoardID, moved.Position)
	}

	// Rename, reorder.
	var r boardView
	w = do(t, ph, "PATCH", "/api/planning/boards/"+mobile.ID, `{"name":"Apps","position":0}`)
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || w.Code != http.StatusOK || r.Name != "Apps" || r.Position != 0 || r.Cards != 1 {
		t.Fatalf("rename and move: %d %s", w.Code, w.Body)
	}
	bs = decodeBoards(t, do(t, ph, "GET", "/api/planning/boards", ""))
	if len(bs) != 2 || bs[0].ID != mobile.ID || bs[1].ID != main.ID || bs[1].Cards != 2 {
		t.Fatalf("after the move: %+v", bs)
	}

	// Delete: not the default board; another's cards move to it.
	if w := do(t, ph, "DELETE", "/api/planning/boards/"+main.ID, ""); w.Code != http.StatusBadRequest {
		t.Errorf("deleting the default board: %d %s", w.Code, w.Body)
	}
	if w := do(t, ph, "DELETE", "/api/planning/boards/"+mobile.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if got := titles(""); !slices.Equal(got, []string{"On main", "By name", "On mobile"}) {
		t.Errorf("the default board after the delete: %v", got)
	}
	if bs = decodeBoards(t, do(t, ph, "GET", "/api/planning/boards", "")); len(bs) != 1 || bs[0].Position != 0 {
		t.Errorf("boards after the delete: %+v", bs)
	}
}

// The planning tools take a board: list_cards lists the default board's
// cards without one, create_card puts a card on it, update_card moves a
// card to it. Each card names its board.
func TestPlanningToolsBoards(t *testing.T) {
	s, _, _ := boardServer(t)
	s.Peers = &peer.Hub{Self: "home"}
	h := loggedIn(t, s.Routes())
	if w := do(t, h, "POST", "/api/planning/boards", `{"name":"Growth"}`); w.Code != http.StatusCreated {
		t.Fatalf("add a board: %d %s", w.Code, w.Body)
	}
	tools := mcptools.FleetTools(mcptools.Fleet{API: handlerAPI{h}})

	out, err := runTool(t, tools, "create_card", `{"title":"Referrals","board":"growth"}`)
	if err != nil {
		t.Fatal(err)
	}
	c := out.(mcptools.CardRow)
	if c.Board != "Growth" {
		t.Fatalf("new card on Growth: %+v", c)
	}
	if _, err := runTool(t, tools, "create_card", `{"title":"Plain"}`); err != nil {
		t.Fatal(err)
	}
	list := func(args string) []string {
		t.Helper()
		out, err := runTool(t, tools, "list_cards", args)
		if err != nil {
			t.Fatal(err)
		}
		var titles []string
		for _, r := range out.([]mcptools.CardRow) {
			titles = append(titles, r.Title+"@"+r.Board)
		}
		return titles
	}
	if got := list(`{}`); !slices.Equal(got, []string{"Plain@Main"}) {
		t.Errorf("list_cards without a board: %v", got)
	}
	if got := list(`{"board":"Growth"}`); !slices.Equal(got, []string{"Referrals@Growth"}) {
		t.Errorf("list_cards of Growth: %v", got)
	}
	if got := list(`{"board":"all"}`); len(got) != 2 {
		t.Errorf("list_cards of every board: %v", got)
	}
	if _, err := runTool(t, tools, "list_cards", `{"board":"nope"}`); err == nil {
		t.Error("list_cards of no board")
	}

	if out, err = runTool(t, tools, "update_card", `{"card":"`+c.ID+`","board":"Main"}`); err != nil {
		t.Fatal(err)
	}
	if got := out.(mcptools.CardRow); got.Board != "Main" {
		t.Errorf("moved to Main: %+v", got)
	}
	if _, err := runTool(t, tools, "update_card", `{"card":"`+c.ID+`","board":"nope"}`); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("update_card to no board: %v", err)
	}
	if _, err := runTool(t, tools, "create_card", `{"title":"Everywhere","board":"all"}`); err == nil {
		t.Error("create_card on every board")
	}
}
