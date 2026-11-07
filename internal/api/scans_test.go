package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func TestScanEndpoints(t *testing.T) {
	s, _, p := boardServer(t)
	p, _ = s.Store.UpdateProject(p.ID, func(x *model.Project) error { x.Path = t.TempDir(); return nil })
	h := loggedIn(t, s.Routes())

	// A repository without templates gets the built-ins, schedules off.
	w := do(t, h, "GET", "/api/projects/"+p.ID+"/scans", "")
	var list struct {
		Scans []struct {
			Name     string `json:"name"`
			Builtin  bool   `json:"builtin"`
			Schedule struct {
				Cron    string `json:"cron"`
				Enabled bool   `json:"enabled"`
			} `json:"schedule"`
		} `json:"scans"`
		Builtin bool `json:"builtin"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if !list.Builtin || len(list.Scans) != 3 || list.Scans[0].Schedule.Enabled || list.Scans[0].Schedule.Cron == "" {
		t.Fatalf("list: %+v", list)
	}

	// Turn a schedule on, with another expression; a bad one is refused.
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID+"/scans/security", `{"schedule":"every night"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad cron: %d %s", w.Code, w.Body)
	}
	w = do(t, h, "PATCH", "/api/projects/"+p.ID+"/scans/security", `{"enabled":true,"schedule":"15 3 * * *"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
	set, ok := s.Store.ScanSetting(p.ID, "security")
	if !ok || set.Enabled == nil || !*set.Enabled || set.Schedule != "15 3 * * *" {
		t.Fatalf("setting: %+v", set)
	}
	// Clearing the expression of a built-in leaves its default; a scan
	// without any schedule can't be turned on.
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID+"/scans/nope", `{"enabled":true}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown scan: %d", w.Code)
	}
	if w := do(t, h, "PATCH", "/api/projects/"+p.ID+"/scans/security", `{"reset":true}`); w.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", w.Code, w.Body)
	}
	if set, _ := s.Store.ScanSetting(p.ID, "security"); set.Enabled != nil || set.Schedule != "" {
		t.Fatalf("after reset: %+v", set)
	}

	// Without a runner, starting a scan says so.
	if w := do(t, h, "POST", "/api/projects/"+p.ID+"/scans/security/run", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("run without runner: %d", w.Code)
	}
	if w := do(t, h, "GET", "/api/projects/"+p.ID+"/scan-runs", ""); w.Code != http.StatusOK || w.Body.String() != "[]\n" {
		t.Fatalf("runs: %d %s", w.Code, w.Body)
	}
}

func TestScanApprovals(t *testing.T) {
	s, _, p := boardServer(t)
	repo := t.TempDir()
	p, _ = s.Store.UpdateProject(p.ID, func(x *model.Project) error { x.Path = repo; return nil })
	h := loggedIn(t, s.Routes())
	write := func(src string) {
		t.Helper()
		dir := filepath.Join(repo, ".dash", "scans")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "deps.md"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	type info struct {
		Tools         []string `json:"tools"`
		ToolsApproved bool     `json:"tools_approved"`
		Schedule      struct {
			Cron      string `json:"cron"`
			Enabled   bool   `json:"enabled"`
			Requested string `json:"requested"`
		} `json:"schedule"`
	}
	get := func() info {
		t.Helper()
		var list struct{ Scans []info }
		w := do(t, h, "GET", "/api/projects/"+p.ID+"/scans", "")
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Scans) != 1 {
			t.Fatalf("list: %d %s", w.Code, w.Body)
		}
		return list.Scans[0]
	}
	patch := func(body string, code int) {
		t.Helper()
		if w := do(t, h, "PATCH", "/api/projects/"+p.ID+"/scans/deps", body); w.Code != code {
			t.Fatalf("PATCH %s: %d %s", body, w.Code, w.Body)
		}
	}
	write("---\ntools: Read, Bash\nschedule: 0 6 * * *\n---\nAudit.")
	if in := get(); in.ToolsApproved || in.Schedule.Enabled || in.Schedule.Requested != "0 6 * * *" {
		t.Fatalf("before approval: %+v", in)
	}
	// Approving what the user didn't see is refused.
	patch(`{"approve_tools":["Read","Bash","WebFetch"]}`, http.StatusBadRequest)
	patch(`{"approve_schedule":"* * * * *"}`, http.StatusBadRequest)
	patch(`{"approve_tools":["Read","Bash"]}`, http.StatusOK)
	patch(`{"approve_schedule":"0 6 * * *"}`, http.StatusOK)
	if in := get(); !in.ToolsApproved || !in.Schedule.Enabled || in.Schedule.Requested != "" {
		t.Fatalf("after approval: %+v", in)
	}
	// The template changes: tools fall back, the approved schedule stays.
	write("---\ntools: Read, Bash, WebFetch\nschedule: '* * * * *'\n---\nAudit.")
	in := get()
	if in.ToolsApproved || !in.Schedule.Enabled || in.Schedule.Cron != "0 6 * * *" || in.Schedule.Requested != "* * * * *" {
		t.Fatalf("after the template changed: %+v", in)
	}
	// Turning it off and on again doesn't adopt the new schedule.
	patch(`{"enabled":false}`, http.StatusOK)
	patch(`{"enabled":true}`, http.StatusOK)
	if in := get(); in.Schedule.Cron != "0 6 * * *" {
		t.Fatalf("re-enabled: %+v", in)
	}
	// Reset turns it off and forgets the approval.
	patch(`{"reset":true}`, http.StatusOK)
	if in := get(); in.Schedule.Enabled || in.Schedule.Requested != "* * * * *" {
		t.Fatalf("after reset: %+v", in)
	}
}

