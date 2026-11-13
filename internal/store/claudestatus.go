package store

import (
	"database/sql"
	"errors"
	"time"
)

// ClaudeStatus is the saved Claude status document (internal/claudestatus):
// nil when none was saved yet.
func (s *Store) ClaudeStatus() ([]byte, error) {
	var data string
	err := s.db.QueryRow("SELECT data FROM claude_status WHERE id = 1").Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(data), nil
}

// PutClaudeStatus saves the Claude status document.
func (s *Store) PutClaudeStatus(data []byte) error {
	_, err := s.db.Exec(`INSERT INTO claude_status (id, data, updated_at) VALUES (1, ?, ?)
		ON CONFLICT (id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`, string(data), time.Now().Unix())
	return err
}
