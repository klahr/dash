package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/scan"
)

// scanInfo is a scan as the Scans view shows it.
type scanInfo struct {
	scan.Template
	Schedule scan.Schedule  `json:"schedule"`
	NextRun  *time.Time     `json:"next_run,omitempty"`
	LastRun  *model.ScanRun `json:"last_run,omitempty"`
	// LastReport is the newest run that finished scanning, when the last
	// run did not (it was skipped, failed or is running).
	LastReport *model.ScanRun `json:"last_report,omitempty"`
	Running    string         `json:"running,omitempty"` // id of the run in progress
	// ToolsApproved says the scan runs with the tools its template asks
	// for; false: it asks for tools not approved in Dash and runs read-only.
	ToolsApproved bool `json:"tools_approved"`
	// ApprovedTools is the tool list approved before, when the template
	// asks for others now.
	ApprovedTools []string `json:"approved_tools,omitempty"`
}

func (s *Server) scanInfo(projectID string, t scan.Template) scanInfo {
	set, has := s.Store.ScanSetting(projectID, t.Name)
	info := scanInfo{Template: t, Schedule: scan.Effective(t, set, has)}
	_, info.ToolsApproved = scan.Tools(t, set, has)
	if !info.ToolsApproved {
		info.ApprovedTools = set.ApprovedTools
	}
	if s.Scheduler != nil {
		if next := s.Scheduler.NextRun(projectID, t); !next.IsZero() {
			info.NextRun = &next
		}
	}
	return info
}

var errScansOff = errors.New("scans are not available on this server")

func (s *Server) listScans(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	templates, problems := scan.Load(p.Path)
	out := []scanInfo{}
	for _, t := range templates {
		info := s.scanInfo(p.ID, t)
		if last, ok := s.Store.LastScanRun(p.ID, t.Name, nil); ok {
			info.LastRun = &last
			if last.Status != model.ScanSucceeded {
				if rep, ok := s.Store.LastScanRun(p.ID, t.Name, func(r model.ScanRun) bool { return r.Status == model.ScanSucceeded }); ok {
					info.LastReport = &rep
				}
			}
		}
		if s.Scans != nil {
			info.Running, _ = s.Scans.Running(p.ID, t.Name)
		}
		out = append(out, info)
	}
	if problems == nil {
		problems = []scan.Problem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scans": out, "problems": problems, "dir": scan.Dir,
		"builtin": len(templates) > 0 && templates[0].Builtin,
	})
}

func (s *Server) runScan(w http.ResponseWriter, r *http.Request) {
	if s.Scans == nil {
		writeErr(w, errScansOff)
		return
	}
	run, err := s.Scans.Start(r.PathValue("pid"), r.PathValue("name"), "manual")
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

// updateScan turns a scan's schedule on or off or changes its expression;
// reset goes back to the template's. For a repository template it also
// approves the tools and schedule the template asks for: approve_tools and
// approve_schedule name exactly what the user saw, and are refused if the
// template asks for something else by now. approve_tools [] withdraws the
// approval.
func (s *Server) updateScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled         *bool     `json:"enabled"`
		Schedule        *string   `json:"schedule"`
		Reset           bool      `json:"reset"`
		ApproveTools    *[]string `json:"approve_tools"`
		ApproveSchedule *string   `json:"approve_schedule"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.Store.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	t, ok := scan.Find(p.Path, r.PathValue("name"))
	if !ok {
		writeErr(w, fmt.Errorf("%w %q", scan.ErrNoScan, r.PathValue("name")))
		return
	}
	if req.ApproveTools != nil && len(*req.ApproveTools) > 0 && !slices.Equal(*req.ApproveTools, t.Tools) {
		writeErr(w, fmt.Errorf("%w: scan %q asks for other tools by now (%s); reload and approve again", errBadRequest, t.Name, strings.Join(t.Tools, ", ")))
		return
	}
	if req.ApproveSchedule != nil && (t.Builtin || *req.ApproveSchedule == "" || *req.ApproveSchedule != t.Schedule) {
		writeErr(w, fmt.Errorf("%w: scan %q has another schedule by now (%q); reload and approve again", errBadRequest, t.Name, t.Schedule))
		return
	}
	if req.Schedule != nil {
		*req.Schedule = strings.TrimSpace(*req.Schedule)
		if *req.Schedule != "" {
			if _, err := scan.ParseCron(*req.Schedule); err != nil {
				writeErr(w, errors.Join(errBadRequest, err))
				return
			}
		}
	}
	apply := func(st *model.ScanSetting) {
		if req.Reset {
			st.Enabled, st.Schedule, st.ApprovedSchedule = nil, "", ""
		}
		if req.Schedule != nil {
			st.Schedule = *req.Schedule
			if st.Schedule == t.Schedule {
				// The template's: for a repository template, typing it in
				// approves it.
				st.Schedule = ""
				if !t.Builtin && t.Schedule != "" {
					st.ApprovedSchedule = t.Schedule
				}
			}
		}
		if req.ApproveSchedule != nil {
			st.Schedule, st.ApprovedSchedule = "", *req.ApproveSchedule
			on := true
			st.Enabled = &on
		}
		if req.Enabled != nil {
			st.Enabled = req.Enabled
			// Turning on a repository template's schedule that was never
			// approved approves the one shown, the template's.
			if *req.Enabled && !t.Builtin && st.Schedule == "" && st.ApprovedSchedule == "" {
				st.ApprovedSchedule = t.Schedule
			}
		}
		if req.ApproveTools != nil {
			st.ApprovedTools = slices.Clone(*req.ApproveTools)
		}
	}
	// Check the outcome before saving it.
	next, _ := s.Store.ScanSetting(p.ID, t.Name)
	apply(&next)
	if req.Enabled != nil && *req.Enabled && !scan.Effective(t, next, true).Enabled {
		writeErr(w, fmt.Errorf("%w: scan %q has no schedule; set one first", errBadRequest, t.Name))
		return
	}
	if _, err := s.Store.SetScanSetting(p.ID, t.Name, apply); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.scanInfo(p.ID, t))
}

func (s *Server) listScanRuns(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	if _, err := s.Store.GetProject(pid); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.Store.ListScanRuns(pid, r.URL.Query().Get("scan")))
}

func (s *Server) getScanRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.GetScanRun(r.PathValue("rid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) scanRunLog(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.GetScanRun(r.PathValue("rid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if s.Scans == nil {
		writeErr(w, errScansOff)
		return
	}
	b, err := os.ReadFile(s.Scans.LogPath(run.ID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) cancelScanRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.GetScanRun(r.PathValue("rid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if s.Scans == nil || !s.Scans.Cancel(run.ID) {
		writeErr(w, fmt.Errorf("%w: the run is not running", errBadRequest))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
