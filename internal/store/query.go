package store

import (
	"database/sql"
	"errors"
	"time"
)

// singletonKey returns the col of table's one row (id = 1), made with gen
// and saved (sealed, secrets.go) the first time. table and col are code
// constants, never user input: they are put into the SQL as they are.
func (s *Store) singletonKey(table, col string, gen func() (string, error)) (string, error) {
	sel := "SELECT " + col + " FROM " + table + " WHERE id = 1"
	ctx := singletonContext(table, col)
	var key string
	err := s.db.QueryRow(sel).Scan(&key)
	if err == nil {
		return s.open(key, ctx)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if key, err = gen(); err != nil {
		return "", err
	}
	sealed, err := s.seal(key, ctx)
	if err != nil {
		return "", err
	}
	// Another process may have won the race: keep whichever is stored.
	if _, err := s.db.Exec("INSERT INTO "+table+" (id, "+col+", created_at) VALUES (1, ?, ?) ON CONFLICT (id) DO NOTHING",
		sealed, time.Now().Unix()); err != nil {
		return "", err
	}
	if err = s.db.QueryRow(sel).Scan(&key); err != nil {
		return "", err
	}
	return s.open(key, ctx)
}

// scanner is a row to scan: *sql.Row or *sql.Rows.
type scanner interface{ Scan(...any) error }

// rowsQuerier runs a query that returns rows: *sql.DB or *sql.Tx.
type rowsQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
}

// queryAll runs query (with args) and returns every row, read by scan, in
// order. It is never nil without an error. The rows are closed when it
// returns, so a transaction can run its next statement.
func queryAll[T any](q rowsQuerier, scan func(scanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// groupBy runs query (with args) and groups the rows, read by scan into a
// key and a value, by key, each group in row order.
func groupBy[K comparable, V any](q rowsQuerier, scan func(scanner) (K, V, error), query string, args ...any) (map[K][]V, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[K][]V{}
	for rows.Next() {
		k, v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out[k] = append(out[k], v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// queryMap runs query (with args) and maps the rows, read by scan into a
// key and a value, by key; a later row with the same key wins.
func queryMap[K comparable, V any](q rowsQuerier, scan func(scanner) (K, V, error), query string, args ...any) (map[K]V, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[K]V{}
	for rows.Next() {
		k, v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// scanOne reads a row of a single column.
func scanOne[T any](sc scanner) (T, error) {
	var v T
	err := sc.Scan(&v)
	return v, err
}

// execer runs a statement: *sql.DB or *sql.Tx.
type execer interface {
	Exec(string, ...any) (sql.Result, error)
}

// execOne runs query (with args) and returns missing if it changed no row.
func execOne(q execer, missing error, query string, args ...any) error {
	res, err := q.Exec(query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return missing
	}
	return nil
}

// execClaim runs query (with args) and reports whether it changed exactly
// one row: a claim that another run didn't take first.
func execClaim(q execer, query string, args ...any) (bool, error) {
	res, err := q.Exec(query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
