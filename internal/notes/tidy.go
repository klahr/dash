package notes

import (
	"cmp"
	"database/sql"
	"slices"
	"strings"
	"time"
)

// Tidying keeps a project's notes from growing without bound. Every rule
// is a function of synced data only (update times, expiries, titles, ids;
// never what a runner read or when), so each runner tidies its own copy
// alone, without the hub, and the copies still converge:
//
//   - Expired notes are purged at once: every copy hides them at the
//     same moment, so they need no tombstone.
//   - Past the project's caps (Policy.MaxEntries, Policy.MaxBytes), the
//     notes updated longest ago (then by id) are evicted, permanent ones
//     never. An eviction is a tombstone written like a delete: if two
//     runners evict the same note, last-writer-wins keeps one tombstone.
//   - One note per title: if runners created the same title concurrently,
//     the oldest note (created first, then smallest id) stays and takes the
//     newest content of the group (by update time, then version); the
//     others become tombstones. The one that stays is the same whatever
//     version of the group a runner has, so two runners merging at once
//     never delete both. It stays permanent if any was, else expires at
//     the latest of their expiries.
//   - Tombstones and clears older than Policy.Retention are purged.
//   - The database is compacted (VACUUM) about once a day.
//
// A copy that hasn't synced within the retention window (Store.Stale) only
// purges: its view may be missing deletes the others already purged, so
// the writes it would make could be wrong. It resyncs in full first.

// vacuumEvery is how often tidying compacts a database.
const vacuumEvery = 24 * time.Hour

// TidyReport is what one tidying did.
type TidyReport struct {
	Expired    int  // expired notes purged
	Purged     int  // tombstones and clears purged past the retention window
	Evicted    int  // notes evicted for the caps
	Merged     int  // notes merged into another of the same title
	Vacuumed   bool // the database was compacted
	Stale      bool // the copy waits for a full resync: it only purged
	Writes     []Entry
	LiveNotes  int
	LiveBytes  int
	Permanents int
}

// Tidy tidies this copy at now. Its writes (evictions, merges) are in the
// report, to send to the other runners.
func (s *Store) Tidy(now time.Time) (TidyReport, error) {
	now = now.UTC()
	var r TidyReport
	err := s.tx(func(tx *sql.Tx) error {
		r.Expired = s.purgeExpired(tx, now)
		res, err := tx.Exec(`DELETE FROM entries WHERE deleted != 0 AND updated < ?`, now.Add(-s.policy.Retention).UnixNano())
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		r.Purged = int(n)
		if r.Stale, err = s.stale(tx, now); err != nil || r.Stale {
			return err
		}
		merged, err := s.dedup(tx, now)
		if err != nil {
			return err
		}
		r.Writes = merged
		for _, w := range merged {
			if w.Deleted {
				r.Merged++
			}
		}
		live, err := liveEntries(tx, now)
		if err != nil {
			return err
		}
		victims, _ := evictions(live, s.policy, 0, 0)
		for _, v := range victims {
			t, err := s.tombstone(tx, v, "", now)
			if err != nil {
				return err
			}
			r.Writes = append(r.Writes, t)
			r.Evicted++
		}
		if live, err = liveEntries(tx, now); err != nil {
			return err
		}
		for _, e := range live {
			r.LiveNotes++
			r.LiveBytes += e.size()
			if e.Permanent {
				r.Permanents++
			}
		}
		return nil
	})
	if err != nil {
		return r, err
	}
	r.Vacuumed, err = s.vacuum(now)
	return r, err
}

// Dedup merges notes of the same title (as Tidy does), and returns its
// writes: after taking peers' writes, so a duplicate shows only briefly.
func (s *Store) Dedup(now time.Time) (writes []Entry, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		if stale, err := s.stale(tx, now); err != nil || stale {
			return err
		}
		writes, err = s.dedup(tx, now.UTC())
		return err
	})
	return
}

