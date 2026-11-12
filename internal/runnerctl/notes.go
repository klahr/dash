package runnerctl

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/klahr/dash/internal/notes"
	"github.com/klahr/dash/internal/tunnel"
)

const notesUsage = `Shows this runner's shared notes key and which runners it trusts, or changes that.

  dash runner notes                              what this runner trusts, and who waits
  dash runner notes keygen [--out <path>]        make a root key (default ~/.ssh/dash_notes)
  dash runner notes enroll --key <path>          enroll this runner under that root key
  dash runner notes approve <fingerprint>        trust one runner by hand
  dash runner notes revoke <fingerprint>         stop trusting it here, and rotate the keys it held
  dash runner notes revoke --key <path> <fp>     revoke it with the root key, on every enrolled runner
  dash runner notes rotate [--key <path>]        give this runner a new key (and enroll it again)

Runners enrolled with the same root key trust each other: no approvals. The
root key is any ed25519 SSH key you keep (keygen, or ssh-keygen -t ed25519);
it is read only while enroll, rotate or revoke --key sign with it (or used
from ssh-agent), and never stored on the runner or sent to the hub. Delete it
from a runner after enrolling unless that is the computer you keep it on.

keygen     makes an ed25519 key with a passphrase (asked twice on the
           terminal; --no-passphrase for none) in OpenSSH's format, and
           never replaces an existing file. It needs no runner: make the key
           on the computer you trust most, and back it up.
enroll     signs a certificate for this runner's key with the root key. The
           running runner picks it up within seconds. Under a different root
           than before it needs --force, which drops the trust the old
           root's certificates gave (approvals stay).
revoke     without --key: this runner only, at once. With --key (on a runner
           enrolled under that key): signed by the root, so every runner
           enrolled under it refuses that key for good, as the revocation
           reaches it through the hub. The hub can delay that but not forge
           one; a plain revoke on each runner is the fallback.
rotate     replaces this runner's key; with --key (or the root key in
           ssh-agent) enrolls the new key at once, and the other enrolled
           runners take it. A revoked runner must rotate and enroll again.
approve    is for runners that aren't enrolled under this runner's root:
           approve each pair of runners on both, after comparing the
           fingerprint with the one ` + "`dash runner notes`" + ` shows on that
           runner itself, not only with the hub's page. A key revoked with the
           root key is refused unless --force.`

// notes runs `dash runner notes [keygen|enroll|approve|revoke|rotate]`:
// shows this runner's shared notes key fingerprint and the runners it
// trades project keys with, or changes who it trusts. This is where the
// user decides who gets a project's key; the hub only shows it and could
// lie.
func (c *CLI) notes(args []string) error {
	// The command and fingerprint come first or after the flags.
	var sub, fp string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			fp, args = args[0], args[1:]
		}
	}
	fs, data := c.flags("notes", notesUsage)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: dash runner notes [keygen|enroll|approve|revoke|rotate] [flags]")
		fmt.Fprintln(fs.Output(), notesUsage)
		fs.PrintDefaults()
	}
	key := fs.String("key", "", "the root key: an ed25519 SSH private key file (enroll, rotate, revoke)")
	force := fs.Bool("force", false, "enroll/rotate: under a different root than before; approve: a key revoked with the root key")
	out := fs.String("out", DefaultNotesKey(c.Home), "keygen: where to write the key (and <path>.pub)")
	noPass := fs.Bool("no-passphrase", false, "keygen: store the key without a passphrase")
	// Flags may also follow the fingerprint (revoke --key <path> <fp> -data …).
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		rest = append(rest, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if sub != "keygen" { // keygen needs no runner
		if err := MigrateRunnerData(c.Home, *data); err != nil {
			return err
		}
	}
	if fp == "" && len(rest) == 1 {
		fp = rest[0]
	} else if len(rest) > 0 {
		return fmt.Errorf("unexpected %q", rest[len(rest)-1])
	}
	shared := filepath.Join(*data, "shared")
	now := time.Now()
	noFP := func() error {
		if fp != "" {
			return fmt.Errorf("unexpected %q", fp)
		}
		return nil
	}
	switch sub {
	case "":
		return c.notesShow(*data, shared)
	case "keygen":
		if err := noFP(); err != nil {
			return err
		}
		return c.notesKeygen(*out, *noPass)
	case "enroll":
		if err := noFP(); err != nil {
			return err
		}
		if *key == "" {
			return errors.New("usage: dash runner notes enroll --key <path to the root key>")
		}
		return c.notesEnroll(*data, shared, *key, *force)
	case "rotate":
		if err := noFP(); err != nil {
			return err
		}
		return c.notesRotate(*data, shared, *key, *force)
	case "approve", "revoke":
		if fp == "" {
			return fmt.Errorf("usage: dash runner notes %s <fingerprint>", sub)
		}
		if *key != "" && sub == "approve" {
			return errors.New("approve takes no --key: enroll runners with the root key instead (dash runner notes enroll)")
		}
		return c.notesSet(*data, shared, sub, fp, *key, *force, now)
	}
	return fmt.Errorf("unknown notes command %q (want keygen, enroll, approve, revoke or rotate)", sub)
}

