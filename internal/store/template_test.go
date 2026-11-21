package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// A new database copied from the migrated template (TestMain turns it on)
// is the one the migrations make from scratch: same schema, same rows.
func TestTemplateMatchesMigrations(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	copied, err := Open(filepath.Join(dir, "copied.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	migrated, err := openPath(filepath.Join(dir, "migrated.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()

	dump := func(db *sql.DB) map[string]string {
		t.Helper()
		out := map[string]string{}
		var v, n int
		if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
			t.Fatal(err)
		}
		out["user_version"] = itoa(int64(v))
		var mode string
		if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
			t.Fatal(err)
		}
		out["journal_mode"] = mode
		rows, err := db.Query("SELECT type, name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'")
		if err != nil {
			t.Fatal(err)
		}
		var tables []string
		for rows.Next() {
			var typ, name, def string
			if err := rows.Scan(&typ, &name, &def); err != nil {
				t.Fatal(err)
			}
			out[typ+" "+name] = def
			if typ == "table" {
				tables = append(tables, name)
			}
		}
		rows.Close()
		for _, tbl := range tables {
			if err := db.QueryRow(`SELECT count(*) FROM "` + tbl + `"`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			out["rows "+tbl] = itoa(int64(n))
		}
		return out
	}
	got, want := dump(copied.db), dump(migrated.db)
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: template has %q, migrations make %q", k, got[k], w)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: only in the template", k)
		}
	}
}

// An existing database is never replaced by the template, even an empty
// file: it is opened (and migrated) as it is.
func TestTemplateLeavesExistingDatabase(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyTemplate(path); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() != 0 {
		t.Fatalf("the template was copied over an existing file: %v %v", fi, err)
	}
}
