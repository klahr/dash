package store

import (
	"path/filepath"
	"strconv"
	"testing"
)

// 0039_one_board.sql drops the extra boards a project could have: a task
// on one of them is on the project's one board, with nothing else about it
// changed, and the project's list of boards is gone from its row.
func TestOneBoardMigrationMovesTasksToTheBoard(t *testing.T) {
	t.Parallel()
	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	i := -1
	for k, m := range ms {
		if m.name == "0039_one_board.sql" {
			i = k
		}
	}
	if i < 0 {
		t.Fatal("no 0039_one_board.sql")
	}
	path := filepath.Join(t.TempDir(), "dash.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms[:i] {
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
	}
	// Written as an older Dash wrote them: a project with a second board,
	// one task on it and one on the main board.
	for _, q := range []string{
		"PRAGMA user_version = " + strconv.Itoa(i),
		`INSERT INTO projects (id, data) VALUES ('p1', '{"id":"p1","name":"web","boards":[{"id":"b2","name":"Website"}]}')`,
		`INSERT INTO todos (id, project_id, data) VALUES ('t1', 'p1', '{"id":"t1","project_id":"p1","board":"b2","title":"On Website","prompt":"x","status":"queued"}')`,
		`INSERT INTO todos (id, project_id, data) VALUES ('t2', 'p1', '{"id":"t2","project_id":"p1","title":"On Main","prompt":"y","status":"draft"}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if p, err := s.GetProject("p1"); err != nil || p.Name != "web" {
		t.Fatalf("the project: %+v (%v)", p, err)
	}
	todos := s.ListTodos("p1")
	if len(todos) != 2 {
		t.Fatalf("the project's tasks: %+v", todos)
	}
	if t1, err := s.GetTodo("t1"); err != nil || t1.Title != "On Website" || t1.Status != "queued" {
		t.Fatalf("the task from the other board: %+v (%v)", t1, err)
	}
	// The rows themselves no longer have the fields.
	for _, q := range []string{
		"SELECT COUNT(*) FROM projects WHERE json_type(data, '$.boards') IS NOT NULL",
		"SELECT COUNT(*) FROM todos WHERE json_type(data, '$.board') IS NOT NULL",
	} {
		var n int
		if err := s.db.QueryRow(q).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d (%v)", q, n, err)
		}
	}
}
