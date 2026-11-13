package notes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// newRoot is a root key, as `dash runner notes keygen` makes one.
func newRoot(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func shared(x *testRunner) string { return filepath.Join(x.dir, "shared") }

// enroll enrolls x under root, as `dash runner notes enroll --key` does.
func enroll(t *testing.T, x *testRunner, root ssh.Signer) {
	t.Helper()
	if _, err := Enroll(shared(x), root, x.id, x.svc.Public(), false, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// settle has every runner act on its trust files and catch up on its
// projects, a few rounds (as their loops do).
func (n *testNet) settle(xs ...*testRunner) {
	n.t.Helper()
	ctx := context.Background()
	for range 3 {
		for _, x := range xs {
			if !x.online {
				continue
			}
			x.svc.Reconcile(ctx, x.projects)
			for _, p := range x.projects {
				if err := x.svc.CatchUp(ctx, p); err != nil {
					n.t.Fatal(err)
				}
			}
		}
	}
}

// keyIDs is x's project keys of project.
func keyIDs(t *testing.T, x *testRunner, project string) []string {
	t.Helper()
	st, err := x.svc.store(project)
	if err != nil {
		t.Fatal(err)
	}
	ks, err := st.Keys(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, k := range ks {
		ids = append(ids, k.ID)
	}
	slices.Sort(ids)
	return ids
}

// shareKey reports whether x and y hold a project key of project in common.
func shareKey(t *testing.T, x, y *testRunner, project string) bool {
	ky := keyIDs(t, y, project)
	return slices.ContainsFunc(keyIDs(t, x, project), func(id string) bool { return slices.Contains(ky, id) })
}

func standing(x *testRunner, runner string, key PublicKey) Standing {
	x.svc.trust.reload()
	x.svc.trust.mu.Lock()
	defer x.svc.trust.mu.Unlock()
	return x.svc.trust.standingLocked(runner, key)
}

// Two or three runners enrolled with the same root key trust each other
// with no approval anywhere. They were running before they were enrolled:
// enrolling takes effect without a restart.
func TestEnrolledRunnersTrustEachOther(t *testing.T) {
	for _, count := range []int{2, 3} {
		t.Run(string(rune('0'+count))+" runners", func(t *testing.T) {
			n := newNet(t)
			root := newRoot(t)
			var xs []*testRunner
			for i := range count {
				xs = append(xs, n.add("r"+string(rune('a'+i))))
			}
			ctx := context.Background()
			if _, err := xs[0].svc.Put(ctx, "p1", Draft{Key: "build", Body: "make all"}); err != nil {
				t.Fatal(err)
			}
			n.settle(xs...)
			if got := keys(t, xs[1]); len(got) != 0 {
				t.Fatalf("read notes before enrolling: %v", got)
			}
			for _, x := range xs {
				enroll(t, x, root)
			}
			n.settle(xs...)
			for _, x := range xs {
				if got := keys(t, x); !slices.Equal(got, []string{"build=make all"}) {
					t.Fatalf("%s: %v", x.id, got)
				}
				for _, y := range xs {
					if x != y && standing(x, y.id, y.svc.Public()) != Certified {
						t.Fatalf("%s stands to %s as %q", x.id, y.id, standing(x, y.id, y.svc.Public()))
					}
				}
				a, _ := ReadApprovals(shared(x))
				if len(a.Approved) != 0 {
					t.Fatalf("approvals were needed: %+v", a)
				}
				st, err := x.svc.Status("p1")
				if err != nil || st.Root != RootFingerprint(root.PublicKey()) || !st.Certified {
					t.Fatalf("status: %+v %v", st, err)
				}
			}
		})
	}
}

// A runner enrolled under another root isn't trusted, and the runner says
// why: not "approve it", but that it has a different root.
func TestOtherRootIsNotTrusted(t *testing.T) {
	n := newNet(t)
	root, other := newRoot(t), newRoot(t)
	a, b, c := n.add("ra"), n.add("rb"), n.add("rc")
	enroll(t, a, root)
	enroll(t, b, root)
	enroll(t, c, other)
	n.settle(a, b, c)
	if !shareKey(t, a, b, "p1") {
		t.Fatal("a and b share no key")
	}
	if shareKey(t, a, c, "p1") || shareKey(t, b, c, "p1") {
		t.Fatal("c got a key from another root's runners")
	}
	if s := standing(a, "rc", c.svc.Public()); s != OtherRoot {
		t.Fatalf("a stands to c as %q", s)
	}
	why := a.svc.trust.why("rc", c.svc.Public())
	if !strings.Contains(why, "different root key "+RootFingerprint(other.PublicKey())) {
		t.Fatalf("why: %s", why)
	}
	st, _ := a.svc.Status("p1")
	if len(st.Waiting) != 1 || st.Waiting[0].OtherRoot != RootFingerprint(other.PublicKey()) {
		t.Fatalf("waiting: %+v", st.Waiting)
	}
	// Approving it by hand still works, as for any runner.
	approve(t, a, c)
	approve(t, c, a)
	n.settle(a, c)
	if !shareKey(t, a, c, "p1") {
		t.Fatal("manual approval across roots doesn't work")
	}
}

// What the hub could do with certificates and revocations it relays, and
// why none of it works.
func TestHubCantForgeEnrollment(t *testing.T) {
	n := newNet(t)
	root := newRoot(t)
	a, b := n.add("ra"), n.add("rb")
	enroll(t, a, root)
	enroll(t, b, root)
	n.settle(a, b)
	be, _ := ReadEnrollment(shared(b))
	bcert := be.Cert
	hub := mustIdentity(t)
	hubRoot := newRoot(t)

	refused := func(what, runner string, key PublicKey, cert Cert) {
		t.Helper()
		_ = a.svc.trust.see(runner, "", key, &cert)
		if a.svc.trust.trusted(runner, key) {
			t.Fatalf("%s: trusted", what)
		}
	}
	// A certificate from the hub's own key.
	forged, err := IssueCert(hubRoot, "rx", hub.Public, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	refused("the hub's own certificate", "rx", hub.Public, forged)
	if s := standing(a, "rx", hub.Public); s != OtherRoot {
		t.Fatalf("a forged root shows as %q", s)
	}
	// b's certificate with the hub's key, or for another runner id.
	refused("b's certificate replayed with another key", "ry", hub.Public, bcert)
	swapped := bcert
	swapped.Key = hub.Public
	refused("b's certificate edited to the hub's key", "rb2", hub.Public, swapped)
	refused("b's certificate for another runner id", "rz", b.svc.Public(), bcert)
	renamed := bcert
	renamed.Runner = "rz2"
	refused("b's certificate edited to another runner id", "rz2", b.svc.Public(), renamed)
	// A certificate with the root's public key in it but a made-up
	// signature.
	later := bcert
	later.Issued = later.Issued.Add(time.Second)
	refused("an edited certificate", "rb3", b.svc.Public(), later)
	garbled := bcert
	garbled.Sig = strings.Replace(garbled.Sig, "A", "B", 1)
	refused("a garbled signature", "rb4", b.svc.Public(), garbled)
	// b's key with b's certificate, under b's id, but the hub got there
	// first with its own key for "rb": the pin stays, the cert doesn't move it.
	if err := a.svc.trust.see("rb", "", hub.Public, &bcert); !errors.Is(err, ErrKeyChanged) {
		t.Fatalf("the hub's key for b with b's certificate: %v", err)
	}

	// Revocations: only the root's count.
	learned := func(what string, r Revocation) {
		t.Helper()
		if a.svc.trust.learn([]Revocation{r}) {
			t.Fatalf("%s: learned", what)
		}
		if standing(a, "rb", b.svc.Public()) != Certified {
			t.Fatalf("%s: b's standing changed", what)
		}
	}
	rogue, _ := IssueRevocation(hubRoot, b.svc.Fingerprint(), time.Now())
	learned("a revocation signed by the hub", rogue)
	// A compromised runner's own key (ed25519, as a notes key can't sign
	// at all) is no root either.
	runnerKey := newRoot(t)
	byRunner, _ := IssueRevocation(runnerKey, b.svc.Fingerprint(), time.Now())
	learned("a revocation signed by a runner", byRunner)
	// A real revocation, of another key, edited to b's.
	real, _ := IssueRevocation(root, mustIdentity(t).Fingerprint(), time.Now())
	edited := real
	edited.Fingerprint = b.svc.Fingerprint()
	learned("a root revocation edited to b's fingerprint", edited)
	// b's certificate's signature passed off as a revocation.
	learned("a certificate's signature as a revocation", Revocation{Fingerprint: b.svc.Fingerprint(), At: bcert.Issued, Sig: bcert.Sig})
	if !a.svc.trust.learn([]Revocation{real}) {
		t.Fatal("a real root revocation isn't learned")
	}

	// End to end: the hub swaps its key into a new runner's hello, keeping
	// the runner's certificate. Nobody wraps a key for it.
	c := n.add("rc")
	enroll(t, c, root)
	n.mu.Lock()
	n.tamper = func(peer string, msg []byte) []byte {
		var env envelope
		if json.Unmarshal(msg, &env) != nil || env.Hello == nil || env.Hello.Runner != "rc" {
			return msg
		}
		env.Hello.Key = hub.Public
		return encode(env)
	}
	n.mu.Unlock()
	c.svc.Reconcile(context.Background(), c.projects)
	if err := c.svc.CatchUp(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, m := range n.seen {
		var env envelope
		json.Unmarshal(m, &env)
		for _, w := range env.Wraps {
			if w.ToKey == hub.Public {
				t.Fatal("a key was wrapped for the hub's key")
			}
		}
	}
}

// A revocation signed with the root key reaches every runner under that
// root, even one that shares no project with the runner where the user
// revoked; it beats the revoked runner's certificate, and lasts.
func TestRootRevocationPropagates(t *testing.T) {
	n := newNet(t)
	root := newRoot(t)
	r1 := n.addIn("r1", "p1")
	r2 := n.addIn("r2", "p1", "p2")
	r3 := n.addIn("r3", "p2") // shares no project with r1
	v := n.addIn("v", "p1", "p2")
	all := []*testRunner{r1, r2, r3, v}
	for _, x := range all {
		enroll(t, x, root)
	}
	n.settle(all...)
	if !shareKey(t, r1, v, "p1") || !shareKey(t, r3, v, "p2") {
		t.Fatal("v didn't get the keys")
	}
	oldKey, oldFP := v.svc.Public(), v.svc.Fingerprint()
	ve, _ := ReadEnrollment(shared(v))
	oldCert := ve.Cert

	if _, err := RevokeWithRoot(shared(r1), root, oldFP, time.Now()); err != nil {
		t.Fatal(err)
	}
	// v is offline meanwhile, so it can't take part.
	n.setOnline(v, false)
	n.settle(r1, r2, r3)
	for _, x := range []*testRunner{r1, r2, r3} {
		if s := standing(x, "v", oldKey); s != RevokedByRoot {
			t.Fatalf("%s stands to v as %q", x.id, s)
		}
	}
	n.setOnline(v, true)
	n.settle(all...)
	if shareKey(t, r1, v, "p1") || shareKey(t, r3, v, "p2") || shareKey(t, r2, v, "p2") {
		t.Fatal("the revoked runner holds a current key")
	}
	// It lasts across a restart, and approving the key by hand is refused.
	r3 = n.restart(r3)
	all = []*testRunner{r1, r2, r3, v}
	n.settle(all...)
	if s := standing(r3, "v", oldKey); s != RevokedByRoot {
		t.Fatalf("after a restart r3 stands to v as %q", s)
	}
	if shareKey(t, r3, v, "p2") {
		t.Fatal("v got r3's key after r3 restarted")
	}
	if _, err := Approve(shared(r3), oldFP, time.Now()); !errors.Is(err, ErrRootRevoked) {
		t.Fatalf("approving a root-revoked key: %v", err)
	}

	// v rotates its key and enrolls again: it is trusted with the new key,
	// the old one stays revoked.
	if _, err := RotateIdentity(filepath.Join(v.dir, "identity.json")); err != nil {
		t.Fatal(err)
	}
	v.svc.Reconcile(context.Background(), v.projects) // the running runner switches
	if v.svc.Public() == oldKey {
		t.Fatal("the running runner kept its old key")
	}
	enroll(t, v, root)
	n.settle(all...)
	n.settle(all...)
	if !shareKey(t, r1, v, "p1") || !shareKey(t, r3, v, "p2") {
		t.Fatal("v, rotated and enrolled again, didn't get the keys")
	}
	for _, x := range []*testRunner{r1, r2, r3} {
		if s := standing(x, "v", v.svc.Public()); s != Certified {
			t.Fatalf("%s stands to v's new key as %q", x.id, s)
		}
		ts, _, _ := ReadTrustState(shared(x))
		if _, ok := ts.RootRevoked(oldFP); !ok || ts.Of("v", oldKey, &oldCert) != RevokedByRoot {
			t.Fatalf("%s forgot the old key's revocation", x.id)
		}
	}
}

// Enrolling under another root needs --force, and drops what the old
// root's certificates trusted; manual approvals stay.
func TestReenrollUnderAnotherRoot(t *testing.T) {
	n := newNet(t)
	root, other := newRoot(t), newRoot(t)
	a, b, c := n.add("ra"), n.add("rb"), n.add("rc")
	enroll(t, a, root)
	enroll(t, b, root)
	approve(t, a, c)
	approve(t, c, a)
	n.settle(a, b, c)
	if !shareKey(t, a, b, "p1") || !shareKey(t, a, c, "p1") {
		t.Fatal("not shared before")
	}
	if _, err := Enroll(shared(a), other, "ra", a.svc.Public(), false, time.Now()); !errors.Is(err, ErrOtherRoot) {
		t.Fatalf("re-enrolling without --force: %v", err)
	}
	e, err := Enroll(shared(a), other, "ra", a.svc.Public(), true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.Dropped, []string{"rb"}) {
		t.Fatalf("dropped %v", e.Dropped)
	}
	n.settle(a, b, c)
	if s := standing(a, "rb", b.svc.Public()); s != OtherRoot {
		t.Fatalf("a stands to b as %q", s)
	}
	if shareKey(t, a, b, "p1") {
		t.Fatal("b holds a's key after a moved to another root")
	}
	if s := standing(a, "rc", c.svc.Public()); s != Approved || !shareKey(t, a, c, "p1") {
		t.Fatalf("the manual approval of c didn't stay: %q", s)
	}

	// Enrolling again under the same root (after rotate --key, say) keeps
	// Dropped, so the keys b may still hold keep being rotated.
	if _, err := Enroll(shared(a), other, "ra", a.svc.Public(), false, time.Now()); err != nil {
		t.Fatal(err)
	}
	re, err := ReadEnrollment(shared(a))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(re.Dropped, []string{"rb"}) {
		t.Fatalf("re-enrolling under the same root: dropped %v", re.Dropped)
	}
	if r := a.svc.trust.revokedRunners(); !slices.Contains(r, "rb") {
		t.Fatalf("revoked runners %v, want rb among them", r)
	}
}

// Signatures are SSHSIG: ssh-keygen checks ours, and ours check its.
func TestSSHSIGMatchesSSHKeygen(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	dir := t.TempDir()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	block, err := ssh.MarshalPrivateKey(priv, "test")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "k")
	os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600)
	msg := []byte("dash-notes-revoke-v1\nfingerprint X\n")

	sig, err := sshsign(signer, RevokeNamespace, msg)
	if err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(dir, "sig")
	os.WriteFile(sigPath, []byte(sig), 0o600)
	cmd := exec.Command("ssh-keygen", "-Y", "check-novalidate", "-n", RevokeNamespace, "-s", sigPath)
	cmd.Stdin = bytes.NewReader(msg)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen rejects our signature: %v %s", err, out)
	}

	msgPath := filepath.Join(dir, "msg")
	os.WriteFile(msgPath, msg, 0o600)
	if out, err := exec.Command("ssh-keygen", "-Y", "sign", "-f", keyPath, "-n", EnrollNamespace, msgPath).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen -Y sign: %v %s", err, out)
	}
	theirs, _ := os.ReadFile(msgPath + ".sig")
	pub, err := sshverify(string(theirs), EnrollNamespace, msg)
	if err != nil || !sameKey(pub, signer.PublicKey()) {
		t.Fatalf("ssh-keygen's signature: %v", err)
	}
	if _, err := sshverify(string(theirs), RevokeNamespace, msg); err == nil {
		t.Fatal("a signature checked out in another namespace")
	}
}
