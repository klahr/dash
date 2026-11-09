package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// BackupPrefix and BackupSuffix frame the names of timestamped backups:
// dash-20260131T030000Z.db. PruneBackups removes only files named so.
const (
	BackupPrefix = "dash-"
	BackupSuffix = ".db"
	backupStamp  = "20060102T150405Z"
)

var backupName = regexp.MustCompile(`^dash-\d{8}T\d{6}Z\.db$`)

// Backup writes a consistent copy of the SQLite database at src to dest
// with VACUUM INTO, while Dash keeps running: in WAL mode the copy reads
// one snapshot and doesn't block Dash's writes. dest must not exist. The
// copy is written beside dest under a temporary name, checked and only
// then renamed, so dest is never a partial copy.
func Backup(ctx context.Context, src, dest string) error {
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("no database at %s: %w", src, err)
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	// Not openDB: it would create a missing database and switch journal
	// modes; a backup only reads.
	db, err := sql.Open("sqlite", "file:"+src+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	tmp := dest + ".partial"
	_ = os.Remove(tmp) // left by an interrupted backup
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("back up %s: %w", src, err)
	}
	if err := checkBackup(ctx, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// The database holds password hashes and tokens: only its owner reads it.
	_ = os.Chmod(tmp, 0o600)
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// checkBackup opens a fresh copy and runs SQLite's quick_check on it.
func checkBackup(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var res string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&res); err != nil {
		return fmt.Errorf("check backup: %w", err)
	}
	if res != "ok" {
		return fmt.Errorf("check backup: %s", res)
	}
	return nil
}

// BackupName is the file name of a timestamped backup taken at t.
func BackupName(t time.Time) string {
	return BackupPrefix + t.UTC().Format(backupStamp) + BackupSuffix
}

// BackupTo writes a timestamped backup of src into dir and then keeps only
// the newest keep backups there (keep <= 0 keeps all). It returns the new
// backup's path.
func BackupTo(ctx context.Context, src, dir string, keep int, now time.Time) (string, error) {
	dest := filepath.Join(dir, BackupName(now))
	if err := Backup(ctx, src, dest); err != nil {
		return "", err
	}
	if keep > 0 {
		if _, err := PruneBackups(dir, keep); err != nil {
			return dest, fmt.Errorf("prune old backups: %w", err)
		}
	}
	return dest, nil
}

// PruneBackups removes all but the newest keep timestamped backups in dir
// and returns the removed paths. Other files in dir are left alone.
func PruneBackups(dir string, keep int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && backupName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if keep < 0 {
		keep = 0
	}
	if len(names) <= keep {
		return nil, nil
	}
	sort.Strings(names) // the UTC timestamps sort in time order
	var removed []string
	var errs []error
	for _, n := range names[:len(names)-keep] {
		p := filepath.Join(dir, n)
		if err := os.Remove(p); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, p)
	}
	return removed, errors.Join(errs...)
}

// ScheduleBackups backs up src into dir every interval until ctx ends,
// keeping the newest keep backups. The first one is due an interval after
// the newest backup already in dir (at once if there is none), so
// restarting Dash doesn't add a backup each time and push older ones out.
func ScheduleBackups(ctx context.Context, src, dir string, every time.Duration, keep int) {
	wait := time.Duration(0)
	if last, ok := newestBackup(dir); ok {
		wait = max(0, time.Until(last.Add(every)))
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		dest, err := BackupTo(ctx, src, dir, keep, time.Now())
		switch {
		case dest == "":
			slog.Error("backup failed", "dir", dir, "err", err)
		case err != nil:
			slog.Warn("backup written, but pruning failed", "path", dest, "err", err)
		default:
			slog.Info("backup written", "path", dest)
		}
		t.Reset(every)
	}
}

// newestBackup is the time of the newest timestamped backup in dir.
func newestBackup(dir string) (time.Time, bool) {
	entries, _ := os.ReadDir(dir)
	var newest time.Time
	for _, e := range entries {
		if !backupName.MatchString(e.Name()) {
			continue
		}
		stamp := e.Name()[len(BackupPrefix) : len(e.Name())-len(BackupSuffix)]
		if t, err := time.Parse(backupStamp, stamp); err == nil && t.After(newest) {
			newest = t
		}
	}
	return newest, !newest.IsZero()
}

// PreMigrationBackup is where Open copies the database at path before
// migrating it from schema version from to version to: beside it, as
// dash.db.pre-migration-<from>-to-<to>-<UTC time>. Scheduled backups never
// prune it; delete it once the new version is known to work.
func PreMigrationBackup(path string, from, to int, t time.Time) string {
	return fmt.Sprintf("%s.pre-migration-%d-to-%d-%s", path, from, to, t.UTC().Format(backupStamp))
}
