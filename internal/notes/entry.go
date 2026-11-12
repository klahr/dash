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

// Limits on what a project's notes may hold.
const (
	MaxEntrySize = 8 << 10 // bytes of key, body and tags in one entry
	MaxEntries   = 500     // live entries per project
	MaxTags      = 16
	MaxKeyLen    = 200
)

// Errors a write can fail with.
var (
	ErrTooLarge   = fmt.Errorf("a shared note may hold at most %d bytes", MaxEntrySize)
	ErrTooMany    = fmt.Errorf("this project already has %d shared notes; delete some first", MaxEntries)
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
	Expires    time.Time `json:"expires,omitzero"`  // zero: never
	Deleted    bool      `json:"deleted,omitempty"` // a tombstone: the entry was deleted

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

// Draft is a write asked of this runner.
type Draft struct {
	ID         string // "" makes a new entry; else the entry to replace
	Key        string
	Body       string
	Tags       []string
	AuthorTask string
	TTL        time.Duration // 0: no expiry
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
