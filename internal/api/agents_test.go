package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/gitops"
)

// A project picks the agent its tasks run on from the registered ones;
// the dashboard lists them with what each can do.
func TestProjectAgentSetting(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	h := loggedIn(t, s.Routes())
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"agent":" Claude "}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"agent":"claude"`) {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"agent":"nobody"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "claude") {
		t.Errorf("unknown agent: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"agent":""}`); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"agent"`) {
		t.Errorf("back to the default: %d %s", w.Code, w.Body)
	}

	w := do(t, h, "GET", "/api/agents", "")
	var list []struct {
		Name     string   `json:"name"`
		Title    string   `json:"title"`
		Features []string `json:"features"`
		Default  bool     `json:"default"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) == 0 {
		t.Fatalf("agents: %v %s", err, w.Body)
	}
	if c := list[0]; c.Name != "claude" || c.Title != "Claude Code" || !c.Default || len(c.Features) != 4 {
		t.Errorf("claude: %+v", c)
	}
}
