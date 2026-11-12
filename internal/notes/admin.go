package notes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"
)

// What the hub's UI may ask of a runner's notes, through the hub (not the
// relay: the hub itself asks, over the runner's tunnel). Neither carries a
// note: a Status is counts, a Removal a filter the runner applies to its
// own copy after opening it, answered with how many notes it removed.

// Status is a runner's copy of a project's notes, as counts only: no
// title, body, tag, task or id of any note.
type Status struct {
	Notes   int       `json:"notes"`            // live notes
	Bytes   int       `json:"bytes"`            // their size (titles, bodies, tags)
	Expired int       `json:"expired"`          // expired, not purged yet
	Newest  time.Time `json:"newest,omitzero"`  // the latest write's update time (deletes too)
	Vector  Vector    `json:"vector,omitempty"` // per writer runner, the counter the copy is complete to
	Stale   bool      `json:"stale,omitempty"`  // waits for a full resync
	// The project key this runner seals with (its random id, which every
	// sealed message carries anyway, and epoch), and the runners it knows
	// hold the project's keys.
	KeyID    string   `json:"key_id,omitempty"`
	KeyEpoch uint64   `json:"key_epoch,omitempty"`
	Holders  []string `json:"holders,omitempty"`
	// Fingerprint is this runner's own key fingerprint.
	Fingerprint string `json:"fingerprint"`
	// Waiting are the runners of the project whose key the user hasn't
	// approved on this runner yet.
	Waiting []Waiting `json:"waiting,omitempty"`
}

// Waiting is a runner waiting for approval on another.
type Waiting struct {
	Runner      string    `json:"runner"`
	Name        string    `json:"name,omitempty"`
	Fingerprint string    `json:"fingerprint"`
	At          time.Time `json:"at"`
}

// Status is this runner's copy of project's notes, counted.
func (s *Service) Status(project string) (Status, error) {
	st, err := s.store(project)
	if err != nil {
		return Status{}, err
	}
	out, err := st.status(s.now())
	if err != nil {
		return out, err
	}
	out.Fingerprint = s.Fingerprint()
	out.Waiting = s.trust.waiting(project)
	return out, nil
}

func (s *Store) status(now time.Time) (out Status, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		live, err := liveEntries(tx, now)
		if err != nil {
			return err
		}
		for _, e := range live {
			out.Notes++
			out.Bytes += e.size()
		}
		if err := tx.QueryRow(`SELECT count(*) FROM entries WHERE deleted = 0 AND expires != 0 AND expires <= ?`, now.UnixNano()).Scan(&out.Expired); err != nil {
			return err
		}
		var newest int64
		if err := tx.QueryRow(`SELECT COALESCE(max(updated), 0) FROM entries`).Scan(&newest); err != nil {
			return err
		}
		if newest != 0 {
			out.Newest = time.Unix(0, newest).UTC()
		}
		if out.Vector, err = vectorOf(tx); err != nil {
			return err
		}
		if out.Stale, err = s.stale(tx, now); err != nil {
			return err
		}
		keys, err := keysOf(tx)
		if err != nil {
			return err
		}
		if k, ok := sealing(keys); ok {
			out.KeyID, out.KeyEpoch = k.ID, k.Epoch
		}
		out.Holders, err = holdersOf(tx)
		return err
	})
	return
}

// waiting is the runners waiting for approval here on project.
func (t *trust) waiting(project string) []Waiting {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Waiting
	for _, p := range t.peers.Pending {
		if p.Project == project {
			out = append(out, Waiting{Runner: p.Runner, Name: p.Name, Fingerprint: p.Fingerprint, At: p.At})
		}
	}
	return out
}

// Kinds of Removal.
const (
	RemoveAll     = "all"     // every note written before Removal.Before
	RemoveTask    = "task"    // the notes last written by one of Removal.Tasks
	RemoveRunner  = "runner"  // the notes last written by Removal.Runner
	RemoveExpired = "expired" // expired notes, now rather than at the next tidying
)

// Removal is a command to remove notes, from the hub's UI. The hub sends
// it unsigned, so a runner checks it is for a project it keeps notes for
// (the Service's allowed), and nothing else: a hub can't do more with one
// than it could already by dropping the runners' traffic.
type Removal struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Tasks name one task by its ids (e.g. the hub's and this runner's),
	// as notes record it (Entry.AuthorTask).
	Tasks  []string  `json:"tasks,omitempty"`
	Runner string    `json:"runner,omitempty"`
	Before time.Time `json:"before,omitzero"` // RemoveAll: when it was asked
}

// ErrBadRemoval is a malformed Removal.
var ErrBadRemoval = errors.New("malformed removal of shared notes")

// Check validates r.
func (r Removal) Check() error {
	short := func(s string, n int) bool { return s != "" && len(s) <= n && utf8.ValidString(s) }
	switch {
	case len(r.ID) > 64:
		return ErrBadRemoval
	case r.Kind == RemoveAll && !r.Before.IsZero(), r.Kind == RemoveExpired:
		return nil
	case r.Kind == RemoveRunner && short(r.Runner, 100):
		return nil
	case r.Kind == RemoveTask && len(r.Tasks) > 0 && len(r.Tasks) <= 4 && !slices.ContainsFunc(r.Tasks, func(t string) bool { return !short(t, 200) }):
		return nil
	}
	return ErrBadRemoval
}

// Remove applies r to this runner's copy of project's notes, and sends what
// it wrote to the project's other runners, and returns how many live notes
// it removed here. Every removal is an ordinary synced write, so a runner
// that is offline now takes it when it catches up and can't bring the
// notes back: remove-all is one clear ("all before r.Before", Clear), the
// filters a tombstone per note that matched. Expired notes are purged, as
// tidying does on every copy at the same moment.
//
// A stale copy catches up (resyncs) first, as a resync would drop what it
// wrote before.
func (s *Service) Remove(ctx context.Context, project string, r Removal) (int, error) {
	if err := r.Check(); err != nil {
		return 0, err
	}
	st, err := s.store(project)
	if err != nil {
		return 0, err
	}
	if stale, err := st.Stale(s.now()); err != nil {
		return 0, err
	} else if stale {
		if err := s.CatchUp(ctx, project); err != nil {
			return 0, err
		}
		if stale, err := st.Stale(s.now()); err != nil || stale {
			return 0, errors.Join(err, ErrStale)
		}
	}
	switch r.Kind {
	case RemoveExpired:
		rep, err := s.Tidy(ctx, project)
		return rep.Expired, err
	case RemoveAll:
		e, n, err := st.clear(r.Before, "", s.now())
		if err != nil {
			return 0, err
		}
		s.send(ctx, project, []Entry{e})
		return n, nil
	}
	match := func(e Entry) bool { return e.Writer == r.Runner }
	if r.Kind == RemoveTask {
		match = func(e Entry) bool { return e.AuthorTask != "" && slices.Contains(r.Tasks, e.AuthorTask) }
	}
	ts, err := st.removeWhere(match, s.now())
	if err != nil {
		return 0, fmt.Errorf("remove shared notes: %w", err)
	}
	s.send(ctx, project, ts)
	return len(ts), nil
}

// removeWhere deletes the live notes that match, as this runner's writes,
// and returns the tombstones.
func (s *Store) removeWhere(match func(Entry) bool, now time.Time) (out []Entry, err error) {
	now = now.UTC()
	err = s.tx(func(tx *sql.Tx) error {
		live, err := liveEntries(tx, now)
		if err != nil {
			return err
		}
		for _, e := range live {
			if !match(e) {
				continue
			}
			t, err := s.tombstone(tx, e, "", now)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return nil
	})
	return
}
