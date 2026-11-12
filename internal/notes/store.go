package notes

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	self   string // this runner's id: the writer of its writes
	db     *sql.DB
	mu     sync.Mutex // one transaction at a time (one connection)
	policy Policy     // under mu
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
	seq         INTEGER NOT NULL,
	permanent   INTEGER NOT NULL DEFAULT 0,
	clear_before INTEGER NOT NULL DEFAULT 0 -- a clear: deletes all updated before (unix ns)
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
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return &Store{self: self, db: db, policy: DefaultPolicy()}, nil
}

// migrate adds the columns a database made before tidying lacks.
func migrate(db *sql.DB) error {
	for _, c := range []string{"permanent", "clear_before"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('entries') WHERE name = ?`, c).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(`ALTER TABLE entries ADD COLUMN ` + c + ` INTEGER NOT NULL DEFAULT 0`); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetPolicy sets how the store tidies itself (zero fields: the defaults).
func (s *Store) SetPolicy(p Policy) {
	s.mu.Lock()
	s.policy = p.orDefaults()
	s.mu.Unlock()
}

// Policy is how the store tidies itself.
func (s *Store) Policy() Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policy
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

const cols = `id, key, body, tags, author_task, created, updated, expires, deleted, clock, writer, seq, permanent, clear_before`

type scanner interface{ Scan(...any) error }

func scanEntry(r scanner) (Entry, error) {
	var e Entry
	var tags string
	var created, updated, expires, before int64
	var deleted, permanent int
	if err := r.Scan(&e.ID, &e.Key, &e.Body, &tags, &e.AuthorTask, &created, &updated, &expires, &deleted, &e.Clock, &e.Writer, &e.Seq, &permanent, &before); err != nil {
		return e, err
	}
	e.Permanent = permanent != 0
	if before != 0 {
		e.Before = time.Unix(0, before).UTC()
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
	tags := []byte("[]")
	if t := cleanTags(e.Tags); len(t) > 0 {
		tags, _ = json.Marshal(t)
	}
	_, err := tx.Exec(`INSERT INTO entries (`+cols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET key=excluded.key, body=excluded.body, tags=excluded.tags,
		author_task=excluded.author_task, created=excluded.created, updated=excluded.updated,
		expires=excluded.expires, deleted=excluded.deleted, clock=excluded.clock,
		writer=excluded.writer, seq=excluded.seq, permanent=excluded.permanent,
		clear_before=excluded.clear_before`,
		e.ID, e.Key, e.Body, string(tags), e.AuthorTask, nanos(e.Created), nanos(e.Updated), nanos(e.Expires),
		e.Deleted, e.Clock, e.Writer, e.Seq, e.Permanent, nanos(e.Before))
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
// d.ID or of the note titled d.Key. It doesn't scan for secrets;
// Service.Put does. Notes it evicted to make room are synced too: a
// catch-up carries them.
func (s *Store) Write(d Draft, now time.Time) (Entry, error) {
	e, _, err := s.Put(d, now)
	return e, err
}

// Put is Write, also returning the tombstones of the notes it evicted to
// keep the project under its caps, to send with it.
func (s *Store) Put(d Draft, now time.Time) (e Entry, evicted []Entry, err error) {
	now = now.UTC()
	err = s.tx(func(tx *sql.Tx) error {
		s.purgeExpired(tx, now)
		live, err := liveEntries(tx, now)
		if err != nil {
			return err
		}
		var old *Entry
		if d.ID != "" {
			i := slices.IndexFunc(live, func(o Entry) bool { return o.ID == d.ID })
			if i < 0 {
				return ErrNotFound
			}
			old = &live[i]
			for _, o := range live {
				if o.ID != d.ID && title(o.Key) == title(d.Key) {
					return ErrTitleTaken
				}
			}
		} else if same := sameTitle(live, d.Key); len(same) > 0 {
			o := survivor(same) // a duplicate tidying hasn't merged yet: write the one that stays
			old = &o
		}
		if old != nil {
			e.ID, e.Created = old.ID, old.Created
		}
		e.Key, e.Body, e.Tags, e.AuthorTask = strings.TrimSpace(d.Key), d.Body, cleanTags(d.Tags), d.AuthorTask
		e.Updated = now
		switch {
		case d.Permanent:
			e.Permanent = true
		case !d.Expires.IsZero():
			if !d.Expires.After(now) {
				return fmt.Errorf("%w: it would expire at once", ErrBadEntry)
			}
			e.Expires = d.Expires.UTC()
		case d.TTL > 0:
			e.Expires = now.Add(d.TTL)
		case old != nil && old.Permanent:
			e.Permanent = true
		default:
			e.Expires = now.Add(s.policy.TTL)
		}
		if e.ID == "" { // check before taking a counter
			e.ID = "x:1"
		}
		e.Clock, e.Writer, e.Seq = 1, s.self, 1
		if err := e.check(); err != nil {
			return err
		}
		// Make room: the project's other live notes and this one must fit.
		others := slices.DeleteFunc(live, func(o Entry) bool { return old != nil && o.ID == old.ID })
		victims, ok := evictions(others, s.policy, 1, e.size())
		if !ok {
			return ErrTooMany
		}
		for _, v := range victims {
			t, err := s.tombstone(tx, v, "", now)
			if err != nil {
				return err
			}
			evicted = append(evicted, t)
		}
		clock, seq, err := s.stamp(tx)
		if err != nil {
			return err
		}
		if old == nil {
			e.ID, e.Created = entryID(s.self, seq), now
		}
		e.Clock, e.Seq = clock, seq
		return put(tx, e)
	})
	if err != nil {
		return Entry{}, nil, err
	}
	return e, evicted, nil
}

// Confirm says note id is still good: a new version of it, as this
// runner's write, that expires ttl from now (0: the project's default TTL),
// or later if it already did. A permanent note stays so. Like any write,
// it reaches every copy and wins last-writer-wins.
func (s *Store) Confirm(id, task string, ttl time.Duration, now time.Time) (Entry, error) {
	now = now.UTC()
	var out Entry
	err := s.tx(func(tx *sql.Tx) error {
		s.purgeExpired(tx, now)
		e, err := get(tx, id)
		if err != nil {
			return err
		}
		if !e.Live(now) {
			return ErrNotFound
		}
		if ttl <= 0 {
			ttl = s.policy.TTL
		}
		if !e.Permanent && !e.Expires.IsZero() {
			e.Expires = laterOf(e.Expires, now.Add(ttl))
		}
		e.Updated = now
		if task != "" {
			e.AuthorTask = task
		}
		if e.Clock, e.Seq, err = s.stamp(tx); err != nil {
			return err
		}
		e.Writer = s.self
		out = e
		return put(tx, e)
	})
	return out, err
}

func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
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
		out, err = s.tombstone(tx, old, task, now)
		return err
	})
	return out, err
}

