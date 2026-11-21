package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// Tests open many new, empty databases, and migrating each one from scratch
// is most of a short test's time (the pure-Go SQLite is slow under -race). A
// test binary that calls UseMigratedTemplateInTests migrates one database
// once and copies it for every later new database instead.

var (
	useTemplate atomic.Bool

	templateOnce sync.Once
	templateDB   []byte // a migrated, empty database: one complete file
	templateErr  error
)

// UseMigratedTemplateInTests makes Open copy a migrated template into a new
// database instead of running every migration on it. Only new databases
// (nothing at the path yet) are copied; an existing one is still migrated
// for real. It panics outside a test binary (built by go test): production
// always migrates. Call it from TestMain, before any test runs.
func UseMigratedTemplateInTests() {
	if !testing.Testing() {
		panic("store: the migrated template is for tests only")
	}
	useTemplate.Store(true)
}

// copyTemplate writes the migrated template to path, if the template is on
// and path is a new database: no file there, and no WAL left beside it that
// SQLite would replay onto the copy. Anything else is left to the real
// migrations.
func copyTemplate(path string) error {
	if !useTemplate.Load() {
		return nil
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	templateOnce.Do(func() { templateDB, templateErr = buildTemplate() })
	if templateErr != nil {
		return fmt.Errorf("migrated template database: %w", templateErr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil // created meanwhile: open it as it is
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(templateDB); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// buildTemplate migrates a new database in a temporary folder and returns
// a consistent copy of it, made with VACUUM INTO (one file, no WAL).
func buildTemplate() ([]byte, error) {
	dir, err := os.MkdirTemp("", "dash-store-template-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	db, err := openDB(filepath.Join(dir, "migrated.db"))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := migrate(db, nil); err != nil {
		return nil, err
	}
	out := filepath.Join(dir, "template.db")
	if _, err := db.Exec("VACUUM INTO ?", out); err != nil {
		return nil, err
	}
	return os.ReadFile(out)
}
