package store

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ---- Deploys ----
//
// Each deploy of a project to one of its environments (model.Deploy). The
// records are in memory without their logs; a log is read from the
// database with its deploy alone (DeployLog).

// KeepDeploys is how many finished deploys of each environment are kept;
// older ones go when a new one is queued. Those that are live stay.
const KeepDeploys = 100

func putDeploy(tx *sql.Tx, d *model.Deploy) error {
	cp := *d
	cp.Log = ""
	return put(tx, "INSERT INTO deploys (id, project_id, environment, org_id, data) VALUES (?, ?, ?, "+projectOrg+", ?)"+
		" ON CONFLICT (id) DO UPDATE SET data = excluded.data", &cp, d.ID, d.ProjectID, d.Environment, d.ProjectID)
}

// CreateDeploy queues a deploy: d's project, environment, ref and trigger
// are the caller's, its id, status and times the store's.
func (s *Store) CreateDeploy(d model.Deploy) (model.Deploy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.data.Projects[d.ProjectID]
	if !ok {
		return d, ErrNotFound
	}
	if _, ok := p.Environment(d.Environment); !ok {
		return d, ErrNotFound
	}
	now := time.Now()
	d.ID, d.CreatedAt, d.Status, d.Phases, d.Log = NewID(), now, "", nil, ""
	d.StartedAt, d.FinishedAt, d.ExitCode = nil, nil, nil
	d.SetStatus(model.DeployQueued, now)
	d = cloneDeploy(d)
	if s.data.Deploys == nil {
		s.data.Deploys = map[string]*model.Deploy{}
	}
	s.data.Deploys[d.ID] = &d
	old := s.oldDeploysLocked(d.ProjectID, d.Environment)
	for _, id := range old {
		delete(s.data.Deploys, id)
	}
	if err := s.writeLocked(func(tx *sql.Tx) error {
		for _, id := range old {
			if err := deleteRow(tx, "deploys", id); err != nil {
				return err
			}
		}
		return putDeploy(tx, &d)
	}); err != nil {
		return d, err
	}
	s.emit(model.EvDeployUpdated, d.ProjectID, "", d)
	return cloneDeploy(d), nil
}

// oldDeploysLocked are the deploys of an environment past the newest
// KeepDeploys finished ones, which go; the live one stays.
func (s *Store) oldDeploysLocked(projectID, env string) []string {
	var done []*model.Deploy
	for _, d := range s.data.Deploys {
		if d.ProjectID == projectID && d.Environment == env && d.Status.Done() && d.Status != model.DeployLive {
			done = append(done, d)
		}
	}
	if len(done) <= KeepDeploys {
		return nil
	}
	slices.SortFunc(done, func(a, b *model.Deploy) int { return b.CreatedAt.Compare(a.CreatedAt) })
	var out []string
	for _, d := range done[KeepDeploys:] {
		out = append(out, d.ID)
	}
	return out
}

// GetDeploy returns a deploy with its log.
func (s *Store) GetDeploy(id string) (model.Deploy, error) {
	s.mu.RLock()
	d, ok := s.data.Deploys[id]
	if !ok {
		s.mu.RUnlock()
		return model.Deploy{}, ErrNotFound
	}
	out := cloneDeploy(*d)
	s.mu.RUnlock()
	err := s.db.QueryRow("SELECT log FROM deploys WHERE id = ?", id).Scan(&out.Log)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return out, err
}

// ListDeploys returns a project's deploys, of one environment ("" for
// all), newest first, without their logs.
func (s *Store) ListDeploys(projectID, env string) []model.Deploy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []model.Deploy{}
	for _, d := range s.data.Deploys {
		if (projectID == "" || d.ProjectID == projectID) && (env == "" || d.Environment == env) {
			out = append(out, cloneDeploy(*d))
		}
	}
	slices.SortFunc(out, func(a, b model.Deploy) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	return out
}

// LiveDeploy is the deploy live in a project's environment, if any.
func (s *Store) LiveDeploy(projectID, env string) (model.Deploy, bool) {
	for _, d := range s.ListDeploys(projectID, env) {
		if d.Status == model.DeployLive {
			return d, true
		}
	}
	return model.Deploy{}, false
}

