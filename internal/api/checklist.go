package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/klahr/dash/internal/checklist"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/tunnel"
)

// ---------------------------------------------------------------- checklist
// The checklist is the project's TODO.md at the repository root, shown as a
// task list (internal/checklist parses and edits it). The REST endpoints and
// the board's MCP tools share the service methods below.
//
// It is read like the file browser reads files: from the default branch as
// committed, or from the checkout when git doesn't track it there. A change
// to a committed TODO.md is committed straight onto the default branch,
// without a task or an agent run (queue.MergeQueue.CommitFile), so pushing
// and syncing work as for merges. Projects that land work through pull
// requests never get commits straight on their default branch: a change
// there queues a task that commits it, as an edit in the file browser does.
//
// On a hub, the file lives in a runner's checkout: reading and saving are
// passed on to one of the project's runners, while the task of a move is
// made on the hub's board.

func checklistConflict(err error) error {
	if errors.Is(err, checklist.ErrStale) || errors.Is(err, queue.ErrFileChanged) {
		return fmt.Errorf("%w: %w", errConflict, err)
	}
	return err
}

// afterMoveTask, when set (tests), runs between a move's task and the
// item's removal.
var afterMoveTask func()

var errStaleVersion = fmt.Errorf("%w: %w", errConflict, checklist.ErrStale)

// readChecklist reads p's TODO.md.
func (sv *service) readChecklist(ctx context.Context, p model.Project) (mcptools.Checklist, error) {
	if sv.s.hub() {
		var out mcptools.Checklist
		err := sv.callRunner(ctx, p, "GET", "/checklist", nil, &out)
		return out, err
	}
	s := sv.s
	c := mcptools.Checklist{Path: checklist.Name, Branch: p.Base(), Via: "commit", Sections: []checklist.Section{}}
	if p.UsesPR() {
		c.Via = "task"
	}
	f, found, err := s.branchFile(ctx, p, checklist.Name)
	if err != nil {
		return c, err
	}
	if !found {
		if local, err := s.Git.LocalFiles(ctx, p.ID, p.Path); err == nil && slices.Contains(local, checklist.Name) {
			if f, err = s.localFile(ctx, p, checklist.Name); err != nil {
				return c, err
			}
			found, c.Local, c.Via = true, true, "local"
		}
	}
	if !found {
		return c, nil
	}
	if f.Content == nil {
		return c, fmt.Errorf("%w: %s is too large or not text", errBadRequest, checklist.Name)
	}
	c.Exists, c.Version, c.Content = true, f.Blob, *f.Content
	c.Sections = checklist.Parse(c.Content).Sections
	return c, nil
}

// callRunner calls a project endpoint ("/checklist", …) on one of p's runners,
// from a hub.
func (sv *service) callRunner(ctx context.Context, p model.Project, method, rest string, in, out any) error {
	run, rpid, err := sv.projectRunner(ctx, p, "")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(tunnel.WithUser(ctx, sv.c.User), 2*time.Minute)
	defer cancel()
	err = sv.s.fleet().Call(ctx, run.ID, method, "/api/projects/"+url.PathEscape(rpid)+rest, in, out)
	if errors.Is(err, tunnel.ErrOffline) || errors.Is(err, tunnel.ErrUnknownName) {
		return errNoRunnerOnline
	}
	var ce *tunnel.CallError
	if errors.As(err, &ce) && ce.Status == http.StatusConflict {
		return fmt.Errorf("%w: %w", errConflict, err)
	}
	return err
}

// changeChecklist makes ch to p's TODO.md and saves it.
func (sv *service) changeChecklist(ctx context.Context, p model.Project, ch mcptools.ChecklistChange) (mcptools.ChecklistSaved, error) {
	if err := sv.writable(); err != nil {
		return mcptools.ChecklistSaved{}, err
	}
	if len(ch.Ops) == 0 {
		return mcptools.ChecklistSaved{}, fmt.Errorf("%w: no changes", errBadRequest)
	}
	if sv.s.hub() {
		if p.UsesPR() {
			// Its task would have to carry the edit to a runner, which tasks can't.
			return mcptools.ChecklistSaved{}, fmt.Errorf("%w: %s lands changes through pull requests, and its %s can't be changed from the hub; ask for the change in a task instead",
				errBadRequest, p.Name, checklist.Name)
		}
		var out mcptools.ChecklistSaved
		err := sv.callRunner(ctx, p, "POST", "/checklist", ch, &out)
		return out, err
	}
	cur, err := sv.readChecklist(ctx, p)
	if err != nil {
		return mcptools.ChecklistSaved{}, err
	}
	if ch.Version != nil && *ch.Version != cur.Version {
		return mcptools.ChecklistSaved{}, errStaleVersion
	}
	res, err := checklist.Apply(cur.Content, ch.Ops)
	if err != nil {
		if errors.Is(err, checklist.ErrStale) {
			return mcptools.ChecklistSaved{}, checklistConflict(err)
		}
		return mcptools.ChecklistSaved{}, fmt.Errorf("%w: %w", errBadRequest, err)
	}
	msg := ch.Message
	if msg == "" {
		msg = res.Message()
	}
	out := mcptools.ChecklistSaved{Refs: res.Refs}
	switch {
	case cur.Local:
		if _, err := sv.s.writeLocalFile(ctx, p, checklist.Name, cur.Version, res.Content); err != nil {
			return out, err
		}
	case p.UsesPR() || sv.s.Merges == nil:
		// Pull request mode: nothing is committed straight onto the default
		// branch, so a task commits it and opens the pull request.
		t, err := sv.queueEdit(ctx, p, checklist.Name, cur.Version, res.Content, checklist.Short(firstLine(msg), 120))
		if err != nil {
			return out, err
		}
		out.Task = &t
		out.Note = fmt.Sprintf("%s lands changes through pull requests, so task %s commits this change; %s shows it once that is merged.", p.Name, t.ID, checklist.Name)
		// The file on the branch is unchanged until then.
		out.Checklist = cur
		out.Refs = map[string]string{}
		return out, nil
	default:
		sha, _, err := sv.s.Merges.CommitFile(ctx, p, checklist.Name, cur.Version, res.Content, msg)
		if errors.Is(err, queue.ErrUsesPR) {
			return out, fmt.Errorf("%w: %w", errBadRequest, err)
		}
		if err != nil {
			return out, checklistConflict(err)
		}
		out.Commit = sha
	}
	if p, err = sv.data.GetProject(p.ID); err != nil {
		return out, err
	}
	if out.Checklist, err = sv.readChecklist(ctx, p); err != nil {
		return out, err
	}
	return out, nil
}