// tombstone deletes old as this runner's write.
func (s *Store) tombstone(tx *sql.Tx, old Entry, task string, now time.Time) (Entry, error) {
	clock, seq, err := s.stamp(tx)
	if err != nil {
		return Entry{}, err
	}
	t := Entry{ID: old.ID, Key: old.Key, AuthorTask: task, Created: old.Created, Updated: now, Deleted: true,
		Clock: clock, Writer: s.self, Seq: seq}
	return t, put(tx, t)
}

// Clear deletes every note of the project last updated before before, on
// every copy, as one write: a clear, kept like a tombstone for the
// retention window. Notes written later stay.
func (s *Store) Clear(before time.Time, task string, now time.Time) (Entry, error) {
	now = now.UTC()
	if before.IsZero() || before.After(now) {
		before = now
	}
	var out Entry
	err := s.tx(func(tx *sql.Tx) error {
		clock, seq, err := s.stamp(tx)
		if err != nil {
			return err
		}
		out = Entry{ID: entryID(s.self, seq), AuthorTask: task, Created: now, Updated: now, Deleted: true,
			Before: before.UTC(), Clock: clock, Writer: s.self, Seq: seq}
		if err := put(tx, out); err != nil {
			return err
		}
		return clearBefore(tx, out.Before)
	})
	return out, err
}

// clearBefore applies a clear: it remembers the latest one for good, and
// forgets the notes (and tombstones) it covers. A version of them that
// arrives later is dropped (apply).
func clearBefore(tx *sql.Tx, before time.Time) error {
	cleared, err := meta(tx, "cleared")
	if err != nil {
		return err
	}
	if b := uint64(before.UnixNano()); b > cleared {
		if err := setMeta(tx, "cleared", b); err != nil {
			return err
		}
		cleared = b
	}
	_, err = tx.Exec(`DELETE FROM entries WHERE clear_before = 0 AND updated < ?`, int64(cleared))
	return err
}

