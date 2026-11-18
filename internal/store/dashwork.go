package store

import (
	"database/sql"
	"errors"

	"github.com/klahr/dash/internal/model"
)

// Whose work Dash's own tasks are. A task Dash makes by itself (a fix for
// a failing build, a card imported from the project's tracker) has no
// creator (CreatedBy 0). A runner does only its owner's work unless it takes
// other members' tasks (see tunnel.Runner.WorksFor), so such a task would
// wait for a shared runner. Instead the hub gives it to a member, as its
// RunBy, before it picks a runner: one of their runners takes it, as if
// they had queued it. It still reads as made by Dash.

// BoardSwitched is the project event kind of a move of a project's board
// (tracker.Manager.Apply); its UserID is who moved it.
const BoardSwitched = "board.switched"

// DashWorkFor is the member Dash's own task t is for (0: none found, it
// stays no one's). The first of these who is still a member of the task's
// org, and not only a planner:
//
//  1. for a card from the project's tracker (ExternalRef), who connected
//     the project's board to the tracker;
//  2. for a fix of a failing build (CIRun), who turned on fixing failed
//     builds for the project (Project.CIBy);
//  3. who added the project (Project.AddedBy);
//  4. the org's owner who has been one longest, else its longest admin.
func (s *Store) DashWorkFor(t model.Todo) int64 {
	p, err := s.GetProject(t.ProjectID)
	if err != nil {
		return 0
	}
	org, ok := s.ProjectOrg(p.ID)
	if !ok || org == 0 {
		return 0
	}
	acc := s.Accounts()
	member := func(uid int64) bool {
		if uid <= 0 {
			return false
		}
		m, err := acc.Membership(uid, org)
		return err == nil && !m.Role.Planner()
	}
	var cands []int64
	if t.ExternalRef != nil {
		cands = append(cands, s.boardConnectedBy(p.ID))
	}
	if t.CIRun != 0 {
		cands = append(cands, p.CIBy)
	}
	cands = append(cands, p.AddedBy)
	for _, u := range cands {
		if member(u) {
			return u
		}
	}
	ms, err := acc.Members(org)
	if err != nil {
		return 0
	}
	for _, role := range []Role{RoleOwner, RoleAdmin} {
		var first *Member
		for i, m := range ms {
			if m.Role == role && (first == nil || m.Joined.Before(first.Joined) || m.Joined.Equal(first.Joined) && m.ID < first.ID) {
				first = &ms[i]
			}
		}
		if first != nil {
			return first.ID
		}
	}
	return 0
}

// boardConnectedBy is who last moved project pid's board, when it is on an
// external tracker now (0: unknown, or it is Dash's own board).
func (s *Store) boardConnectedBy(pid string) int64 {
	cfg, err := s.TrackerConfig(pid)
	if err != nil || cfg.Native() {
		return 0
	}
	var uid sql.NullInt64
	err = s.db.QueryRow("SELECT user_id FROM project_events WHERE project_id = ? AND kind = ? ORDER BY id DESC LIMIT 1",
		pid, BoardSwitched).Scan(&uid)
	if err != nil { // sql.ErrNoRows too: nobody is known
		return 0
	}
	return uid.Int64
}

// dashOwnedTask reports whether t is Dash's own work nobody was given yet.
func dashOwnedTask(t model.Todo) bool { return t.CreatedBy == 0 && t.RunBy == 0 }

// GiveDashWork gives queued task id, Dash's own work that nobody started,
// to the member it is for (DashWorkFor), as its RunBy. It returns the task
// as it is then; unchanged when it isn't such a task or no member is found.
func (s *Store) GiveDashWork(id string) (model.Todo, error) {
	t, err := s.GetTodo(id)
	if err != nil || !dashOwnedTask(t) {
		return t, err
	}
	uid := s.DashWorkFor(t)
	if uid == 0 {
		return t, nil
	}
	nt, err := s.UpdateTodo(id, func(x *model.Todo) error {
		if !dashOwnedTask(*x) {
			return errNoChange
		}
		x.RunBy = uid
		return nil
	})
	if errors.Is(err, errNoChange) {
		return s.GetTodo(id)
	}
	return nt, err
}

var errNoChange = errors.New("nothing to change")
