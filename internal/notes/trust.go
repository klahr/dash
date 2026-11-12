package notes

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/klahr/dash/internal/fsutil"
)

// Which runners get a project's key is the user's call, made on the runners
// themselves, never on the hub: the hub relays the keys runners send, and
// could swap in one of its own. So:
//
//   - The user approves a peer by its key's fingerprint, on this runner
//     (`dash runner notes approve <fingerprint>`, after comparing it with
//     what `dash runner notes` shows on the peer). This runner wraps its
//     project keys only for approved keys, and takes keys only from them.
//     Approval goes both ways: a runner that took its first key from
//     whoever offered one could be handed the hub's own key.
//   - Each runner id's key is pinned the first time it is seen (TOFU). A
//     different key for that id later is refused, loudly, until the user
//     approves the new fingerprint.
//
// The approvals file is written by the CLI only and read by the running
// runner; the peers file the other way round.

// ApprovalsPath and PeersPath are the trust files in a runner's shared
// notes folder.
func ApprovalsPath(sharedDir string) string { return filepath.Join(sharedDir, "approvals.json") }

// PeersPath: see ApprovalsPath.
func PeersPath(sharedDir string) string { return filepath.Join(sharedDir, "peers.json") }

// Approvals are the fingerprints the user approved or revoked on this
// runner.
type Approvals struct {
	Approved map[string]time.Time `json:"approved,omitempty"`
	Revoked  map[string]time.Time `json:"revoked,omitempty"`
}

// ReadApprovals reads the approvals file (none: empty).
func ReadApprovals(sharedDir string) (Approvals, error) {
	var a Approvals
	b, err := os.ReadFile(ApprovalsPath(sharedDir))
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return a, fmt.Errorf("%s: %w", ApprovalsPath(sharedDir), err)
	}
	return a, nil
}

