// Package notes is the shared notes space of a hub project: notes the runners
// working on it share, so a task on one runner can leave what it learned for
// tasks on the others.
//
// Every runner keeps a full copy per project (Store, SQLite), and reads it
// locally, also while the hub is unreachable. A write is saved locally
// first, then sent to the project's other runners (Transport); the hub only
// relays opaque bytes between them (Relay) and keeps nothing: there is no
// copy on the hub, and no hub page or API that shows a note.
//
// Each write gets a stamp: its writer (a runner id) and that writer's next
// counter. Entries merge last-writer-wins on a Lamport clock (Entry.wins),
// so every copy converges whatever order writes arrive in, and concurrent
// writers never wait for each other. A runner that was away catches up by
// sending its per-writer counters (Vector); its peers answer with what it
// lacks, and it sends them what they lack.
package notes

import (
	"cmp"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits on what one note may hold, and the defaults of Policy.
const (
	MaxEntrySize = 8 << 10 // bytes of key, body and tags in one entry: a hard limit
	MaxEntries   = 500     // live entries per project, by default
	MaxBytes     = 1 << 20 // bytes of all live entries of a project, by default
	MaxTags      = 16
	MaxKeyLen    = 200

	DefaultTTL       = 30 * 24 * time.Hour // a note's life unless its writer says otherwise
	DefaultRetention = 60 * 24 * time.Hour // how long tombstones are kept
)

// Policy is how a project's notes are tidied. Every runner of a project
// should use the same one; where they differ, the tidying they do still
// converges (it is made of ordinary writes), the stricter runner winning.
type Policy struct {
	// TTL is how long a note lives after its last write or confirmation,
	// unless its writer sets another expiry or makes it permanent.
	TTL time.Duration
	// MaxEntries and MaxBytes cap the project's live notes: past either,
	// the notes updated longest ago are evicted, permanent ones never.
	MaxEntries int
	MaxBytes   int
	// Retention is how long tombstones (deletes, evictions, clears) are
	// kept before they are purged. A runner that has been away from the
	// hub for longer discards its copy and resyncs it in full, so it can't
	// bring back notes the others purged.
	Retention time.Duration
}

// DefaultPolicy is the policy of a project nobody configured.
func DefaultPolicy() Policy {
	return Policy{TTL: DefaultTTL, MaxEntries: MaxEntries, MaxBytes: MaxBytes, Retention: DefaultRetention}
}

// orDefaults fills p's zero fields with the defaults.
func (p Policy) orDefaults() Policy {
	d := DefaultPolicy()
	if p.TTL <= 0 {
		p.TTL = d.TTL
	}
	if p.MaxEntries <= 0 {
		p.MaxEntries = d.MaxEntries
	}
	if p.MaxBytes <= 0 {
		p.MaxBytes = d.MaxBytes
	}
	if p.Retention <= 0 {
		p.Retention = d.Retention
	}
	return p
}

// Errors a write can fail with.
var (
	ErrTooLarge   = fmt.Errorf("a shared note may hold at most %d bytes", MaxEntrySize)
	ErrTooMany    = errors.New("this project's shared notes are full of permanent notes; delete some first")
	ErrTitleTaken = errors.New("another shared note already has this title")
	ErrStale      = errors.New("this runner's copy of the shared notes is too old: it waits for a full resync from another runner")
	ErrNoKey      = errors.New("a shared note needs a key (its title)")
	ErrNotFound   = errors.New("no such shared note")
	ErrSecret     = errors.New("the note looks like it holds a secret")
	ErrNoProject  = errors.New("shared notes aren't available for this project here")
	ErrBadEntry   = errors.New("malformed shared note")
	ErrNoRunnerID = errors.New("shared notes need this runner's id")
)

// Entry is one shared note, as stored and as sent between runners.
type Entry struct {
	// ID names the entry for good: "<writer>:<counter>" of the write that
	// created it.
	ID   string   `json:"id"`
	Key  string   `json:"key"` // its title
	Body string   `json:"body,omitempty"`
	Tags []string `json:"tags,omitempty"`
	// AuthorTask is the task whose run made the last write ("" if none).
	AuthorTask string    `json:"author_task,omitempty"`
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
	Expires    time.Time `json:"expires,omitzero"`    // zero: never
	Permanent  bool      `json:"permanent,omitempty"` // never expires nor is evicted
	Deleted    bool      `json:"deleted,omitempty"`   // a tombstone: the entry was deleted
	// Before, on a tombstone without a key, makes it a clear: every note
	// of the project last updated before it is deleted.
	Before time.Time `json:"before,omitzero"`

	// The last write's version: Lamport clock, then its stamp.
	Clock  uint64 `json:"clock"`
	Writer string `json:"writer"`
	Seq    uint64 `json:"seq"`
}

// wins reports whether e is a later version of its entry than o: last
// writer wins, by Lamport clock, ties broken by writer and counter. Every
// runner orders two versions the same way, so all copies converge.
func (e Entry) wins(o Entry) bool {
	if c := cmp.Compare(e.Clock, o.Clock); c != 0 {
		return c > 0
	}
	if c := strings.Compare(e.Writer, o.Writer); c != 0 {
		return c > 0
	}
	return e.Seq > o.Seq
}

// Live reports whether e is shown at now: not deleted, not expired.
func (e Entry) Live(now time.Time) bool {
	return !e.Deleted && !e.expired(now)
}

func (e Entry) expired(now time.Time) bool { return !e.Expires.IsZero() && !now.Before(e.Expires) }

// size is what counts towards MaxEntrySize.
func (e Entry) size() int {
	n := len(e.Key) + len(e.Body)
	for _, t := range e.Tags {
		n += len(t)
	}
	return n
}

// entryID is the id of the entry first written by writer's write seq.
func entryID(writer string, seq uint64) string { return writer + ":" + strconv.FormatUint(seq, 10) }

// check validates e as it arrives from a peer (or before it leaves here).
func (e Entry) check() error {
	switch {
	case e.ID == "" || len(e.ID) > 200 || e.Writer == "" || len(e.Writer) > 100 || e.Seq == 0 || e.Clock == 0:
		return ErrBadEntry
	case !e.Deleted && strings.TrimSpace(e.Key) == "":
		return ErrNoKey
	case !e.Before.IsZero() && (!e.Deleted || e.Key != "" || e.Before.After(e.Updated)):
		return ErrBadEntry // a clear is a keyless tombstone, of the past
	case e.Permanent && !e.Expires.IsZero():
		return ErrBadEntry
	case len(e.Key) > MaxKeyLen || len(e.Tags) > MaxTags || e.size() > MaxEntrySize:
		return ErrTooLarge
	case !utf8.ValidString(e.Key) || !utf8.ValidString(e.Body) || !utf8.ValidString(e.AuthorTask) || len(e.AuthorTask) > 200:
		return ErrBadEntry
	}
	for _, t := range e.Tags {
		if t == "" || len(t) > 64 || !utf8.ValidString(t) {
			return ErrBadEntry
		}
	}
	return nil
}

// Vector is, per writer, the counter up to which a copy has every write of
// that writer (or a later version of the entry it wrote).
type Vector map[string]uint64

// isClear reports whether e is a project-wide "everything before" tombstone.
func (e Entry) isClear() bool { return !e.Before.IsZero() }

// title is e's key as titles compare: a project has one note per title.
func title(key string) string { return strings.ToLower(strings.TrimSpace(key)) }

// Draft is a write asked of this runner.
type Draft struct {
	// ID is the entry to replace; "" writes the note titled Key: a new one,
	// or the one that already has that title.
	ID         string
	Key        string
	Body       string
	Tags       []string
	AuthorTask string
	// The note's expiry: Permanent, else Expires, else TTL from now. With
	// none set, a new note lives the project's default TTL, and a rewritten
	// one keeps being permanent or lives the default TTL from now.
	TTL       time.Duration
	Expires   time.Time
	Permanent bool
}

// cleanTags trims, drops empty and duplicate tags, keeping the order.
func cleanTags(tags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
