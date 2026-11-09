package store

import (
	"context"

	"github.com/klahr/dash/internal/model"
)

// SchemaVersion is the database's schema version and the one this Dash
// needs: every migration it knows applied (/healthz). It doesn't take the
// store's lock, so a slow mutation doesn't hold it up, but a database that
// doesn't answer before ctx is done is an error.
func (s *Store) SchemaVersion(ctx context.Context) (have, want int, err error) {
	ms, err := migrations()
	if err != nil {
		return 0, 0, err
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&have); err != nil {
		return 0, len(ms), err
	}
	return have, len(ms), nil
}

// TodoCounts is how many todos there are in each status, in every org
// (/metrics).
func (s *Store) TodoCounts() map[model.TodoStatus]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[model.TodoStatus]int{}
	for _, t := range s.data.Todos {
		out[t.Status]++
	}
	return out
}
