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
//   - Or the user enrolls runners with a root key of theirs (enroll.go):
//     runners enrolled under the same root trust each other's certified
//     keys without approvals, and a key the root revoked is never trusted.
//
// The approvals and enrollment files are written by the CLI only and read
// by the running runner; the peers file the other way round.

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
	// Override are approvals the user made over a root revocation
	// (approve --force).
	Override map[string]time.Time `json:"override,omitempty"`
	// RootRevocations are the revocations signed with the root key here
	// (revoke --key); the runner passes them on to its peers.
	RootRevocations []Revocation `json:"root_revocations,omitempty"`
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
// written. A key the root key revoked is refused (ErrRootRevoked).
func Approve(sharedDir, fp string, now time.Time) (string, error) {
	return setApproval(sharedDir, fp, now, true, false)
}

// ApproveOverride is Approve, even of a key the root key revoked.
func ApproveOverride(sharedDir, fp string, now time.Time) (string, error) {
	return setApproval(sharedDir, fp, now, true, true)
}

// Revoke withdraws approval of fingerprint fp: this runner stops trading
// keys with it and rotates every project key it may hold.
func Revoke(sharedDir, fp string, now time.Time) (string, error) {
	return setApproval(sharedDir, fp, now, false, false)
}

func setApproval(sharedDir, fp string, now time.Time, approve, override bool) (string, error) {
	fp, err := NormalizeFingerprint(fp)
	if err != nil {
		return "", err
	}
	ts, _, err := ReadTrustState(sharedDir)
	if err != nil {
		return "", err
	}
	a := ts.appr
	if _, revoked := ts.RootRevoked(fp); approve && revoked {
		if !override {
			return "", fmt.Errorf("%w: it is never trusted again; to approve it anyway, pass --force", ErrRootRevoked)
		}
		if a.Override == nil {
			a.Override = map[string]time.Time{}
		}
		a.Override[fp] = now.UTC()
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
		delete(a.Override, fp)
	}
	return fp, writeApprovals(sharedDir, a)
}

// Pin is the key a runner id was first seen with.
type Pin struct {
	Key         PublicKey `json:"key"`
	Fingerprint string    `json:"fingerprint"`
	Name        string    `json:"name,omitempty"` // as the runner calls itself
	FirstSeen   time.Time `json:"first_seen"`
	// Cert is the root's certificate for this key, as the runner sent it
	// (checked against this runner's root whenever it counts).
	Cert *Cert `json:"cert,omitempty"`
}

// Pending is a runner that asked for a project's key, or offered one, with
// a key the user hasn't approved here.
type Pending struct {
	Runner      string    `json:"runner"`
	Name        string    `json:"name,omitempty"`
	Fingerprint string    `json:"fingerprint"`
	Project     string    `json:"project"`
	At          time.Time `json:"at"`
	// OtherRoot is the fingerprint of the root its certificate is signed
	// by, when that isn't this runner's root.
	OtherRoot string `json:"other_root,omitempty"`
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
	// Revocations are the root revocations learned from peers.
	Revocations []Revocation `json:"revocations,omitempty"`
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

// trust is the running runner's view of its trust files.
type trust struct {
	dir string
	now func() time.Time

	mu    sync.Mutex
	stamp string // the approvals and enrollment files' sizes and times, when last read
	gen   int    // bumped on every read that found a change, and on news
	appr  Approvals
	enr   *Enrollment
	state *TrustState
	peers Peers
	// news: the enrollment changed, or a root revocation came in; tell
	// every project's peers (Reconcile).
	news bool
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
	t.news = false // nothing to tell yet: the runner catches up on connecting
	return t, nil
}

func fileStamp(path string) string {
	if fi, err := os.Stat(path); err == nil {
		return fmt.Sprint(fi.Size(), fi.ModTime().UnixNano())
	}
	return "-"
}

// reload re-reads the approvals and enrollment files if they changed (the
// CLI writes them while the runner runs), and returns its generation: it
// grows on every change.
func (t *trust) reload() int {
	stamp := fileStamp(ApprovalsPath(t.dir)) + "/" + fileStamp(EnrollmentPath(t.dir))
	t.mu.Lock()
	defer t.mu.Unlock()
	if stamp == t.stamp && t.state != nil {
		return t.gen
	}
	a, err := ReadApprovals(t.dir)
	if err != nil {
		slog.Warn("shared notes: can't read approvals", "err", err)
		return t.gen
	}
	e, err := ReadEnrollment(t.dir)
	if err != nil {
		slog.Warn("shared notes: can't read the enrollment", "err", err)
		return t.gen
	}
	if t.state != nil && len(a.RootRevocations) != len(t.appr.RootRevocations) {
		t.news = true // revoked here with the root key: tell every project
	}
	if t.state != nil && !sameEnrollment(t.enr, e) {
		t.news = true
		if e != nil {
			slog.Info("shared notes: enrolled under a root key", "root", RootFingerprint(e.RootKey()))
		}
	}
	t.stamp, t.appr, t.enr = stamp, a, e
	t.restateLocked()
	t.gen++
	return t.gen
}

func sameEnrollment(a, b *Enrollment) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Root == b.Root && a.Cert.Sig == b.Cert.Sig
}

