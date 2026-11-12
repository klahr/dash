package notes

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store is this runner's full copy of one project's shared notes.
type Store struct {
	self string // this runner's id: the writer of its writes
	db   *sql.DB
	mu   sync.Mutex // one transaction at a time (one connection)
}

const schema = `
CREATE TABLE IF NOT EXISTS entries (
	id          TEXT PRIMARY KEY,
	key         TEXT NOT NULL,
	body        TEXT NOT NULL,
	tags        TEXT NOT NULL,      -- JSON array
	author_task TEXT NOT NULL,
	created     INTEGER NOT NULL,   -- unix nanoseconds
	updated     INTEGER NOT NULL,
	expires     INTEGER NOT NULL,   -- 0: never
	deleted     INTEGER NOT NULL,
	clock       INTEGER NOT NULL,
	writer      TEXT NOT NULL,
	seq         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS entries_stamp ON entries(writer, seq);
CREATE TABLE IF NOT EXISTS vector (writer TEXT PRIMARY KEY, seq INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v INTEGER NOT NULL);
`

// OpenStore opens (creating) the notes database at path, written as
// runner self.
func OpenStore(path, self string) (*Store, error) {
	if self == "" {
		return nil, ErrNoRunnerID
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema + keySchema); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return &Store{self: self, db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

const cols = `id, key, body, tags, author_task, created, updated, expires, deleted, clock, writer, seq`

type scanner interface{ Scan(...any) error }

func scanEntry(r scanner) (Entry, error) {
	var e Entry
	var tags string
	var created, updated, expires int64
	var deleted int
	if err := r.Scan(&e.ID, &e.Key, &e.Body, &tags, &e.AuthorTask, &created, &updated, &expires, &deleted, &e.Clock, &e.Writer, &e.Seq); err != nil {
		return e, err
	}
	_ = json.Unmarshal([]byte(tags), &e.Tags)
	e.Created, e.Updated = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	if expires != 0 {
		e.Expires = time.Unix(0, expires).UTC()
	}
	e.Deleted = deleted != 0
	return e, nil
}

func nanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func put(tx *sql.Tx, e Entry) error {
	tags, _ := json.Marshal(cleanTags(e.Tags))
	if e.Tags == nil {
		tags = []byte("[]")
	}
	_, err := tx.Exec(`INSERT INTO entries (`+cols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET key=excluded.key, body=excluded.body, tags=excluded.tags,
		author_task=excluded.author_task, created=excluded.created, updated=excluded.updated,
		expires=excluded.expires, deleted=excluded.deleted, clock=excluded.clock,
		writer=excluded.writer, seq=excluded.seq`,
		e.ID, e.Key, e.Body, string(tags), e.AuthorTask, nanos(e.Created), nanos(e.Updated), nanos(e.Expires),
		e.Deleted, e.Clock, e.Writer, e.Seq)
	return err
}

func get(tx *sql.Tx, id string) (Entry, error) {
	e, err := scanEntry(tx.QueryRow(`SELECT `+cols+` FROM entries WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

func meta(tx *sql.Tx, k string) (uint64, error) {
	var v uint64
	err := tx.QueryRow(`SELECT v FROM meta WHERE k = ?`, k).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, err
}

func setMeta(tx *sql.Tx, k string, v uint64) error {
	_, err := tx.Exec(`INSERT INTO meta (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v)
	return err
}

func vectorOf(tx *sql.Tx) (Vector, error) {
	rows, err := tx.Query(`SELECT writer, seq FROM vector`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	v := Vector{}
	for rows.Next() {
		var w string
		var n uint64
		if err := rows.Scan(&w, &n); err != nil {
			return nil, err
		}
		v[w] = n
	}
	return v, rows.Err()
}

func raise(tx *sql.Tx, writer string, seq uint64) error {
	_, err := tx.Exec(`INSERT INTO vector (writer, seq) VALUES (?, ?)
		ON CONFLICT(writer) DO UPDATE SET seq = max(seq, excluded.seq)`, writer, seq)
	return err
}

// tx runs fn in one transaction.
func (s *Store) tx(fn func(tx *sql.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// stamp takes this runner's next write counter and Lamport clock, raising
// both past anything seen.
func (s *Store) stamp(tx *sql.Tx) (clock, seq uint64, err error) {
	if clock, err = meta(tx, "clock"); err != nil {
		return
	}
	if seq, err = meta(tx, "seq"); err != nil {
		return
	}
	clock, seq = clock+1, seq+1
	if err = setMeta(tx, "clock", clock); err == nil {
		err = setMeta(tx, "seq", seq)
	}
	if err == nil {
		err = raise(tx, s.self, seq)
	}
	return
}

// Write saves d as this runner's write: a new entry, or a new version of
// d.ID. It doesn't scan for secrets; Service.Put does.
func (s *Store) Write(d Draft, now time.Time) (Entry, error) {
	now = now.UTC()
	var out Entry
	err := s.tx(func(tx *sql.Tx) error {
		s.purge(tx, now)
		var e Entry
		if d.ID != "" {
			old, err := get(tx, d.ID)
			if err != nil {
				return err
			}
			if !old.Live(now) {
				return ErrNotFound
			}
			e.ID, e.Created = old.ID, old.Created
		} else {
			n, err := s.live(tx, now)
			if err != nil {
				return err
			}
			if n >= MaxEntries {
				return ErrTooMany
			}
		}
		clock, seq, err := s.stamp(tx)
		if err != nil {
			return err
		}
		if e.ID == "" {
			e.ID, e.Created = entryID(s.self, seq), now
		}
		e.Key, e.Body, e.Tags, e.AuthorTask = strings.TrimSpace(d.Key), d.Body, cleanTags(d.Tags), d.AuthorTask
		e.Updated, e.Clock, e.Writer, e.Seq = now, clock, s.self, seq
		if d.TTL > 0 {
			e.Expires = now.Add(d.TTL)
		}
		if err := e.check(); err != nil {
			return err
		}
		out = e
		return put(tx, e)
	})
	return out, err
}

// Delete leaves a tombstone for id as this runner's write.
func (s *Store) Delete(id, task string, now time.Time) (Entry, error) {
	now = now.UTC()
	var out Entry
	err := s.tx(func(tx *sql.Tx) error {
		old, err := get(tx, id)
		if err != nil {
			return err
		}
		if old.Deleted {
			return ErrNotFound
		}
		clock, seq, err := s.stamp(tx)
		if err != nil {
			return err
		}
		out = Entry{ID: old.ID, Key: old.Key, AuthorTask: task, Created: old.Created, Updated: now, Deleted: true,
			Clock: clock, Writer: s.self, Seq: seq}
		return put(tx, out)
	})
	return out, err
}

func (s *Store) live(tx *sql.Tx, now time.Time) (int, error) {
	var n int
	err := tx.QueryRow(`SELECT count(*) FROM entries WHERE deleted = 0 AND (expires = 0 OR expires > ?)`, now.UnixNano()).Scan(&n)
	return n, err
}

// purge forgets expired entries. Every copy hides them at the same time, so
// they need no tombstone; a copy that hasn't purged yet doesn't send them.
func (s *Store) purge(tx *sql.Tx, now time.Time) {
	_, _ = tx.Exec(`DELETE FROM entries WHERE expires != 0 AND expires <= ?`, now.UnixNano())
}

// apply merges e into tx's copy: it wins if it is a later version. It
// reports whether it did.
func (s *Store) apply(tx *sql.Tx, e Entry) (bool, error) {
	if err := e.check(); err != nil {
		return false, nil // a peer's bad entry: skip it, keep the rest
	}
	old, err := get(tx, e.ID)
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return false, err
	case !e.wins(old):
		return false, nil
	}
	clock, err := meta(tx, "clock")
	if err != nil {
		return false, err
	}
	if e.Clock > clock {
		if err := setMeta(tx, "clock", e.Clock); err != nil {
			return false, err
		}
	}
	if e.Writer == s.self { // our own write we had lost (the database was reset)
		if seq, err := meta(tx, "seq"); err != nil {
			return false, err
		} else if e.Seq > seq {
			if err := setMeta(tx, "seq", e.Seq); err != nil {
				return false, err
			}
		}
	}
	return true, put(tx, e)
}

// Apply merges entries a peer sent as it wrote them. A writer's counter in
// the vector moves only past writes in sequence; gap reports that some
// write came before one we missed, which a catch-up then fills.
func (s *Store) Apply(entries []Entry) (applied int, gap bool, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		v, err := vectorOf(tx)
		if err != nil {
			return err
		}
		slices.SortFunc(entries, func(a, b Entry) int {
			if c := strings.Compare(a.Writer, b.Writer); c != 0 {
				return c
			}
			return cmp.Compare(a.Seq, b.Seq)
		})
		for _, e := range entries {
			ok, err := s.apply(tx, e)
			if err != nil {
				return err
			}
			if ok {
				applied++
			}
			switch {
			case e.Seq == v[e.Writer]+1:
				v[e.Writer] = e.Seq
				if err := raise(tx, e.Writer, e.Seq); err != nil {
					return err
				}
			case e.Seq > v[e.Writer]:
				gap = true
			}
		}
		return nil
	})
	return
}

// Merge applies a peer's answer to a catch-up: the entries we lacked, and
// its vector, taken with them. Our copy then has everything the peer's has,
// so our vector rises to at least the peer's.
func (s *Store) Merge(entries []Entry, peer Vector) (applied int, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		for _, e := range entries {
			ok, err := s.apply(tx, e)
			if err != nil {
				return err
			}
			if ok {
				applied++
			}
		}
		for w, n := range peer {
			if w == "" || len(w) > 100 {
				continue
			}
			if err := raise(tx, w, n); err != nil {
				return err
			}
			if w == s.self { // we wrote more than we remember: don't reuse counters
				if seq, err := meta(tx, "seq"); err != nil {
					return err
				} else if n > seq {
					if err := setMeta(tx, "seq", n); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	return
}

// Since returns the entries whose last write a copy with vector v lacks
// (deleted ones too; expired ones not), and our vector, read together.
func (s *Store) Since(v Vector, now time.Time) (entries []Entry, ours Vector, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT `+cols+` FROM entries WHERE expires = 0 OR expires > ? ORDER BY writer, seq`, now.UnixNano())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanEntry(rows)
			if err != nil {
				return err
			}
			if e.Seq > v[e.Writer] {
				entries = append(entries, e)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		ours, err = vectorOf(tx)
		return err
	})
	return
}

// Vector is this copy's vector.
func (s *Store) Vector() (v Vector, err error) {
	err = s.tx(func(tx *sql.Tx) error { v, err = vectorOf(tx); return err })
	return
}

// Get returns the live entry id.
func (s *Store) Get(id string, now time.Time) (Entry, error) {
	var e Entry
	err := s.tx(func(tx *sql.Tx) (err error) { e, err = get(tx, id); return })
	if err == nil && !e.Live(now) {
		err = ErrNotFound
	}
	return e, err
}

// Query picks entries for List.
type Query struct {
	Tag  string // only entries with this tag
	Text string // only entries whose key or body contains this (any case)
}

// List returns the live entries that match q, most recently updated first.
func (s *Store) List(q Query, now time.Time) ([]Entry, error) {
	var out []Entry
	text := strings.ToLower(strings.TrimSpace(q.Text))
	err := s.tx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT `+cols+` FROM entries WHERE deleted = 0 AND (expires = 0 OR expires > ?) ORDER BY updated DESC, id`, now.UnixNano())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanEntry(rows)
			if err != nil {
				return err
			}
			if q.Tag != "" && !slices.Contains(e.Tags, q.Tag) {
				continue
			}
			if text != "" && !strings.Contains(strings.ToLower(e.Key), text) && !strings.Contains(strings.ToLower(e.Body), text) {
				continue
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// all returns every row, tombstones and expired ones included (tests).
func (s *Store) all() ([]Entry, error) {
	var out []Entry
	err := s.tx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT ` + cols + ` FROM entries ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanEntry(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}
