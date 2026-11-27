package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// Project settings → Auto-approve saves its switch and rules, cleaned, and
// records when they changed; a bad glob is refused.
func TestProjectAutoApproveRules(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	h := loggedIn(t, s.Routes())
	w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"auto_approve":true,"auto_approve_rules":{"max_lines":50,"paths":[" docs/** ","","**/*_test.go"],"review":true}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	var got model.Project
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	r := got.Rules()
	if !got.AutoApprove || r.MaxLines != 50 || strings.Join(r.Paths, ",") != "docs/**,**/*_test.go" || !r.Review {
		t.Fatalf("saved %+v, rules %+v", got.AutoApprove, r)
	}
	if got.AutoApproveChanged == nil {
		t.Error("want the change recorded, so tasks running meanwhile aren't approved on their own")
	}
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID, `{"auto_approve_rules":{"paths":["/etc/**"]}}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "relative") {
		t.Fatalf("an absolute glob: %d %s", w.Code, w.Body)
	}
}