func (t *trust) restateLocked() {
	t.state = newTrustState(t.appr, t.enr, t.peers.Revocations)
}

// standingLocked is how this runner stands to runner with key, and the
// certificate kept for it.
func (t *trust) standingLocked(runner string, key PublicKey) Standing {
	var cert *Cert
	if p, ok := t.peers.Pins[runner]; ok && p.Key == key {
		cert = p.Cert
	}
	return t.state.Of(runner, key, cert)
}

// revokedRunners are the runner ids pinned with a key the user revoked,
// here or with the root key, and those only the previous root trusted.
func (t *trust) revokedRunners() []string {
	t.reload()
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for id, p := range t.peers.Pins {
		switch t.standingLocked(id, p.Key) {
		case RevokedHere, RevokedByRoot:
			out = append(out, id)
		default:
			if t.enr != nil && slices.Contains(t.enr.Dropped, id) && !t.standingLocked(id, p.Key).Trusted() {
				out = append(out, id)
			}
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

// trusted reports whether runner, with key, is trusted here: approved by
// the user, or certified by this runner's root; and not revoked.
func (t *trust) trusted(runner string, key PublicKey) bool {
	t.reload()
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.standingLocked(runner, key).Trusted()
}

// why is the reason runner, with key, isn't trusted here, for the log.
func (t *trust) why(runner string, key PublicKey) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var cert *Cert
	if p, ok := t.peers.Pins[runner]; ok && p.Key == key {
		cert = p.Cert
	}
	switch st := t.state.Of(runner, key, cert); st {
	case RevokedByRoot:
		return "its key was revoked with the root key: it must rotate its key (dash runner notes rotate) and enroll again"
	case RevokedHere:
		return "its key is revoked here"
	case OtherRoot:
		if t.state.root == nil {
			return "it is enrolled under root key " + t.state.otherRoot(cert) + " and this runner isn't: enroll it with the same key (dash runner notes enroll --key), or approve it here with: dash runner notes approve " + key.Fingerprint()
		}
		return "it is enrolled under a different root key " + t.state.otherRoot(cert) + " (this runner's is " + t.state.Root() + "): enroll both with the same key, or approve it here with: dash runner notes approve " + key.Fingerprint()
	}
	return "its key isn't approved here. Run `dash runner notes` on it: if it shows the same fingerprint, approve it here with: dash runner notes approve " + key.Fingerprint() + " (or enroll both runners with the same root key)"
}

// keepCertLocked keeps cert in pin p of runner if it is better than the
// one kept: one this runner's root signed always is; another root's only
// replaces one that isn't this root's. One that doesn't hold, or is for
// another runner or key, is dropped.
func (t *trust) keepCertLocked(runner string, p *Pin, cert *Cert) bool {
	if cert == nil || cert.Runner != runner || cert.Key != p.Key || cert.signer() == nil || p.Cert != nil && p.Cert.Sig == cert.Sig {
		return false
	}
	if cert.vouches(t.state.root, runner, p.Key) || !p.Cert.vouches(t.state.root, runner, p.Key) {
		c := *cert
		p.Cert = &c
		return true
	}
	return false
}

// see checks that runner shows the key pinned for it, pinning key the
// first time. A different key is refused (ErrKeyChanged) and logged
// loudly, unless the user approved the new key's fingerprint here, or it
// comes with a certificate from this runner's root (after `dash runner
// notes rotate`): then it replaces the pin. cert is the certificate the
// runner sent (nil: none).
func (t *trust) see(runner, name string, key PublicKey, cert *Cert) error {
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
		changed := t.keepCertLocked(runner, &p, cert)
		if name != "" && p.Name != name {
			p.Name, changed = name, true
		}
		if changed {
			t.peers.Pins[runner] = p
			t.saveLocked()
		}
		return nil
	case ok:
		st := t.state.Of(runner, key, cert)
		w := KeyWarning{Runner: runner, Name: name, Pinned: p.Fingerprint, Offered: key.Fingerprint(), At: t.now().UTC()}
		if st == Certified {
			slog.Info("shared notes: a runner has a new key, certified by the root key; it replaces the old one",
				"runner", runner, "name", name, "old", p.Fingerprint, "new", w.Offered)
			break
		}
		if i := slices.IndexFunc(t.peers.Warnings, func(x KeyWarning) bool {
			return x.Runner == w.Runner && x.Offered == w.Offered
		}); i >= 0 {
			t.peers.Warnings[i].At = w.At
		} else {
			t.peers.Warnings = append(t.peers.Warnings, w)
			if len(t.peers.Warnings) > maxWarnings {
				t.peers.Warnings = t.peers.Warnings[len(t.peers.Warnings)-maxWarnings:]
			}
			if st == Approved {
				slog.Warn("shared notes: runner's key CHANGED; the new key is approved here, so it replaces the old one",
					"runner", runner, "name", name, "pinned", p.Fingerprint, "new", w.Offered)
			} else {
				slog.Error("shared notes: runner's key CHANGED — REFUSED. Someone (the hub?) may be posing as it. "+
					"If it really got a new key, enroll it with the root key (dash runner notes enroll), or check `dash runner notes` on it shows the new fingerprint, then approve it here with: dash runner notes approve "+w.Offered,
					"runner", runner, "name", name, "pinned", p.Fingerprint, "offered", w.Offered, "standing", string(st))
			}
		}
		if st != Approved {
			t.saveLocked()
			return ErrKeyChanged
		}
	}
	np := Pin{Key: key, Fingerprint: key.Fingerprint(), Name: name, FirstSeen: t.now().UTC()}
	if np.Name == "" {
		np.Name = p.Name
	}
	t.keepCertLocked(runner, &np, cert)
	t.peers.Pins[runner] = np
	t.saveLocked()
	return nil
}

// learn keeps the root revocations in rs that this runner didn't know and
// that its root signed, and reports whether there were any.
func (t *trust) learn(rs []Revocation) bool {
	if len(rs) == 0 {
		return false
	}
	t.reload()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state.root == nil {
		return false
	}
	var added []string
	for _, r := range rs[:min(len(rs), maxHelloRevocations)] {
		if _, known := t.state.revoked[r.Fingerprint]; known || !r.by(t.state.root) {
			continue
		}
		t.peers.Revocations = append(t.peers.Revocations, r)
		t.state.revoked[r.Fingerprint] = r
		added = append(added, r.Fingerprint)
	}
	if len(added) == 0 {
		return false
	}
	if n := len(t.peers.Revocations); n > maxRevocations {
		t.peers.Revocations = t.peers.Revocations[n-maxRevocations:]
	}
	slog.Warn("shared notes: runner keys were revoked with the root key; they are never trusted again", "fingerprints", added)
	t.saveLocked()
	t.gen++
	t.news = true
	return true
}

// takeNews reports, once, whether there is news to tell every project's
// peers: a new enrollment, or root revocations.
func (t *trust) takeNews() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := t.news
	t.news = false
	return n
}