func (c *CLI) notesSet(data, shared, sub, fp, key string, force bool, now time.Time) error {
	id, err := notes.LoadIdentity(notes.IdentityPath(data))
	if err != nil {
		return err
	}
	if n, err := notes.NormalizeFingerprint(fp); err == nil && n == id.Fingerprint() {
		return errors.New("that is this runner's own fingerprint: approve or revoke it on the other runners (or rotate this runner's key)")
	}
	peers, _ := notes.ReadPeers(shared)
	w := c.Out
	if sub == "revoke" && key != "" {
		if _, err := notes.NormalizeFingerprint(fp); err != nil {
			return err
		}
		signer, err := c.rootSigner(key)
		if err != nil {
			return err
		}
		r, err := notes.RevokeWithRoot(shared, signer, fp, now)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "Revoked %s%s with the root key.\n", r.Fingerprint, namesOf(peers, r.Fingerprint))
		fmt.Fprintln(w, "This runner stops trading keys with it now, and rotates the key of each project it held.")
		fmt.Fprintln(w, "The revocation reaches every runner enrolled under the same root as they trade keys")
		fmt.Fprintln(w, "through the hub; no runner trusts that key again. To be sure without the hub, also run")
		fmt.Fprintf(w, "  dash runner notes revoke %s\n", r.Fingerprint)
		fmt.Fprintln(w, "on the others. The revoked runner must rotate its key and be enrolled again to come back.")
		return nil
	}
	if sub == "revoke" {
		fp, err := notes.Revoke(shared, fp, now)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "Revoked %s%s here.\n", fp, namesOf(peers, fp))
		fmt.Fprintln(w, "The running runner stops trading keys with it, and rotates the key of each project it held:")
		fmt.Fprintln(w, "new notes are sealed with a key it doesn't have. This affects this runner only: revoke it on the")
		fmt.Fprintln(w, "project's other runners too, or with the root key from one of them (revoke --key <path>).")
		return nil
	}
	approve := notes.Approve
	if force {
		approve = notes.ApproveOverride
	}
	fp, err = approve(shared, fp, now)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Approved %s%s.\n", fp, namesOf(peers, fp))
	fmt.Fprintln(w, "The running runner trades project keys with it within seconds, once it is approved there too.")
	fmt.Fprintf(w, "If you haven't, approve this runner on it: dash runner notes approve %s\n", id.Fingerprint())
	return nil
}

// runnerID is this runner's id with the hub (a certificate names it).
func (c *CLI) runnerID(data string) (tunnel.Credentials, error) {
	creds, err := tunnel.LoadCredentials(c.credsPath(data))
	if err != nil {
		return creds, fmt.Errorf("this computer isn't registered with a hub yet (dash runner setup), so there is no runner to enroll: %w", err)
	}
	return creds, nil
}

// notesEnroll enrolls this runner under the root key at keyPath.
func (c *CLI) notesEnroll(data, shared, keyPath string, force bool) error {
	creds, err := c.runnerID(data)
	if err != nil {
		return err
	}
	id, err := notes.LoadIdentity(notes.IdentityPath(data))
	if err != nil {
		return err
	}
	ts, _, err := notes.ReadTrustState(shared)
	if err != nil {
		return err
	}
	if _, revoked := ts.RootRevoked(id.Fingerprint()); revoked {
		return fmt.Errorf("this runner's key %s was revoked with the root key: give it a new one and enroll that: dash runner notes rotate --key %s", id.Fingerprint(), keyPath)
	}
	signer, err := c.rootSigner(keyPath)
	if err != nil {
		return err
	}
	return c.enrollWith(shared, signer, creds, id, force)
}

