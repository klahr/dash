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
	"github.com/klahr/dash/internal/store"
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
	// Markdown is a Dash scan as a repository file, for "Export to repo".
	Markdown string `json:"markdown,omitempty"`
}

func (sv *service) scanInfo(projectID string, t scan.Template) scanInfo {
	set, has := sv.data.ScanSetting(projectID, t.Name)
	info := scanInfo{Template: t, Schedule: scan.Effective(t, set, has)}
	if t.Origin == scan.SourceDash {
		info.Markdown = t.Markdown()
	}
	if t.ShadowedBy != "" {
		// It doesn't run; the settings under its name are the other's.
		info.Schedule = scan.Schedule{Cron: t.Schedule, Default: t.Schedule}
		info.ToolsApproved = true
		return info
	}
	_, info.ToolsApproved = scan.Tools(t, set, has)
	if !info.ToolsApproved {
		info.ApprovedTools = set.ApprovedTools
	}
	if sv.s.Scheduler != nil {
		if next := sv.s.Scheduler.NextRun(projectID, t); !next.IsZero() {
			info.NextRun = &next
		}
	}
	return info
}

var errScansOff = errors.New("scans are not available on this server")

func (s *Server) listScans(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	templates, shadowed, problems := scan.Templates(sv.data, p)
	out := []scanInfo{}
	builtin := false
	for _, t := range templates {
		builtin = builtin || t.Builtin
		info := sv.scanInfo(p.ID, t)
		if last, ok := sv.data.LastScanRun(p.ID, t.Name, nil); ok {
			info.LastRun = &last
			if last.Status != model.ScanSucceeded {
				if rep, ok := sv.data.LastScanRun(p.ID, t.Name, func(r model.ScanRun) bool { return r.Status == model.ScanSucceeded }); ok {
					info.LastReport = &rep
				}
			}
		}
		if s.Scans != nil {
			info.Running, _ = s.Scans.Running(p.ID, t.Name)
		}
		out = append(out, info)
	}
	for _, t := range shadowed {
		out = append(out, sv.scanInfo(p.ID, t))
	}
	if problems == nil {
		problems = []scan.Problem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scans": out, "problems": problems, "dir": scan.Dir,
		"builtin": builtin,
	})
}

func (s *Server) runScan(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if _, err := sv.data.GetProject(r.PathValue("pid")); err != nil {
		writeErr(w, err)
		return
	}
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
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
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
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	t, ok := scan.FindIn(sv.data, p, r.PathValue("name"))
	if !ok {
		writeErr(w, fmt.Errorf("%w %q", scan.ErrNoScan, r.PathValue("name")))
		return
	}
	trusted := t.Trusted()
	if req.ApproveTools != nil && len(*req.ApproveTools) > 0 && !slices.Equal(*req.ApproveTools, t.Tools) {
		writeErr(w, fmt.Errorf("%w: scan %q asks for other tools by now (%s); reload and approve again", errBadRequest, t.Name, strings.Join(t.Tools, ", ")))
		return
	}
	if req.ApproveSchedule != nil && (trusted || *req.ApproveSchedule == "" || *req.ApproveSchedule != t.Schedule) {
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
				if !trusted && t.Schedule != "" {
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
			if *req.Enabled && !trusted && st.Schedule == "" && st.ApprovedSchedule == "" {
				st.ApprovedSchedule = t.Schedule
			}
		}
		if req.ApproveTools != nil {
			st.ApprovedTools = slices.Clone(*req.ApproveTools)
		}
	}
	// Check the outcome before saving it.
	next, _ := sv.data.ScanSetting(p.ID, t.Name)
	apply(&next)
	if req.Enabled != nil && *req.Enabled && !scan.Effective(t, next, true).Enabled {
		writeErr(w, fmt.Errorf("%w: scan %q has no schedule; set one first", errBadRequest, t.Name))
		return
	}
	if _, err := sv.data.SetScanSetting(p.ID, t.Name, apply); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sv.scanInfo(p.ID, t))
}

