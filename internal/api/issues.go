package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// issueInput is an issue as clients send it.
type issueInput struct {
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

func (in issueInput) issue(projectID string) model.Issue {
	return model.Issue{
		ProjectID: projectID, Title: in.Title, Type: in.Type, Severity: in.Severity,
		File: in.File, Line: in.Line, Details: in.Details, Fix: in.Fix, Rule: in.Rule,
		Source: in.Source, Fingerprint: in.Fingerprint,
	}
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	if _, err := s.Store.GetProject(pid); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.Store.ListIssues(pid))
}

// addIssue adds one issue (the web UI's quick add needs only a title), or
// updates the one with the same fingerprint.
func (s *Server) addIssue(w http.ResponseWriter, r *http.Request) {
	var in issueInput
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	is, res, err := s.Store.UpsertIssue(in.issue(r.PathValue("pid")))
	if err != nil {
		writeErr(w, err)
		return
	}
	code := http.StatusOK
	if res == model.UpsertNew {
		code = http.StatusCreated
	}
	writeJSON(w, code, map[string]any{"issue": is, "result": res})
}

// addIssues upserts a batch of issues by fingerprint, e.g. a scan's
// findings, and answers how many were new, updated, already known or
// dismissed.
func (s *Server) addIssues(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string       `json:"source"` // e.g. "scan:gosec", for issues without their own
		Issues []issueInput `json:"issues"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	pid := r.PathValue("pid")
	in := make([]model.Issue, len(req.Issues))
	for i, x := range req.Issues {
		in[i] = x.issue(pid)
	}
	counts, err := s.Store.UpsertIssues(pid, req.Source, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, counts)
}

func (s *Server) updateIssue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title         *string            `json:"title"`
		Type          *model.IssueType   `json:"type"`
		Severity      *model.Severity    `json:"severity"`
		File          *string            `json:"file"`
		Line          *int               `json:"line"`
		Details       *string            `json:"details"`
		Fix           *string            `json:"fix"`
		Status        *model.IssueStatus `json:"status"`
		DismissReason *string            `json:"dismiss_reason"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	is, err := s.Store.UpdateIssue(r.PathValue("iid"), func(is *model.Issue) error {
		set(&is.Title, req.Title)
		set(&is.Type, req.Type)
		set(&is.Severity, req.Severity)
		set(&is.File, req.File)
		set(&is.Line, req.Line)
		set(&is.Details, req.Details)
		set(&is.Fix, req.Fix)
		set(&is.Status, req.Status)
		set(&is.DismissReason, req.DismissReason)
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, is)
}

func (s *Server) deleteIssue(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Store.DeleteIssue(r.PathValue("iid")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) dismissIssues(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []string `json:"ids"`
		Reason string   `json:"reason"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	pid := r.PathValue("pid")
	out := []model.Issue{}
	for _, id := range req.IDs {
		if is, err := s.Store.GetIssue(id); err != nil || is.ProjectID != pid {
			writeErr(w, fmt.Errorf("issue %q: %w", id, store.ErrNotFound))
			return
		}
		is, err := s.Store.SetIssueStatus(id, model.IssueDismissed, req.Reason)
		if err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, is)
	}
	writeJSON(w, http.StatusOK, out)
}

// issueTasks creates tasks for issues: one each, or one for them all
// (combine). They are drafts unless enqueue is set.
func (s *Server) issueTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs     []string `json:"ids"`
		Combine bool     `json:"combine"`
		Enqueue bool     `json:"enqueue"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	todos, err := s.tasksForIssues(r.PathValue("pid"), req.IDs, req.Combine, req.Enqueue)
	if err != nil && len(todos) == 0 {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, nonNil(todos))
}

// tasksForIssues creates the tasks issueTasks describes and links each issue
// to its task. It returns the tasks created, with an error if one failed.
func (s *Server) tasksForIssues(pid string, ids []string, combine, enqueue bool) ([]model.Todo, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: no issues given", errBadRequest)
	}
	var issues []model.Issue
	for _, id := range ids {
		is, err := s.Store.GetIssue(strings.TrimSpace(id))
		if err != nil || is.ProjectID != pid {
			return nil, fmt.Errorf("issue %q: %w", id, store.ErrNotFound)
		}
		if is.Status != model.IssueOpen && is.Status != model.IssueTaskCreated {
			return nil, fmt.Errorf("%w: issue %q is %s; reopen it first", errBadRequest, is.Title, is.Status)
		}
		issues = append(issues, is)
	}
	groups := [][]model.Issue{issues}
	if !combine {
		groups = nil
		for _, is := range issues {
			groups = append(groups, []model.Issue{is})
		}
	}
	var out []model.Todo
	for _, g := range groups {
		title, prompt := model.TaskFromIssues(g)
		t, err := s.Store.CreateTodo(model.Todo{ProjectID: pid, Title: title, Prompt: prompt})
		if err != nil {
			return out, err
		}
		gids := make([]string, len(g))
		for i, is := range g {
			gids[i] = is.ID
		}
		if _, err := s.Store.LinkIssues(gids, t.ID); err != nil {
			_, _ = s.Store.DeleteTodo(t.ID)
			return out, err
		}
		if enqueue {
			if t, err = s.enqueue(t.ID); err != nil {
				return append(out, t), err
			}
		}
		out = append(out, t)
	}
	return out, nil
}