// introduce is what this runner, runner with key, tells peers of its
// trust in its hello: its certificate, and the root revocations it knows.
func (t *trust) introduce(runner string, key PublicKey) (*Cert, []Revocation) {
	t.reload()
	t.mu.Lock()
	defer t.mu.Unlock()
	revs := t.state.RootRevocations()
	return t.state.selfCert(runner, key), revs[:min(len(revs), maxHelloRevocations)]
}

// enrollment is this runner's root fingerprint ("": none) and whether its
// certificate is for key.
func (t *trust) enrollment(runner string, key PublicKey) (root string, certified bool) {
	t.reload()
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state.Root(), t.state.selfCert(runner, key) != nil
}

// pinned is the key pinned for runner.
func (t *trust) pinned(runner string) (PublicKey, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.peers.Pins[runner]
	return p.Key, ok
}

// ask records that runner (with key, not trusted here) wants project's
// key or offers its own, and reports whether that is news.
func (t *trust) ask(runner, name string, key PublicKey, project string) bool {
	p := Pending{Runner: runner, Name: clip(name, 64), Fingerprint: key.Fingerprint(), Project: project, At: t.now().UTC()}
	t.mu.Lock()
	defer t.mu.Unlock()
	if pin, ok := t.peers.Pins[runner]; ok && pin.Key == key {
		p.OtherRoot = t.state.otherRoot(pin.Cert)
	}
	if i := slices.IndexFunc(t.peers.Pending, func(x Pending) bool {
		return x.Runner == p.Runner && x.Project == p.Project && x.Fingerprint == p.Fingerprint
	}); i >= 0 {
		t.peers.Pending[i].At = p.At // not saved: only a timestamp
		if t.peers.Pending[i].OtherRoot != p.OtherRoot {
			t.peers.Pending[i].OtherRoot = p.OtherRoot
			t.saveLocked()
			return true
		}
		return false
	}
	t.peers.Pending = append(t.peers.Pending, p)
	if len(t.peers.Pending) > maxPending {
		t.peers.Pending = t.peers.Pending[len(t.peers.Pending)-maxPending:]
	}
	t.saveLocked()
	return true
}

// approvedPending takes the pending runners whose keys are now trusted
// (approved, or certified) off the list, and returns them.
func (t *trust) approvedPending() []Pending {
	t.reload()
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Pending
	t.peers.Pending = slices.DeleteFunc(t.peers.Pending, func(p Pending) bool {
		pin, ok := t.peers.Pins[p.Runner]
		if ok && pin.Fingerprint == p.Fingerprint && t.standingLocked(p.Runner, pin.Key).Trusted() {
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
