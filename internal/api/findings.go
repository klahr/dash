package api

import (
	"fmt"
	"net/http"

	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// Reported issues are draft tasks on the Board (model.Todo.Finding): these
// handlers report one, list and undo the dismissed ones, and merge drafts.

// reportIssue handles POST /api/projects/{pid}/issues: it reports an issue,
// which adds a draft task or bumps the task that has its fingerprint
// (store.Store.ReportFinding). It answers {todo, result, reason}: 201 for a
// new draft, else 200. It is what report_issue calls on another computer.
func (s *Server) reportIssue(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var in struct {
		Title       string          `json:"title"`
		Type        model.IssueType `json:"type"`
		Severity    model.Severity  `json:"severity"`
		File        string          `json:"file"`
		Line        int             `json:"line"`
		Details     string          `json:"details"`
		Fix         string          `json:"fix"`
		Rule        string          `json:"rule"`
		Source      string          `json:"source"`
		Fingerprint string          `json:"fingerprint"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	rep, err := sv.reportIssue(model.Issue{
		ProjectID: r.PathValue("pid"), Title: in.Title, Type: in.Type, Severity: in.Severity,
		File: in.File, Line: in.Line, Details: in.Details, Fix: in.Fix, Rule: in.Rule,
		Source: in.Source, Fingerprint: in.Fingerprint,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	code := http.StatusOK
	if rep.Result == model.UpsertNew || rep.Result == model.UpsertReopened {
		code = http.StatusCreated
	}
	writeJSON(w, code, rep)
}

// reportIssue reports an issue for report_issue and POST .../issues.
func (sv *service) reportIssue(is model.Issue) (mcptools.Reported, error) {
	o, err := sv.data.ReportFinding(is)
	if err != nil {
		return mcptools.Reported{}, err
	}
	rep := mcptools.Reported{Todo: o.Todo, Result: o.Result}
	if o.Dismissed != nil {
		rep.Reason = o.Dismissed.Reason
	}
	return rep, nil
}

// listDismissed handles GET /api/projects/{pid}/dismissed: the issues the
// project's users dismissed (they deleted the drafts), latest first.
func (s *Server) listDismissed(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	pid := r.PathValue("pid")
	if _, err := sv.data.GetProject(pid); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sv.data.ListDismissed(pid))
}

// undismiss handles DELETE /api/projects/{pid}/dismissed/{fp}: the issue
// with that fingerprint adds a draft again the next time it is reported.
func (s *Server) undismiss(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	d, err := sv.data.Undismiss(r.PathValue("pid"), r.PathValue("fp"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// mergeTodos handles POST /api/projects/{pid}/todos/merge {"ids": [...]}:
// the drafts are combined into the first (store.Store.MergeTodos), which
// it answers with.
func (s *Server) mergeTodos(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	t, err := sv.combineTodos(r.PathValue("pid"), req.IDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// combineTodos combines drafts ids of project pid into the first, for the
// board's Combine and combine_todos: every one must be in pid; the store
// takes drafts of one project only. Their issues stay linked to it.
func (sv *service) combineTodos(pid string, ids []string) (model.Todo, error) {
	for _, id := range ids {
		if t, err := sv.data.GetTodo(id); err != nil || t.ProjectID != pid {
			return model.Todo{}, fmt.Errorf("task %q: %w", id, store.ErrNotFound)
		}
	}
	return sv.data.MergeTodos(ids)
}