func (c *CLI) enrollWith(shared string, signer ssh.Signer, creds tunnel.Credentials, id *notes.Identity, force bool) error {
	e, err := notes.Enroll(shared, signer, creds.ID, id.Public, force, time.Now())
	if err != nil {
		if errors.Is(err, notes.ErrOtherRoot) {
			return err
		}
		return fmt.Errorf("enroll: %w", err)
	}
	w := c.Out
	fmt.Fprintf(w, "Enrolled %s (key %s) under root key %s.\n", runnerLabel(creds.Name, creds.ID), id.Fingerprint(), notes.RootFingerprint(e.RootKey()))
	fmt.Fprintln(w, "Runners enrolled with the same root key trust it, and it trusts them: no approvals needed.")
	fmt.Fprintln(w, "The running runner picks this up within seconds.")
	if len(e.Dropped) > 0 {
		fmt.Fprintf(w, "No longer trusted (they were by the old root's certificates): %s. They lose the project keys.\n", strings.Join(e.Dropped, ", "))
	}
	fmt.Fprintln(w, "The root key isn't needed here any more: delete it from this computer unless it is the one you keep it on.")
	return nil
}

// notesRotate gives this runner a new notes key, and enrolls it with the
// root key at keyPath (or in ssh-agent) if it can.
func (c *CLI) notesRotate(data, shared, keyPath string, force bool) error {
	path := notes.IdentityPath(data)
	old, err := notes.LoadIdentity(path)
	if err != nil {
		return err
	}
	e, err := notes.ReadEnrollment(shared)
	if err != nil {
		return err
	}
	// The signer first, so a wrong passphrase leaves the key as it was.
	var signer ssh.Signer
	var creds tunnel.Credentials
	if keyPath != "" || e != nil {
		if creds, err = c.runnerID(data); err != nil {
			return err
		}
	}
	if keyPath != "" {
		if signer, err = c.rootSigner(keyPath); err != nil {
			return err
		}
	} else if e != nil {
		if s := c.agentSigner(e.RootKey()); s != nil {
			fmt.Fprintln(c.Out, "Signing with the root key in ssh-agent.")
			signer = s
		}
	}
	id, err := notes.RotateIdentity(path)
	if err != nil {
		return err
	}
	w := c.Out
	fmt.Fprintf(w, "This runner's shared notes key is now %s (was %s).\n", id.Fingerprint(), old.Fingerprint())
	fmt.Fprintln(w, "The running runner switches to it within seconds, and tells its peers.")
	if signer != nil {
		return c.enrollWith(shared, signer, creds, id, force)
	}
	if e != nil {
		fmt.Fprintln(w, "Its certificate is for the old key: until you enroll it again, the other runners refuse the new one:")
		fmt.Fprintln(w, "  dash runner notes enroll --key <path to the root key>")
	} else {
		fmt.Fprintf(w, "On each runner that approved the old key, approve the new one: dash runner notes approve %s\n", id.Fingerprint())
	}
	return nil
}

// namesOf is " (name, id …)" for the runners pinned with fingerprint fp.
func namesOf(p notes.Peers, fp string) string {
	var out []string
	for _, id := range slices.Sorted(maps.Keys(p.Pins)) {
		if pin := p.Pins[id]; pin.Fingerprint == fp {
			out = append(out, runnerLabel(pin.Name, id))
		}
	}
	if len(out) == 0 {
		return ""
	}
	return " (" + strings.Join(out, ", ") + ")"
}

func runnerLabel(name, id string) string {
	if name == "" {
		return "runner " + id
	}
	return name + ", id " + id
}

