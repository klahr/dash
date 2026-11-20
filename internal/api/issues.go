package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/textutil"
	"github.com/klahr/dash/internal/tunnel"
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
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	pid := r.PathValue("pid")
	if _, err := sv.data.GetProject(pid); err != nil {
		writeErr(w, err)
		return
	}
	// ?type= (bug, vuln, issue) and ?kind= (the kind of scan that filed
	// them, aliases allowed) narrow the list.
	ty := model.IssueType(r.URL.Query().Get("type"))
	kind := model.NormalizeScanKind(r.URL.Query().Get("kind"))
	if kind != "" && !slices.Contains(model.ScanKinds(), kind) {
		writeErr(w, fmt.Errorf("%w: unknown kind %q (%s)", errBadRequest, r.URL.Query().Get("kind"), strings.Join(model.ScanKinds(), ", ")))
		return
	}
	out := []model.Issue{}
	for _, i := range sv.data.ListIssues(pid) {
		if (ty == "" || i.Type == ty) && (kind == "" || i.ScanKind == kind) {
			out = append(out, i)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// addIssue adds one issue (the web UI's quick add needs only a title), or
// updates the one with the same fingerprint.
func (s *Server) addIssue(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var in issueInput
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	is, res, err := sv.data.UpsertIssue(in.issue(r.PathValue("pid")))
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

func (s *Server) updateIssue(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
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
	is, err := sv.data.UpdateIssue(r.PathValue("iid"), func(is *model.Issue) error {
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
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	if _, err := sv.data.DeleteIssue(r.PathValue("iid")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) dismissIssues(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
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
		if is, err := sv.data.GetIssue(id); err != nil || is.ProjectID != pid {
			writeErr(w, fmt.Errorf("issue %q: %w", id, store.ErrNotFound))
			return
		}
		is, err := sv.data.SetIssueStatus(id, model.IssueDismissed, req.Reason)
		if err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, is)
	}
	writeJSON(w, http.StatusOK, out)
}

// issueTasks creates tasks for issues: one each, one for them all
// (combine), or one for each group of similar ones, named for what it
// fixes (group; Claude groups them). They are drafts unless enqueue is set.
func (s *Server) issueTasks(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	var req struct {
		IDs     []string `json:"ids"`
		Combine bool     `json:"combine"`
		Group   bool     `json:"group"`
		Enqueue bool     `json:"enqueue"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	how := issuesEach
	if req.Combine {
		how = issuesCombined
	} else if req.Group {
		how = issuesGrouped
	}
	todos, err := sv.tasksForIssues(r.Context(), r.PathValue("pid"), req.IDs, how, req.Enqueue)
	if len(todos) == 0 && (errors.Is(err, errNoGrouper) || errors.Is(err, errNoClaudeRunner) || errors.Is(err, tunnel.ErrOffline)) {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil && len(todos) == 0 {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, nonNil(todos))
}

// issuesHow is how tasksForIssues splits issues into tasks.
type issuesHow int

const (
	issuesEach     issuesHow = iota // a task for each
	issuesCombined                  // one task for them all
	issuesGrouped                   // a task for each group of similar ones
)

// maxGroupIssues is how many issues may be grouped at once.
const maxGroupIssues = 100

// tasksForIssues creates the tasks issueTasks describes and links each issue
// to its task. It returns the tasks created, with an error if one failed.
func (sv *service) tasksForIssues(ctx context.Context, pid string, ids []string, how issuesHow, enqueue bool) ([]model.Todo, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: no issues given", errBadRequest)
	}
	var issues []model.Issue
	for _, id := range ids {
		is, err := sv.data.GetIssue(strings.TrimSpace(id))
		if err != nil || is.ProjectID != pid {
			return nil, fmt.Errorf("issue %q: %w", id, store.ErrNotFound)
		}
		if is.Status != model.IssueOpen && is.Status != model.IssueTaskCreated {
			return nil, fmt.Errorf("%w: issue %q is %s; reopen it first", errBadRequest, is.Title, is.Status)
		}
		issues = append(issues, is)
	}
	var groups []issueBatch
	switch {
	case how == issuesCombined:
		groups = []issueBatch{{issues: issues}}
	case how == issuesGrouped && len(issues) > 1:
		if len(issues) > maxGroupIssues {
			return nil, fmt.Errorf("%w: group at most %d issues at once", errBadRequest, maxGroupIssues)
		}
		gs, err := sv.group(ctx, issueListing(issues))
		if err != nil {
			return nil, err
		}
		groups = fitGroups(gs, issues)
	default:
		for _, is := range issues {
			groups = append(groups, issueBatch{issues: []model.Issue{is}})
		}
	}
	var out []model.Todo
	for _, g := range groups {
		title, prompt := model.TaskFromIssues(g.issues, sv.issueReporter)
		if g.title != "" {
			title = g.title
		}
		t, err := sv.data.CreateTodo(model.Todo{ProjectID: pid, CreatedBy: sv.c.author(), Title: title, Prompt: prompt})
		if err != nil {
			return out, err
		}
		gids := make([]string, len(g.issues))
		for i, is := range g.issues {
			gids[i] = is.ID
		}
		if _, err := sv.data.LinkIssues(gids, t.ID); err != nil {
			_, _ = sv.data.DeleteTodo(t.ID)
			return out, err
		}
		if enqueue {
			if t, err = sv.enqueue(t.ID); err != nil {
				return append(out, t), err
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// issueReporter names an issue's source for a task prompt: a task that
// reported it by its id and title, rather than "agent:<id>".
func (sv *service) issueReporter(source string) string {
	switch {
	case source == "agent:chat":
		return "the chat"
	case strings.HasPrefix(source, "agent:"):
		id := strings.TrimPrefix(source, "agent:")
		if t, err := sv.data.GetTodo(id); err == nil && t.Title != "" {
			return fmt.Sprintf("task %s, %q", id, t.Title)
		}
		return "task " + id
	}
	return ""
}

// An issueBatch is issues one task fixes, with that task's title ("": made
// from the issues).
type issueBatch struct {
	title  string
	issues []model.Issue
}

// maxListedDetails is how much of an issue's details issueListing gives,
// in runes.
const maxListedDetails = 300

// issueListing numbers issues for Claude to group (see agent.Titler.Group):
// what each is and where, and the start of its details.
func issueListing(issues []model.Issue) string {
	var b strings.Builder
	for n, is := range issues {
		fmt.Fprintf(&b, "%d. [%s %s] %s", n+1, is.Severity, is.Type, textutil.OneLine(is.Title, 200))
		var about []string
		if loc := is.Location(); loc != "" {
			about = append(about, "at "+loc)
		}
		if is.Rule != "" {
			about = append(about, "rule "+is.Rule)
		}
		if is.ScanKind != "" {
			about = append(about, "from a "+is.ScanKind+" scan")
		}
		if len(about) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(about, ", "))
		}
		b.WriteByte('\n')
		if d := textutil.OneLine(is.Details, maxListedDetails); d != "" {
			fmt.Fprintf(&b, "   %s\n", d)
		}
	}
	return b.String()
}

// fitGroups turns Claude's groups of issues into batches: an issue named
// twice stays in the first group that names it, numbers past the end are
// dropped, and an issue no group names gets a task of its own. A group of
// one keeps the issue's own title. Batches go most severe first.
func fitGroups(gs []agent.IssueGroup, issues []model.Issue) []issueBatch {
	placed := make([]bool, len(issues))
	var out []issueBatch
	for _, g := range gs {
		var b issueBatch
		for _, i := range g.Issues {
			if i >= 0 && i < len(issues) && !placed[i] {
				placed[i] = true
				b.issues = append(b.issues, issues[i])
			}
		}
		if len(b.issues) > 1 {
			b.title = strings.TrimSpace(g.Title)
		}
		if len(b.issues) > 0 {
			out = append(out, b)
		}
	}
	for i, is := range issues {
		if !placed[i] {
			out = append(out, issueBatch{issues: []model.Issue{is}})
		}
	}
	worst := func(b issueBatch) int {
		r := 99
		for _, is := range b.issues {
			r = min(r, is.Severity.Rank())
		}
		return r
	}
	slices.SortStableFunc(out, func(a, b issueBatch) int { return worst(a) - worst(b) })
	return out
}
