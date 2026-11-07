package api

import (
	"encoding/json"
	"net/http"
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