func (c *CLI) notesShow(data, shared string) error {
	w := c.Out
	id, err := notes.LoadIdentity(notes.IdentityPath(data))
	if err != nil {
		return err
	}
	name, self := "this runner", ""
	if creds, err := tunnel.LoadCredentials(c.credsPath(data)); err == nil {
		name, self = creds.Name, creds.ID
	}
	appr, err := notes.ReadApprovals(shared)
	if err != nil {
		return err
	}
	ts, peers, err := notes.ReadTrustState(shared)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Shared notes key of %s: %s\n", name, id.Fingerprint())
	if root := ts.Root(); root != "" {
		fmt.Fprintf(w, "Enrolled under root key %s (since %s): it trusts the runners enrolled under it.\n",
			root, ts.Enrollment.At.Local().Format("2006-01-02 15:04"))
		if c := ts.Enrollment.Cert; c.Key != id.Public || (self != "" && c.Runner != self) {
			fmt.Fprintln(w, "  !! Its certificate is for another key (rotated since?): the others refuse this one. Enroll again:")
			fmt.Fprintln(w, "     dash runner notes enroll --key <path to the root key>")
		}
		if _, revoked := ts.RootRevoked(id.Fingerprint()); revoked {
			fmt.Fprintln(w, "  !! This runner's key was revoked with the root key: dash runner notes rotate --key <path>")
		}
	} else {
		fmt.Fprintln(w, "Not enrolled with a root key: approve runners pair by pair, or enroll every runner with one key:")
		fmt.Fprintln(w, "  dash runner notes enroll --key ~/.ssh/dash_notes   (make one with: dash runner notes keygen)")
		fmt.Fprintln(w, "  Other runners ask you to approve this fingerprint: check theirs shows exactly this.")
	}

	if len(peers.Pins) > 0 {
		fmt.Fprintln(w, "\nRunners seen here:")
		for _, rid := range slices.Sorted(maps.Keys(peers.Pins)) {
			p := peers.Pins[rid]
			st := ts.Of(rid, p.Key, p.Cert)
			line := string(st)
			if st == notes.OtherRoot {
				line += " " + p.Cert.Root()
			}
			mark := "  "
			if st.Trusted() {
				mark = "✓ "
			}
			fmt.Fprintf(w, "  %s%s  %s: %s\n", mark, p.Fingerprint, runnerLabel(p.Name, rid), line)
		}
	}
	if len(appr.Approved) > 0 {
		fmt.Fprintln(w, "\nApproved here (they get this runner's project keys, and it takes theirs):")
		for _, fp := range slices.Sorted(maps.Keys(appr.Approved)) {
			fmt.Fprintf(w, "  %s%s, approved %s\n", fp, namesOf(peers, fp), appr.Approved[fp].Local().Format("2006-01-02 15:04"))
		}
	}
	var waiting []notes.Pending
	for _, p := range peers.Pending {
		pin, ok := peers.Pins[p.Runner]
		if ok && pin.Fingerprint == p.Fingerprint {
			if st := ts.Of(p.Runner, pin.Key, pin.Cert); st.Trusted() || st == notes.RevokedHere || st == notes.RevokedByRoot {
				continue
			}
		}
		waiting = append(waiting, p)
	}
	if len(waiting) > 0 {
		fmt.Fprintln(w, "\nWaiting:")
		for _, p := range waiting {
			fmt.Fprintf(w, "  %s  %s, project %s, %s ago\n", p.Fingerprint, runnerLabel(p.Name, p.Runner), p.Project, since(p.At))
			if p.OtherRoot != "" {
				if ts.Root() == "" {
					fmt.Fprintf(w, "    It is enrolled under root key %s; this runner isn't. Enroll this one with the same key:\n", p.OtherRoot)
				} else {
					fmt.Fprintf(w, "    It is enrolled under a different root key %s. Enroll both under the same key, or:\n", p.OtherRoot)
				}
				fmt.Fprintln(w, "      dash runner notes enroll --key <path to the root key>")
				continue
			}
			fmt.Fprintf(w, "    On %s, run dash runner notes. If it shows %s, approve it here:\n", cmp.Or(p.Name, "it"), p.Fingerprint)
			fmt.Fprintf(w, "      dash runner notes approve %s\n", p.Fingerprint)
			if ts.Root() != "" {
				fmt.Fprintln(w, "    or enroll it under this runner's root key.")
			}
		}
	}
	if len(peers.Warnings) > 0 {
		fmt.Fprintln(w, "\n!! Runners seen with a key other than the one pinned for them (refused unless approved or certified):")
		for _, k := range peers.Warnings {
			fmt.Fprintf(w, "  %s: pinned %s, offered %s, %s\n", runnerLabel(k.Name, k.Runner), k.Pinned, k.Offered, k.At.Local().Format("2006-01-02 15:04"))
		}
		fmt.Fprintln(w, "  Someone (the hub?) may be posing as it. If it really has a new key, enroll it with the root key, or approve the new fingerprint after checking it there.")
	}
	if len(appr.Revoked) > 0 {
		fmt.Fprintln(w, "\nRevoked here:")
		for _, fp := range slices.Sorted(maps.Keys(appr.Revoked)) {
			fmt.Fprintf(w, "  %s%s, revoked %s\n", fp, namesOf(peers, fp), appr.Revoked[fp].Local().Format("2006-01-02 15:04"))
		}
	}
	if revs := ts.RootRevocations(); len(revs) > 0 {
		fmt.Fprintln(w, "\nRevoked with the root key (on every runner enrolled under it):")
		for _, r := range revs {
			fmt.Fprintf(w, "  %s%s, revoked %s\n", r.Fingerprint, namesOf(peers, r.Fingerprint), r.At.Local().Format("2006-01-02 15:04"))
		}
	}
	return nil
}
