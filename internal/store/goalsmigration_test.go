package store

import (
	"path/filepath"
	"strconv"
	"testing"
)

// 0021_goals.sql was briefly numbered 0020 beside 0020_claude_status.sql,
// so a database at version 20 may have either table and not the other.
// Migrating either kind leaves it with both.
func TestGoalsMigrationRepairsEitherVersion20(t *testing.T) {
	t.Parallel()
	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	if ms[19].name != "0020_claude_status.sql" || ms[20].name != "0021_goals.sql" {
		t.Fatalf("migrations 20 and 21 are %s and %s", ms[19].name, ms[20].name)
	}
	goals20 := `CREATE TABLE goals (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, org_id INTEGER, data TEXT NOT NULL);
CREATE INDEX goals_project ON goals (project_id);
CREATE INDEX goals_org ON goals (org_id);`
	for name, v20 := range map[string]string{"claude_status": ms[19].sql, "goals": goals20} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "dash.db")
			db, err := openDB(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, m := range ms[:19] {
				if _, err := db.Exec(m.sql); err != nil {
					t.Fatalf("%s: %v", m.name, err)
				}
			}
			if _, err := db.Exec(v20 + "\nPRAGMA user_version = " + strconv.Itoa(20)); err != nil {
				t.Fatal(err)
			}
			if err := migrate(db, nil); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"goals", "claude_status"} {
				var n int
				if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&n); err != nil || n != 1 {
					t.Errorf("table %s: count %d, err %v", table, n, err)
				}
			}
		})
	}
}
