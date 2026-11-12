package notes

import (
	"cmp"
	"database/sql"
	"slices"
	"time"
)

// A project's keys live in its notes database, next to the notes (this
// runner holds both in the clear; only what leaves it is sealed). It keeps
// the keys of the latest epoch only: a rotation drops the older ones, so
// messages sealed with them are refused from then on.

const keySchema = `
CREATE TABLE IF NOT EXISTS keys (
	id      TEXT PRIMARY KEY,
	epoch   INTEGER NOT NULL,
	secret  BLOB NOT NULL,
	created INTEGER NOT NULL
);
-- Runners known to hold a key (this one included).
CREATE TABLE IF NOT EXISTS holders (key_id TEXT NOT NULL, runner TEXT NOT NULL, PRIMARY KEY (key_id, runner));
`

func keysOf(tx *sql.Tx) ([]projectKey, error) {
	rows, err := tx.Query(`SELECT id, epoch, secret, created FROM keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []projectKey
	for rows.Next() {
		var k projectKey
		var created int64
		if err := rows.Scan(&k.ID, &k.Epoch, &k.Secret, &created); err != nil {
			return nil, err
		}
		k.Created = time.Unix(0, created).UTC()
		out = append(out, k)
	}
	return out, rows.Err()
}

func holdersOf(tx *sql.Tx) ([]string, error) {
	rows, err := tx.Query(`SELECT DISTINCT runner FROM holders ORDER BY runner`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func addKeyTx(tx *sql.Tx, k projectKey, holders []string) error {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO keys (id, epoch, secret, created) VALUES (?,?,?,?)`,
		k.ID, k.Epoch, k.Secret, nanos(k.Created)); err != nil {
		return err
	}
	for _, h := range holders {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO holders (key_id, runner) VALUES (?,?)`, k.ID, h); err != nil {
			return err
		}
	}
	return nil
}

func dropKeysTx(tx *sql.Tx) error {
	if _, err := tx.Exec(`DELETE FROM keys`); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM holders`)
	return err
}

// sealing is the key that seals: the highest id of the latest epoch.
func sealing(keys []projectKey) (projectKey, bool) {
	if len(keys) == 0 {
		return projectKey{}, false
	}
	return slices.MaxFunc(keys, func(a, b projectKey) int {
		return cmp.Or(cmp.Compare(a.Epoch, b.Epoch), cmp.Compare(a.ID, b.ID))
	}), true
}

// Keys returns the project's keys, making the first one if it has none:
// a runner seals its notes with a key of its own until it trades keys with
// the project's other runners.
func (s *Store) Keys(now time.Time) (keys []projectKey, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		if keys, err = keysOf(tx); err != nil || len(keys) > 0 {
			return err
		}
		k, err := newProjectKey(1, now)
		if err != nil {
			return err
		}
		keys = []projectKey{k}
		return addKeyTx(tx, k, []string{s.self})
	})
	return keys, err
}

// Holders are the runners known to hold one of the project's keys.
func (s *Store) Holders() (out []string, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		out, err = holdersOf(tx)
		return err
	})
	return out, err
}

// AddHolder records that runner holds the keys of ids that this store has.
func (s *Store) AddHolder(runner string, ids []string) error {
	return s.tx(func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO holders (key_id, runner) SELECT id, ? FROM keys WHERE id = ?`, runner, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// AddKeys takes keys a peer wrapped for this runner: those of a later
// epoch replace the ones here, those of the same epoch join them, older
// ones are dropped. holders hold them. It reports whether a key is new.
func (s *Store) AddKeys(keys []projectKey, holders []string) (added bool, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		have, err := keysOf(tx)
		if err != nil {
			return err
		}
		var epoch uint64
		for _, k := range have {
			epoch = max(epoch, k.Epoch)
		}
		var top uint64
		for _, k := range keys {
			top = max(top, k.Epoch)
		}
		if top < epoch {
			return nil
		}
		if top > epoch {
			if err := dropKeysTx(tx); err != nil {
				return err
			}
			have = nil
		}
		for _, k := range keys {
			if k.Epoch != top {
				continue
			}
			if !slices.ContainsFunc(have, func(h projectKey) bool { return h.ID == k.ID }) {
				added = true
			}
			if err := addKeyTx(tx, k, append(slices.Clone(holders), s.self)); err != nil {
				return err
			}
		}
		return nil
	})
	return added, err
}

// Rotate replaces the project's keys with a new one of the next epoch,
// held by this runner only so far.
func (s *Store) Rotate(now time.Time) (k projectKey, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		have, err := keysOf(tx)
		if err != nil {
			return err
		}
		var epoch uint64
		for _, h := range have {
			epoch = max(epoch, h.Epoch)
		}
		if k, err = newProjectKey(epoch+1, now); err != nil {
			return err
		}
		if err := dropKeysTx(tx); err != nil {
			return err
		}
		return addKeyTx(tx, k, []string{s.self})
	})
	return k, err
}