// moveChecklistItem creates a task from an item, then removes the item from
// TODO.md. The task is made first: should removing the item fail (TODO.md
// changed meanwhile), the task stays and the answer says the item is still
// there.
func (sv *service) moveChecklistItem(ctx context.Context, p model.Project, m mcptools.ChecklistMove) (mcptools.ChecklistMoved, error) {
	if err := sv.writable(); err != nil {
		return mcptools.ChecklistMoved{}, err
	}
	cur, err := sv.readChecklist(ctx, p)
	if err != nil {
		return mcptools.ChecklistMoved{}, err
	}
	if m.Version != nil && *m.Version != cur.Version {
		return mcptools.ChecklistMoved{}, errStaleVersion
	}
	it, err := checklist.Parse(cur.Content).Item(m.Ref)
	if errors.Is(err, checklist.ErrStale) {
		return mcptools.ChecklistMoved{}, checklistConflict(err)
	}
	if err != nil {
		return mcptools.ChecklistMoved{}, fmt.Errorf("%w: %w", errBadRequest, err)
	}
	t, err := sv.addTodo(model.Todo{
		ProjectID: p.ID, Title: checklist.Short(it.Text, 200), Prompt: checklist.Prompt(cur.Content, *it),
		Priority: m.Priority, DependsOn: m.DependsOn,
	}, m.Queue)
	if err != nil {
		return mcptools.ChecklistMoved{}, err
	}
	out := mcptools.ChecklistMoved{Task: t}
	if afterMoveTask != nil {
		afterMoveTask()
	}
	saved, err := sv.changeChecklist(ctx, p, mcptools.ChecklistChange{
		Version: &cur.Version,
		Ops:     []checklist.Op{{Op: "delete", Ref: m.Ref}},
		Message: fmt.Sprintf("%s: move %s to task %s", checklist.Name, checklist.Short(it.Text, 60), t.ID),
	})
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	out.Removed, out.Saved = true, &saved
	return out, nil
}

// ---- REST ----

func (s *Server) checklistProject(w http.ResponseWriter, r *http.Request) (*service, model.Project, bool) {
	sv, ok := s.service(w, r)
	if !ok {
		return nil, model.Project{}, false
	}
	p, err := sv.data.GetProject(r.PathValue("pid"))
	if err != nil {
		writeErr(w, err)
		return nil, model.Project{}, false
	}
	return sv, p, true
}

// getChecklist handles GET /api/projects/{pid}/checklist.
func (s *Server) getChecklist(w http.ResponseWriter, r *http.Request) {
	sv, p, ok := s.checklistProject(w, r)
	if !ok {
		return
	}
	c, err := sv.readChecklist(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// postChecklist handles POST /api/projects/{pid}/checklist: a batch of
// changes (mcptools.ChecklistChange), saved as one commit.
func (s *Server) postChecklist(w http.ResponseWriter, r *http.Request) {
	sv, p, ok := s.checklistProject(w, r)
	if !ok {
		return
	}
	var req mcptools.ChecklistChange
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := sv.changeChecklist(r.Context(), p, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// moveChecklist handles POST /api/projects/{pid}/checklist/move.
func (s *Server) moveChecklist(w http.ResponseWriter, r *http.Request) {
	sv, p, ok := s.checklistProject(w, r)
	if !ok {
		return
	}
	var req mcptools.ChecklistMove
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := sv.moveChecklistItem(r.Context(), p, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// ---- the board's MCP tools ----

func (b board) ReadChecklist(projectID string) (mcptools.Checklist, error) {
	p, err := b.sv.data.GetProject(projectID)
	if err != nil {
		return mcptools.Checklist{}, fmt.Errorf("no project %q (see list_projects)", projectID)
	}
	return b.sv.readChecklist(context.Background(), p)
}

func (b board) ChangeChecklist(projectID string, c mcptools.ChecklistChange) (mcptools.ChecklistSaved, error) {
	p, err := b.sv.data.GetProject(projectID)
	if err != nil {
		return mcptools.ChecklistSaved{}, fmt.Errorf("no project %q (see list_projects)", projectID)
	}
	return b.sv.changeChecklist(context.Background(), p, c)
}

func (b board) MoveChecklistItem(projectID string, m mcptools.ChecklistMove) (mcptools.ChecklistMoved, error) {
	p, err := b.sv.data.GetProject(projectID)
	if err != nil {
		return mcptools.ChecklistMoved{}, fmt.Errorf("no project %q (see list_projects)", projectID)
	}
	return b.sv.moveChecklistItem(context.Background(), p, m)
}
