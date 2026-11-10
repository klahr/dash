package store

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/klahr/dash/internal/model"

	_ "modernc.org/sqlite" // pure Go: no cgo, one binary
)

// LegacyFile is the JSON file Dash kept its state in before SQLite. Open
// imports it once from the database's directory.
const LegacyFile = "state.json"

//go:embed migrations/*.sql
var migrationFS embed.FS

// Chat kinds in chat_messages.
const (
	chatMain    = "main"
	chatTodo    = "todo"
	chatProject = "project"
)

func openDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// Pragmas in the DSN apply to every connection the pool opens. Immediate
	// transactions take the write lock up front, so a busy database waits
	// (busy_timeout) instead of failing halfway through a transaction.
	dsn := "file:" + path + "?_txlock=immediate" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)" // the account tables use them; the record tables have none
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// The store serializes its writes and reads from memory; one connection
	// is all it needs and keeps SQLite from contending with itself.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func migrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %q: name must start with a version, like 0001_", e.Name())
		}
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := range out {
		if out[i].version != i+1 {
			return nil, fmt.Errorf("migration %q: versions must count up from 1 without gaps", out[i].name)
		}
	}
	return out, nil
}

// migrate brings the schema up to date, one transaction per migration. The
// schema version is SQLite's user_version. Migrations only go forward: an
// older Dash refuses the database afterwards. Before migrating a database
// that has a schema already (not a new one), backup is called with the
// version it has and the one it gets, and an error from it stops the
// migration (optional).
func migrate(db *sql.DB, backup func(from, to int) error) error {
	ms, err := migrations()
	if err != nil {
		return err
	}
	var cur int
	if err := db.QueryRow("PRAGMA user_version").Scan(&cur); err != nil {
		return err
	}
	if cur > len(ms) {
		return fmt.Errorf("database schema is version %d, newer than this Dash knows (%d): "+
			"run the Dash version that last used it (or a newer one), or restore a backup taken before it", cur, len(ms))
	}
	if cur > 0 && cur < len(ms) && backup != nil {
		if err := backup(cur, len(ms)); err != nil {
			return fmt.Errorf("back up the database before migrating it from schema %d to %d: %w", cur, len(ms), err)
		}
	}
	for _, m := range ms[cur:] {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	return nil
}

var dataTables = []string{"projects", "todos", "issues", "scan_runs", "scan_settings", "scan_templates", "chat_messages"}

func dbEmpty(db *sql.DB) (bool, error) {
	for _, t := range dataTables {
		var n int
		if err := db.QueryRow("SELECT EXISTS (SELECT 1 FROM " + t + ")").Scan(&n); err != nil {
			return false, err
		}
		if n != 0 {
			return false, nil
		}
	}
	return true, nil
}

// importLegacy copies a state.json into an empty database in one transaction
// and renames the file to state.json.imported. No file, or a database that
// already has data, is left alone.
func importLegacy(db *sql.DB, legacy string) error {
	b, err := os.ReadFile(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	empty, err := dbEmpty(db)
	if err != nil || !empty {
		return err
	}
	var data snapshot
	if len(b) > 0 {
		if err := json.Unmarshal(b, &data); err != nil {
			return fmt.Errorf("import %s: %w", legacy, err)
		}
	}
	err = inTx(db, func(tx *sql.Tx) error {
		for _, p := range data.Projects {
			if err := putProject(tx, p); err != nil {
				return err
			}
		}
		for _, t := range data.Todos {
			if t.Priority != nil && *t.Priority == 0 {
				t.Priority = nil // 0 meant "unset" before priorities were optional (see 0013)
			}
			if err := putTodo(tx, t); err != nil {
				return err
			}
		}
		for _, is := range data.Issues {
			if err := putIssue(tx, is); err != nil {
				return err
			}
		}
		for _, r := range data.ScanRuns {
			if err := putScanRun(tx, r); err != nil {
				return err
			}
		}
		for k, st := range data.ScanSettings {
			if st.ProjectID == "" || st.Scan == "" { // the key says, if an old file didn't
				st.ProjectID, st.Scan, _ = strings.Cut(k, "/")
			}
			if err := putScanSetting(tx, st); err != nil {
				return err
			}
		}
		for k, t := range data.ScanTemplates {
			if t.ProjectID == "" || t.Name == "" {
				t.ProjectID, t.Name, _ = strings.Cut(k, "/")
			}
			if err := putScanTemplate(tx, t); err != nil {
				return err
			}
		}
		if err := setChat(tx, chatMain, "", 0, data.Chat); err != nil {
			return err
		}
		for id, msgs := range data.TodoChats {
			if err := setChat(tx, chatTodo, id, 0, msgs); err != nil {
				return err
			}
		}
		for id, msgs := range data.ProjectChats {
			if err := setChat(tx, chatProject, id, 0, msgs); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("import %s: %w", legacy, err)
	}
	if err := os.Rename(legacy, legacy+".imported"); err != nil {
		// The data is in; the database isn't empty any more, so it won't be
		// imported twice.
		slog.Warn("imported state.json but could not rename it", "err", err)
	}
	slog.Info("imported state.json into the database", "projects", len(data.Projects), "todos", len(data.Todos))
	return nil
}

// load reads every record into a snapshot.
func load(db *sql.DB) (snapshot, error) {
	data := snapshot{
		Projects:      map[string]*model.Project{},
		Todos:         map[string]*model.Todo{},
		TodoChats:     map[string][]model.ChatMessage{},
		ProjectChats:  map[string][]model.ChatMessage{},
		Issues:        map[string]*model.Issue{},
		ScanRuns:      map[string]*model.ScanRun{},
		ScanSettings:  map[string]*model.ScanSetting{},
		ScanTemplates: map[string]*model.ScanTemplate{},
		MainChats:     map[string][]model.ChatMessage{},
	}
	// The column, not the JSON, says which org a project is in.
	if err := loadRows(db, "SELECT data, COALESCE(org_id, 0) FROM projects", func(p *model.Project, org int64) {
		p.OrgID = org
		data.Projects[p.ID] = p
	}); err != nil {
		return data, err
	}
	if err := loadRows(db, "SELECT data FROM todos", func(t *model.Todo, _ int64) { data.Todos[t.ID] = t }); err != nil {
		return data, err
	}
	if err := loadRows(db, "SELECT data FROM issues", func(is *model.Issue, _ int64) { data.Issues[is.ID] = is }); err != nil {
		return data, err
	}
	if err := loadRows(db, "SELECT data FROM scan_runs", func(r *model.ScanRun, _ int64) { data.ScanRuns[r.ID] = r }); err != nil {
		return data, err
	}
	if err := loadRows(db, "SELECT data FROM scan_settings", func(st *model.ScanSetting, _ int64) {
		data.ScanSettings[scanKey(st.ProjectID, st.Scan)] = st
	}); err != nil {
		return data, err
	}
	if err := loadRows(db, "SELECT data FROM scan_templates", func(t *model.ScanTemplate, _ int64) {
		data.ScanTemplates[scanKey(t.ProjectID, t.Name)] = t
	}); err != nil {
		return data, err
	}
	rows, err := db.Query("SELECT kind, owner_id, data FROM chat_messages ORDER BY kind, owner_id, seq")
	if err != nil {
		return data, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, owner string
		var raw []byte
		if err := rows.Scan(&kind, &owner, &raw); err != nil {
			return data, err
		}
		var m model.ChatMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return data, fmt.Errorf("chat message: %w", err)
		}
		switch kind {
		case chatMain:
			data.MainChats[owner] = append(data.MainChats[owner], m)
		case chatTodo:
			data.TodoChats[owner] = append(data.TodoChats[owner], m)
		case chatProject:
			data.ProjectChats[owner] = append(data.ProjectChats[owner], m)
		}
	}
	return data, rows.Err()
}

// loadRows reads the records query selects as (data[, org_id]).
func loadRows[T any](db *sql.DB, query string, add func(*T, int64)) error {
	rows, err := db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw []byte
		var org int64
		dest := []any{&raw}
		if len(cols) > 1 {
			dest = append(dest, &org)
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		v := new(T)
		if err := json.Unmarshal(raw, v); err != nil {
			return fmt.Errorf("%s: %w", query, err)
		}
		add(v, org)
	}
	return rows.Err()
}

func inTx(db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---- row writers ----

func put(tx *sql.Tx, query string, v any, args ...any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = tx.Exec(query, append(args, string(b))...)
	return err
}

// defaultOrg is the org rows are put in when nothing says otherwise (see
// the instance table): projects created without an org, and rows of
// projects that don't exist (old dangling references).
const defaultOrg = "(SELECT default_org_id FROM instance WHERE id = 1)"

// projectOrg is the org of the project whose id is the query's next
// argument: a project's todos, issues, scans and chats belong to its org.
const projectOrg = "COALESCE((SELECT org_id FROM projects WHERE id = ?), " + defaultOrg + ")"

// nullOrg is org as a column value: NULL (then the default org) for 0.
func nullOrg(org int64) any {
	if org == 0 {
		return nil
	}
	return org
}

// putProject writes a project. Its org is set when the row is created and
// kept on update: a project never moves to another org.
func putProject(tx *sql.Tx, p *model.Project) error {
	return put(tx, "INSERT INTO projects (id, org_id, data) VALUES (?, COALESCE(?, "+defaultOrg+"), ?)"+
		" ON CONFLICT (id) DO UPDATE SET data = excluded.data", p, p.ID, nullOrg(p.OrgID))
}

func putTodo(tx *sql.Tx, t *model.Todo) error {
	return put(tx, "INSERT INTO todos (id, project_id, org_id, data) VALUES (?, ?, "+projectOrg+", ?)"+
		" ON CONFLICT (id) DO UPDATE SET project_id = excluded.project_id, org_id = excluded.org_id, data = excluded.data",
		t, t.ID, t.ProjectID, t.ProjectID)
}

func putIssue(tx *sql.Tx, is *model.Issue) error {
	return put(tx, "INSERT INTO issues (id, project_id, org_id, data) VALUES (?, ?, "+projectOrg+", ?)"+
		" ON CONFLICT (id) DO UPDATE SET project_id = excluded.project_id, org_id = excluded.org_id, data = excluded.data",
		is, is.ID, is.ProjectID, is.ProjectID)
}

func putScanRun(tx *sql.Tx, r *model.ScanRun) error {
	return put(tx, "INSERT INTO scan_runs (id, project_id, scan, org_id, data) VALUES (?, ?, ?, "+projectOrg+", ?)"+
		" ON CONFLICT (id) DO UPDATE SET project_id = excluded.project_id, scan = excluded.scan, org_id = excluded.org_id, data = excluded.data",
		r, r.ID, r.ProjectID, r.Scan, r.ProjectID)
}

func putScanSetting(tx *sql.Tx, st *model.ScanSetting) error {
	return put(tx, "INSERT INTO scan_settings (project_id, scan, org_id, data) VALUES (?, ?, "+projectOrg+", ?)"+
		" ON CONFLICT (project_id, scan) DO UPDATE SET org_id = excluded.org_id, data = excluded.data",
		st, st.ProjectID, st.Scan, st.ProjectID)
}

func putScanTemplate(tx *sql.Tx, t *model.ScanTemplate) error {
	return put(tx, "INSERT INTO scan_templates (project_id, name, org_id, data) VALUES (?, ?, "+projectOrg+", ?)"+
		" ON CONFLICT (project_id, name) DO UPDATE SET org_id = excluded.org_id, data = excluded.data",
		t, t.ProjectID, t.Name, t.ProjectID)
}

// setChat replaces a conversation (nil or empty clears it). org is the org
// it belongs to (0: the default org).
func setChat(tx *sql.Tx, kind, owner string, org int64, msgs []model.ChatMessage) error {
	if _, err := tx.Exec("DELETE FROM chat_messages WHERE kind = ? AND owner_id = ?", kind, owner); err != nil {
		return err
	}
	for i, m := range msgs {
		if err := putChatMessage(tx, kind, owner, org, i, m); err != nil {
			return err
		}
	}
	return nil
}

// updateChat turns a stored conversation, prev, into msgs, writing only what
// differs: the messages both start with stay, the rest of prev is deleted and
// the rest of msgs inserted. A turn that adds a question and its answer to a
// long conversation writes those two rows, not the whole conversation again.
func updateChat(tx *sql.Tx, kind, owner string, org int64, prev, msgs []model.ChatMessage) error {
	n := 0
	for n < len(prev) && n < len(msgs) && sameChatMessage(prev[n], msgs[n]) {
		n++
	}
	if n < len(prev) {
		if _, err := tx.Exec("DELETE FROM chat_messages WHERE kind = ? AND owner_id = ? AND seq >= ?", kind, owner, n); err != nil {
			return err
		}
	}
	for i := n; i < len(msgs); i++ {
		if err := putChatMessage(tx, kind, owner, org, i, msgs[i]); err != nil {
			return err
		}
	}
	return nil
}

// sameChatMessage reports whether a and b would be stored the same.
func sameChatMessage(a, b model.ChatMessage) bool {
	if a.Role != b.Role || a.Content != b.Content || a.CreatedBy != b.CreatedBy || !slices.Equal(a.Attachments, b.Attachments) {
		return false
	}
	if (a.At == nil) != (b.At == nil) || a.At != nil && !a.At.Equal(*b.At) {
		return false
	}
	if (a.Usage == nil) != (b.Usage == nil) || a.Usage != nil && *a.Usage != *b.Usage {
		return false
	}
	return true
}

func putChatMessage(tx *sql.Tx, kind, owner string, org int64, seq int, m model.ChatMessage) error {
	return put(tx, "INSERT INTO chat_messages (kind, owner_id, seq, org_id, data) VALUES (?, ?, ?, COALESCE(?, "+defaultOrg+"), ?)"+
		" ON CONFLICT (kind, owner_id, seq) DO UPDATE SET org_id = excluded.org_id, data = excluded.data", m, kind, owner, seq, nullOrg(org))
}

func deleteRow(tx *sql.Tx, table, id string) error {
	_, err := tx.Exec("DELETE FROM "+table+" WHERE id = ?", id)
	return err
}