// scanRequest is a Dash scan as the editor sends it.
type scanRequest struct {
	Name     string   `json:"name"`
	Title    string   `json:"title"`
	Kind     string   `json:"kind"`
	Scope    string   `json:"scope"`
	Path     string   `json:"path"`
	Model    string   `json:"model"`
	Effort   string   `json:"effort"`
	Schedule string   `json:"schedule"`
	Tools    []string `json:"tools"`
	Prompt   string   `json:"prompt"`
}

// customScan checks a Dash scan from the request.
func customScan(projectID string, req scanRequest) (scan.Template, error) {
	t, err := scan.Custom(model.ScanTemplate{
		ProjectID: projectID, Name: req.Name, Title: req.Title, Kind: req.Kind, Scope: req.Scope,
		Path: req.Path, Model: req.Model, Effort: req.Effort, Schedule: req.Schedule,
		Tools: req.Tools, Prompt: req.Prompt,
	})
	if err != nil {
		return t, errors.Join(errBadRequest, err)
	}
	return t, nil
}

// createScan adds a scan written in Dash. Its name may not be another Dash
// scan's; a built-in or repository scan of that name is replaced by it.
func (s *Server) createScan(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req scanRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	t, err := customScan(p.ID, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := sv.data.SaveScanTemplate(t.Record(p.ID), true); err != nil {
		writeErr(w, err)
		return
	}
	// It replaces a built-in or repository scan of its name: settings made
	// for that one (a schedule, approvals) don't carry over.
	if _, has := sv.data.ScanSetting(p.ID, t.Name); has {
		if _, err := sv.data.SetScanSetting(p.ID, t.Name, func(st *model.ScanSetting) { *st = model.ScanSetting{} }); err != nil {
			writeErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, sv.scanInfo(p.ID, t))
}

// dashScan is the project's Dash scan of the request's name, or an error
// that says why it can't be changed.
func (sv *service) dashScan(r *http.Request) (model.Project, model.ScanTemplate, error) {
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		return p, model.ScanTemplate{}, err
	}
	name := r.PathValue("name")
	rec, err := sv.data.ScanTemplate(p.ID, name)
	if errors.Is(err, store.ErrNotFound) {
		if other, ok := scan.FindIn(sv.data, p, name); ok {
			return p, rec, fmt.Errorf("%w: scan %q is a %s scan; only scans written in Dash can be changed here (duplicate it instead)", errBadRequest, name, other.Origin)
		}
		return p, rec, fmt.Errorf("%w %q", scan.ErrNoScan, name)
	}
	return p, rec, err
}

// editScan replaces a Dash scan's prompt and settings; its name stays.
func (s *Server) editScan(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req scanRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, old, err := sv.dashScan(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if req.Name != "" && req.Name != old.Name {
		writeErr(w, fmt.Errorf("%w: a scan can't be renamed; duplicate it instead", errBadRequest))
		return
	}
	req.Name = old.Name
	t, err := customScan(p.ID, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := sv.data.SaveScanTemplate(t.Record(p.ID), false); err != nil {
		writeErr(w, err)
		return
	}
	if t.Schedule != old.Schedule {
		// A new schedule in the editor replaces one changed in the list.
		if set, has := sv.data.ScanSetting(p.ID, t.Name); has && set.Schedule != "" {
			if _, err := sv.data.SetScanSetting(p.ID, t.Name, func(st *model.ScanSetting) { st.Schedule = "" }); err != nil {
				writeErr(w, err)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, sv.scanInfo(p.ID, t))
}

// deleteScan forgets a Dash scan. Its runs and the issues it filed stay.
func (s *Server) deleteScan(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	p, rec, err := sv.dashScan(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	// A Dash scan is never shadowed, so the settings under its name are its
	// own: a built-in or repository scan it replaced comes back with none
	// (its schedule off).
	if err := sv.data.DeleteScanTemplate(p.ID, rec.Name, false); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listScanRuns(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	pid := r.PathValue("pid")
	if _, err := sv.data.GetProject(pid); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sv.data.ListScanRuns(pid, r.URL.Query().Get("scan")))
}

func (s *Server) getScanRun(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	run, err := sv.data.GetScanRun(r.PathValue("rid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) scanRunLog(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	run, err := sv.data.GetScanRun(r.PathValue("rid"))
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
	writeText(w, string(b))
}

func (s *Server) cancelScanRun(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	run, err := sv.data.GetScanRun(r.PathValue("rid"))
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