func TestDashScanEndpoints(t *testing.T) {
	s, _, p := boardServer(t)
	repo := t.TempDir()
	p, _ = s.Store.UpdateProject(p.ID, func(x *model.Project) error { x.Path = repo; return nil })
	h := loggedIn(t, s.Routes())
	base := "/api/projects/" + p.ID + "/scans"

	type info struct {
		Name       string `json:"name"`
		Source     string `json:"source"`
		Prompt     string `json:"prompt"`
		ShadowedBy string `json:"shadowed_by"`
		Markdown   string `json:"markdown"`
		Schedule   struct {
			Cron    string `json:"cron"`
			Enabled bool   `json:"enabled"`
		} `json:"schedule"`
		ToolsApproved bool `json:"tools_approved"`
	}
	list := func() map[string]info {
		t.Helper()
		w := do(t, h, "GET", base, "")
		var l struct{ Scans []info }
		if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		out := map[string]info{}
		for _, i := range l.Scans {
			out[i.Name+"/"+i.Source] = i
		}
		return out
	}

	// Validation, and names of the built-ins are taken.
	for _, bad := range []string{`{"name":"a b","prompt":"x"}`, `{"name":"x","prompt":""}`, `{"name":"x","schedule":"nope","prompt":"x"}`, `{"name":"x","kind":"what","prompt":"x"}`} {
		if w := do(t, h, "POST", base, bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
	if w := do(t, h, "POST", base, `{"name":"security","prompt":"x"}`); w.Code != http.StatusConflict {
		t.Fatalf("built-in name: %d %s", w.Code, w.Body)
	}

	w := do(t, h, "POST", base, `{"name":"mine","title":"Mine","kind":"security","schedule":"0 6 * * *","tools":["Bash(npm audit:*)"],"prompt":"Check {{path}}."}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "POST", base, `{"name":"mine","prompt":"x"}`); w.Code != http.StatusConflict {
		t.Fatalf("duplicate: %d %s", w.Code, w.Body)
	}
	l := list()
	mine, ok := l["mine/dash"]
	if !ok || len(l) != 4 || !mine.Schedule.Enabled || !mine.ToolsApproved || mine.Markdown == "" {
		t.Fatalf("list: %+v", l)
	}

	// Edit: the prompt and settings change; a schedule set in the list is
	// replaced by the editor's.
	if w := do(t, h, "PATCH", base+"/mine", `{"schedule":"0 7 * * *"}`); w.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, "PUT", base+"/mine", `{"name":"other","prompt":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("rename: %d", w.Code)
	}
	if w := do(t, h, "PUT", base+"/mine", `{"kind":"nope","prompt":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad edit: %d", w.Code)
	}
	w = do(t, h, "PUT", base+"/mine", `{"title":"Mine","kind":"code_review","schedule":"0 8 * * *","prompt":"Review {{since}}."}`)
	if w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	if mine = list()["mine/dash"]; mine.Prompt != "Review {{since}}." || mine.Schedule.Cron != "0 8 * * *" {
		t.Fatalf("after edit: %+v", mine)
	}

	// Built-in and repository scans can't be edited or deleted here.
	if w := do(t, h, "PUT", base+"/security", `{"prompt":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("edit built-in: %d", w.Code)
	}
	if w := do(t, h, "DELETE", base+"/security", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("delete built-in: %d", w.Code)
	}
	if w := do(t, h, "DELETE", base+"/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("delete unknown: %d", w.Code)
	}

	// A repository template of the same name shadows it.
	dir := filepath.Join(repo, ".dash", "scans")
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "mine.md"), []byte("Repo."), 0o644); err != nil {
		t.Fatal(err)
	}
	l = list()
	if l["mine/repo"].Prompt != "Repo." || l["mine/dash"].ShadowedBy != ".dash/scans/mine.md" || len(l) != 2 {
		t.Fatalf("shadowed: %+v", l)
	}
	// The shadowed one can still be edited; it stays shadowed.
	w = do(t, h, "PUT", base+"/mine", `{"prompt":"Still mine."}`)
	if w.Code != http.StatusOK || !json.Valid(w.Body.Bytes()) {
		t.Fatalf("edit shadowed: %d %s", w.Code, w.Body)
	}
	var edited info
	_ = json.Unmarshal(w.Body.Bytes(), &edited)
	if edited.ShadowedBy == "" || edited.Prompt != "Still mine." {
		t.Fatalf("edited shadowed: %+v", edited)
	}
	// Deleting it keeps the repository scan's settings.
	if w := do(t, h, "DELETE", base+"/mine", ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if _, has := s.Store.ScanSetting(p.ID, "mine"); !has {
		t.Fatal("repository scan's settings deleted")
	}
	if l = list(); len(l) != 1 || l["mine/repo"].Name == "" {
		t.Fatalf("after delete: %+v", l)
	}
}
