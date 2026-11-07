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