func writeApprovals(sharedDir string, a Approvals) error {
	if err := os.MkdirAll(sharedDir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(a, "", "  ")
	return fsutil.WriteFileAtomic(ApprovalsPath(sharedDir), b, 0o600)
}

// Approve approves the runner key with fingerprint fp on this runner: it
// may get this runner's project keys, and give it theirs. It returns fp as
// written.
func Approve(sharedDir, fp string, now time.Time) (string, error) {
	return setApproval(sharedDir, fp, now, true)
}

// Revoke withdraws approval of fingerprint fp: this runner stops trading
// keys with it and rotates every project key it may hold.
func Revoke(sharedDir, fp string, now time.Time) (string, error) {
	return setApproval(sharedDir, fp, now, false)
}

func setApproval(sharedDir, fp string, now time.Time, approve bool) (string, error) {
	fp, err := NormalizeFingerprint(fp)
	if err != nil {
		return "", err
	}
	a, err := ReadApprovals(sharedDir)
	if err != nil {
		return "", err
	}
	if a.Approved == nil {
		a.Approved = map[string]time.Time{}
	}
	if a.Revoked == nil {
		a.Revoked = map[string]time.Time{}
	}
	if approve {
		a.Approved[fp] = now.UTC()
		delete(a.Revoked, fp)
	} else {
		a.Revoked[fp] = now.UTC()
		delete(a.Approved, fp)
	}
	return fp, writeApprovals(sharedDir, a)
}

// Pin is the key a runner id was first seen with.
type Pin struct {
	Key         PublicKey `json:"key"`
	Fingerprint string    `json:"fingerprint"`
	Name        string    `json:"name,omitempty"` // as the runner calls itself
	FirstSeen   time.Time `json:"first_seen"`
}

// Pending is a runner that asked for a project's key, or offered one, with
// a key the user hasn't approved here.
type Pending struct {
	Runner      string    `json:"runner"`
	Name        string    `json:"name,omitempty"`
	Fingerprint string    `json:"fingerprint"`
	Project     string    `json:"project"`
	At          time.Time `json:"at"`
}

// KeyWarning is a runner seen with a key other than the one pinned for it.
type KeyWarning struct {
	Runner  string    `json:"runner"`
	Name    string    `json:"name,omitempty"`
	Pinned  string    `json:"pinned"`  // fingerprint
	Offered string    `json:"offered"` // fingerprint
	At      time.Time `json:"at"`
}

// Peers is what this runner knows of other runners' keys.
type Peers struct {
	Pins     map[string]Pin `json:"pins,omitempty"` // runner id → its key
	Pending  []Pending      `json:"pending,omitempty"`
	Warnings []KeyWarning   `json:"warnings,omitempty"`
}

// ReadPeers reads the peers file (none: empty).
func ReadPeers(sharedDir string) (Peers, error) {
	var p Peers
	b, err := os.ReadFile(PeersPath(sharedDir))
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(b, &p)
}

const (
	maxPending  = 50
	maxWarnings = 20
)

// trust is the running runner's view of both files.
type trust struct {
	dir string
	now func() time.Time

	mu      sync.Mutex
	stamp   string // the approvals file's size and time, when last read
	gen     int    // bumped on every read that found a change
	appr    Approvals
	peers   Peers
	revoked map[string]bool // fingerprints revoked when last read
}

func openTrust(dir string, now func() time.Time) (*trust, error) {
	p, err := ReadPeers(dir)
	if err != nil {
		return nil, err
	}
	if p.Pins == nil {
		p.Pins = map[string]Pin{}
	}
	t := &trust{dir: dir, now: now, peers: p}
	t.reload()
	return t, nil
}

// reload re-reads the approvals file if it changed (the CLI writes it while
// the runner runs), and returns its generation: it grows on every change.
func (t *trust) reload() int {
	var stamp string
	if fi, err := os.Stat(ApprovalsPath(t.dir)); err == nil {
		stamp = fmt.Sprint(fi.Size(), fi.ModTime().UnixNano())
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if stamp == t.stamp && t.revoked != nil {
		return t.gen
	}
	a, err := ReadApprovals(t.dir)
	if err != nil {
		slog.Warn("shared notes: can't read approvals", "err", err)
		return t.gen
	}
	t.stamp, t.appr = stamp, a
	t.revoked = map[string]bool{}
	for fp := range a.Revoked {
		t.revoked[fp] = true
	}
	t.gen++
	return t.gen
}

// revokedRunners are the runner ids pinned with a key the user revoked.
func (t *trust) revokedRunners() []string {
	t.reload()
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for id, p := range t.peers.Pins {
		if t.revoked[p.Fingerprint] {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

func (t *trust) saveLocked() {
	if err := os.MkdirAll(t.dir, 0o700); err != nil {
		slog.Warn("shared notes: save peers", "err", err)
		return
	}
	b, _ := json.MarshalIndent(t.peers, "", "  ")
	if err := fsutil.WriteFileAtomic(PeersPath(t.dir), b, 0o600); err != nil {
		slog.Warn("shared notes: save peers", "err", err)
	}
}

func (t *trust) approvedLocked(key PublicKey) bool {
	fp := key.Fingerprint()
	_, ok := t.appr.Approved[fp]
	return ok && !t.revoked[fp]
}

// trusted reports whether the user approved key here.
func (t *trust) trusted(key PublicKey) bool {
	t.reload()
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.approvedLocked(key)
}

// see checks that runner shows the key pinned for it, pinning key the
// first time. A different key is refused (ErrKeyChanged) and logged
// loudly, unless the user approved the new key's fingerprint here: then it
// replaces the pin.
func (t *trust) see(runner, name string, key PublicKey) error {
	if runner == "" || key.IsZero() {
		return ErrBadEntry
	}
	name = clip(name, 64)
	t.reload()
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.peers.Pins[runner]
	switch {
	case ok && p.Key == key:
		if name != "" && p.Name != name {
			p.Name = name
			t.peers.Pins[runner] = p
			t.saveLocked()
		}
		return nil
	case ok:
		approved := t.approvedLocked(key)
		w := KeyWarning{Runner: runner, Name: name, Pinned: p.Fingerprint, Offered: key.Fingerprint(), At: t.now().UTC()}
		if i := slices.IndexFunc(t.peers.Warnings, func(x KeyWarning) bool {
			return x.Runner == w.Runner && x.Offered == w.Offered
		}); i >= 0 {
			t.peers.Warnings[i].At = w.At
		} else {
			t.peers.Warnings = append(t.peers.Warnings, w)
			if len(t.peers.Warnings) > maxWarnings {
				t.peers.Warnings = t.peers.Warnings[len(t.peers.Warnings)-maxWarnings:]
			}
			if approved {
				slog.Warn("shared notes: runner's key CHANGED; the new key is approved here, so it replaces the old one",
					"runner", runner, "name", name, "pinned", p.Fingerprint, "new", w.Offered)
			} else {
				slog.Error("shared notes: runner's key CHANGED — REFUSED. Someone (the hub?) may be posing as it. "+
					"If it really got a new key, check `dash runner notes` on it shows the new fingerprint, then approve it here with: dash runner notes approve "+w.Offered,
					"runner", runner, "name", name, "pinned", p.Fingerprint, "offered", w.Offered)
			}
		}
		if !approved {
			t.saveLocked()
			return ErrKeyChanged
		}
	}
	t.peers.Pins[runner] = Pin{Key: key, Fingerprint: key.Fingerprint(), Name: name, FirstSeen: t.now().UTC()}
	t.saveLocked()
	return nil
}

// pinned is the key pinned for runner.
func (t *trust) pinned(runner string) (PublicKey, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.peers.Pins[runner]
	return p.Key, ok
}

// ask records that runner (with key, not approved here) wants project's
// key or offers its own, and reports whether that is news.
func (t *trust) ask(runner, name string, key PublicKey, project string) bool {
	p := Pending{Runner: runner, Name: clip(name, 64), Fingerprint: key.Fingerprint(), Project: project, At: t.now().UTC()}
	t.mu.Lock()
	defer t.mu.Unlock()
	if i := slices.IndexFunc(t.peers.Pending, func(x Pending) bool {
		return x.Runner == p.Runner && x.Project == p.Project && x.Fingerprint == p.Fingerprint
	}); i >= 0 {
		t.peers.Pending[i].At = p.At // not saved: only a timestamp
		return false
	}
	t.peers.Pending = append(t.peers.Pending, p)
	if len(t.peers.Pending) > maxPending {
		t.peers.Pending = t.peers.Pending[len(t.peers.Pending)-maxPending:]
	}
	t.saveLocked()
	return true
}

// approvedPending takes the pending runners whose keys are now approved
// off the list, and returns them.
func (t *trust) approvedPending() []Pending {
	t.reload()
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Pending
	t.peers.Pending = slices.DeleteFunc(t.peers.Pending, func(p Pending) bool {
		pin, ok := t.peers.Pins[p.Runner]
		if ok && pin.Fingerprint == p.Fingerprint && t.approvedLocked(pin.Key) {
			out = append(out, p)
			return true
		}
		return false
	})
	if len(out) > 0 {
		t.saveLocked()
	}
	return out
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
