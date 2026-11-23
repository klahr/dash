package store

import "database/sql"

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