// liveEntries returns the live entries, by id.
func liveEntries(tx *sql.Tx, now time.Time) ([]Entry, error) {
	rows, err := tx.Query(`SELECT `+cols+` FROM entries WHERE deleted = 0 AND (expires = 0 OR expires > ?) ORDER BY id`, now.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// purgeExpired forgets expired entries. Every copy hides them at the same
// time, so they need no tombstone; a copy that hasn't purged yet doesn't
// send them.
func (s *Store) purgeExpired(tx *sql.Tx, now time.Time) int {
	r, err := tx.Exec(`DELETE FROM entries WHERE expires != 0 AND expires <= ?`, now.UnixNano())
	if err != nil {
		return 0
	}
	n, _ := r.RowsAffected()
	return int(n)
}

// apply merges e into tx's copy: it wins if it is a later version. It
// reports whether it did.
func (s *Store) apply(tx *sql.Tx, e Entry) (bool, error) {
	if err := e.check(); err != nil {
		return false, nil // a peer's bad entry: skip it, keep the rest
	}
	cleared, err := meta(tx, "cleared")
	if err != nil {
		return false, err
	}
	if cleared != 0 && !e.isClear() && nanos(e.Updated) < int64(cleared) {
		return false, nil // a clear deleted it
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
	if err := put(tx, e); err != nil {
		return false, err
	}
	if e.isClear() {
		return true, clearBefore(tx, e.Before)
	}
	return true, nil
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
	err = s.tx(func(tx *sql.Tx) (err error) {
		applied, err = s.merge(tx, entries, peer)
		return
	})
	return
}

func (s *Store) merge(tx *sql.Tx, entries []Entry, peer Vector) (applied int, err error) {
	for _, e := range entries {
		ok, err := s.apply(tx, e)
		if err != nil {
			return applied, err
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
			return applied, err
		}
		if w == s.self { // we wrote more than we remember: don't reuse counters
			if seq, err := meta(tx, "seq"); err != nil {
				return applied, err
			} else if n > seq {
				if err := setMeta(tx, "seq", n); err != nil {
					return applied, err
				}
			}
		}
	}
	return applied, nil
}

// replace discards this copy's notes and takes peers' copies instead (a
// full resync): what a runner that was away longer than the retention
// window does, so the notes it still holds and the others purged don't
// come back. Its own counters and clock stay, so it never reuses them.
func (s *Store) replace(copies []answer, now time.Time) (applied int, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM entries; DELETE FROM vector`); err != nil {
			return err
		}
		for _, c := range copies {
			n, err := s.merge(tx, c.Entries, c.Vector)
			if err != nil {
				return err
			}
			applied += n
		}
		s.purgeExpired(tx, now)
		return setMeta(tx, "synced", uint64(now.UnixNano()))
	})
	return
}

// MarkSynced records that this copy caught up with the project's runners
// (or found none other) at now.
func (s *Store) MarkSynced(now time.Time) error {
	return s.tx(func(tx *sql.Tx) error { return setMeta(tx, "synced", uint64(now.UnixNano())) })
}

// markOpened starts the clock of a copy never synced: from now on, it
// becomes stale if it isn't synced within the retention window.
func (s *Store) markOpened(now time.Time) error {
	return s.tx(func(tx *sql.Tx) error {
		v, err := meta(tx, "synced")
		if err != nil || v != 0 {
			return err
		}
		return setMeta(tx, "synced", uint64(now.UnixNano()))
	})
}

// Stale reports whether this copy last synced longer than the retention
// window ago: the other copies may have purged tombstones it never saw,
// so it must not offer its notes to them, nor tidy by writing, but resync
// in full (replace).
func (s *Store) Stale(now time.Time) (stale bool, err error) {
	err = s.tx(func(tx *sql.Tx) (err error) { stale, err = s.stale(tx, now); return })
	return
}

func (s *Store) stale(tx *sql.Tx, now time.Time) (bool, error) {
	v, err := meta(tx, "synced")
	if err != nil || v == 0 {
		return false, err
	}
	return now.Sub(time.Unix(0, int64(v))) > s.policy.Retention, nil
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
