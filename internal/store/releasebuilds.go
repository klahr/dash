package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ---- Release builds ----
//
// Each release tag of a project and its build (model.ReleaseBuild), read
// from the database when asked for: the CI watcher keeps the one it follows
// in memory.

// KeepReleaseBuilds is how many release tags' builds are kept per project;
// the oldest go when a newer one is saved.
const KeepReleaseBuilds = 200

// PutReleaseBuild saves the build of b's tag, replacing what was saved for
// it. A project that doesn't exist (anymore) saves nothing.
func (s *Store) PutReleaseBuild(b model.ReleaseBuild) error {
	if b.ProjectID == "" || b.Tag == "" {
		return errors.New("a release build needs a project and a tag")
	}
	if b.Runs == nil {
		b.Runs = []model.CIRun{}
	}
	if b.Updated.IsZero() {
		b.Updated = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Projects[b.ProjectID]; !ok {
		return ErrNotFound
	}
	return s.writeLocked(func(tx *sql.Tx) error {
		if err := put(tx, "INSERT INTO release_builds (project_id, tag, org_id, state, pushed, data) VALUES (?, ?, "+projectOrg+", ?, ?, ?)"+
			" ON CONFLICT (project_id, tag) DO UPDATE SET state = excluded.state, pushed = excluded.pushed, data = excluded.data",
			b, b.ProjectID, b.Tag, b.ProjectID, b.State, b.Pushed.UnixMilli()); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM release_builds WHERE project_id = ?1 AND tag NOT IN
			(SELECT tag FROM release_builds WHERE project_id = ?1 ORDER BY pushed DESC LIMIT ?2)`, b.ProjectID, KeepReleaseBuilds)
		return err
	})
}

// ReleaseBuild is the saved build of a project's release tag.
func (s *Store) ReleaseBuild(projectID, tag string) (model.ReleaseBuild, error) {
	bs, err := s.releaseBuilds("SELECT project_id, data FROM release_builds WHERE project_id = ? AND tag = ?", projectID, tag)
	if err != nil {
		return model.ReleaseBuild{}, err
	}
	if len(bs) == 0 {
		return model.ReleaseBuild{}, ErrNotFound
	}
	return bs[0], nil
}

// ReleaseBuilds are a project's latest release builds (at most limit),
// the most recently pushed first.
func (s *Store) ReleaseBuilds(projectID string, limit int) ([]model.ReleaseBuild, error) {
	return s.releaseBuilds("SELECT project_id, data FROM release_builds WHERE project_id = ? ORDER BY pushed DESC, tag DESC LIMIT ?", projectID, limit)
}

func (s *Store) releaseBuilds(query string, args ...any) ([]model.ReleaseBuild, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ReleaseBuild{}
	for rows.Next() {
		var pid, data string
		if err := rows.Scan(&pid, &data); err != nil {
			return nil, err
		}
		var b model.ReleaseBuild
		if err := json.Unmarshal([]byte(data), &b); err != nil {
			continue
		}
		b.ProjectID = pid
		if b.Runs == nil {
			b.Runs = []model.CIRun{}
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