func (s *Store) dedup(tx *sql.Tx, now time.Time) ([]Entry, error) {
	live, err := liveEntries(tx, now)
	if err != nil {
		return nil, err
	}
	groups := map[string][]Entry{}
	for _, e := range live {
		groups[title(e.Key)] = append(groups[title(e.Key)], e)
	}
	names := make([]string, 0, len(groups))
	for t, g := range groups {
		if len(g) > 1 {
			names = append(names, t)
		}
	}
	slices.Sort(names)
	var writes []Entry
	for _, t := range names {
		g := groups[t]
		keep := survivor(g)
		merged := mergeGroup(keep, g)
		if !sameContent(merged, keep) {
			clock, seq, err := s.stamp(tx)
			if err != nil {
				return nil, err
			}
			merged.Clock, merged.Writer, merged.Seq = clock, s.self, seq
			if err := put(tx, merged); err != nil {
				return nil, err
			}
			writes = append(writes, merged)
		}
		for _, o := range g {
			if o.ID == keep.ID {
				continue
			}
			ts, err := s.tombstone(tx, o, "", now)
			if err != nil {
				return nil, err
			}
			writes = append(writes, ts)
		}
	}
	return writes, nil
}

// sameTitle returns the entries of live titled key.
func sameTitle(live []Entry, key string) []Entry {
	var out []Entry
	for _, e := range live {
		if title(e.Key) == title(key) {
			out = append(out, e)
		}
	}
	return out
}

// survivor is the note of a same-title group that stays: the one created
// first, then by id. It doesn't depend on the versions a copy has.
func survivor(g []Entry) Entry {
	return slices.MinFunc(g, func(a, b Entry) int {
		if c := a.Created.Compare(b.Created); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// mergeGroup is keep, with the newest content of its group g.
func mergeGroup(keep Entry, g []Entry) Entry {
	newest := slices.MaxFunc(g, func(a, b Entry) int {
		if c := a.Updated.Compare(b.Updated); c != 0 {
			return c
		}
		if a.wins(b) {
			return 1
		}
		if b.wins(a) {
			return -1
		}
		return strings.Compare(a.ID, b.ID)
	})
	m := keep
	m.Key, m.Body, m.Tags, m.AuthorTask, m.Updated = newest.Key, newest.Body, newest.Tags, newest.AuthorTask, newest.Updated
	m.Permanent, m.Expires = false, time.Time{}
	never := false
	for _, e := range g {
		switch {
		case e.Permanent:
			m.Permanent = true
		case e.Expires.IsZero():
			never = true
		case e.Expires.After(m.Expires):
			m.Expires = e.Expires
		}
	}
	if m.Permanent || never {
		m.Expires = time.Time{}
	}
	return m
}

func sameContent(a, b Entry) bool {
	return a.Key == b.Key && a.Body == b.Body && slices.Equal(a.Tags, b.Tags) && a.AuthorTask == b.AuthorTask &&
		a.Updated.Equal(b.Updated) && a.Expires.Equal(b.Expires) && a.Permanent == b.Permanent
}

// evictions picks the notes of live to evict so that they, plus addCount
// notes of addBytes more, fit p's caps: updated longest ago first (then by
// id), never permanent ones. ok is false if they can't fit even so.
func evictions(live []Entry, p Policy, addCount, addBytes int) (victims []Entry, ok bool) {
	count, size := addCount, addBytes
	for _, e := range live {
		count++
		size += e.size()
	}
	if count <= p.MaxEntries && size <= p.MaxBytes {
		return nil, true
	}
	order := slices.Clone(live)
	slices.SortFunc(order, func(a, b Entry) int {
		if c := a.Updated.Compare(b.Updated); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	for _, e := range order {
		if count <= p.MaxEntries && size <= p.MaxBytes {
			break
		}
		if e.Permanent {
			continue
		}
		victims = append(victims, e)
		count--
		size -= e.size()
	}
	return victims, count <= p.MaxEntries && size <= p.MaxBytes
}

// vacuum compacts the database if it wasn't for vacuumEvery.
func (s *Store) vacuum(now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var last uint64
	if err := s.db.QueryRow(`SELECT v FROM meta WHERE k = 'vacuumed'`).Scan(&last); err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if last != 0 && now.Sub(time.Unix(0, int64(last))) < vacuumEvery {
		return false, nil
	}
	if _, err := s.db.Exec(`VACUUM`); err != nil {
		return false, err
	}
	_, err := s.db.Exec(`INSERT INTO meta (k, v) VALUES ('vacuumed', ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, uint64(now.UnixNano()))
	return err == nil, err
}