// UpdateDeploy applies fn to a copy of the deploy (without its log), then
// stores and announces it. fn's error leaves the deploy as it was.
func (s *Store) UpdateDeploy(id string, fn func(*model.Deploy) error) (model.Deploy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data.Deploys[id]
	if !ok {
		return model.Deploy{}, ErrNotFound
	}
	cp := cloneDeploy(*d)
	if err := fn(&cp); err != nil {
		return cloneDeploy(*d), err
	}
	cp.ID, cp.ProjectID, cp.Environment, cp.CreatedAt, cp.Log = d.ID, d.ProjectID, d.Environment, d.CreatedAt, ""
	s.data.Deploys[id] = &cp
	if err := s.writeLocked(func(tx *sql.Tx) error { return putDeploy(tx, &cp) }); err != nil {
		return cp, err
	}
	s.emit(model.EvDeployUpdated, cp.ProjectID, "", cp)
	return cloneDeploy(cp), nil
}

// SetDeployLog saves a deploy's output (the newest MaxDeployLog of it).
func (s *Store) SetDeployLog(id, log string) error {
	if len(log) > model.MaxDeployLog {
		log = log[len(log)-model.MaxDeployLog:]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Deploys[id]; !ok {
		return ErrNotFound
	}
	return s.writeLocked(func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE deploys SET log = ? WHERE id = ?", log, id)
		return err
	})
}

// DeployOrg is the org a deploy belongs to: its project's.
func (s *Store) DeployOrg(id string) (int64, bool) {
	s.mu.RLock()
	d, ok := s.data.Deploys[id]
	s.mu.RUnlock()
	if !ok {
		return 0, false
	}
	return s.ProjectOrg(d.ProjectID)
}

// CardsLinkedTo are the planning cards linked to any of the tasks, each
// once, with their titles.
func (s *Store) CardsLinkedTo(todoIDs []string) ([]model.DeployShipped, error) {
	out := []model.DeployShipped{}
	if len(todoIDs) == 0 {
		return out, nil
	}
	args := []any{model.LinkTask}
	for _, id := range todoIDs {
		args = append(args, id)
	}
	rows, err := s.db.Query("SELECT DISTINCT c.id, c.title FROM planning_links l JOIN planning_cards c ON c.id = l.card_id"+
		" WHERE l.kind = ? AND l.work_id IN (?"+strings.Repeat(", ?", len(todoIDs)-1)+") ORDER BY c.title", args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var c model.DeployShipped
		if err := rows.Scan(&c.ID, &c.Title); err != nil {
			return out, err
		}
		c.Kind = model.LinkCard
		out = append(out, c)
	}
	return out, rows.Err()
}

func cloneDeploy(d model.Deploy) model.Deploy {
	d.Phases = slices.Clone(d.Phases)
	d.Shipped = slices.Clone(d.Shipped)
	if d.ExitCode != nil {
		c := *d.ExitCode
		d.ExitCode = &c
	}
	return d
}

// ---- in one org ----

func (o *OrgStore) ownsDeploy(id string) bool {
	org, ok := o.s.DeployOrg(id)
	return ok && o.org != 0 && org == o.org
}

// ListDeploys is the deploys of one of the org's projects.
func (o *OrgStore) ListDeploys(projectID, env string) []model.Deploy {
	if !o.ownsProject(projectID) {
		return []model.Deploy{}
	}
	return o.s.ListDeploys(projectID, env)
}

func (o *OrgStore) GetDeploy(id string) (model.Deploy, error) {
	if !o.ownsDeploy(id) {
		return model.Deploy{}, ErrNotFound
	}
	return o.s.GetDeploy(id)
}

func (o *OrgStore) LiveDeploy(projectID, env string) (model.Deploy, bool) {
	if !o.ownsProject(projectID) {
		return model.Deploy{}, false
	}
	return o.s.LiveDeploy(projectID, env)
}
