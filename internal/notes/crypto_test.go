package notes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

func mustIdentity(t *testing.T) *Identity {
	t.Helper()
	id, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// A runner's identity is made once and read back the same; its file is
// for this user only.
func TestIdentityIsKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes-identity.json")
	a, err := LoadIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadIdentity(path)
	if err != nil || b.Public != a.Public || b.private != a.private {
		t.Fatalf("reloaded a different identity: %v", err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("identity file mode: %v %v", fi.Mode(), err)
	}
	// A file whose public key isn't its private key's is refused.
	raw, _ := os.ReadFile(path)
	var f map[string]any
	json.Unmarshal(raw, &f)
	f["public"] = mustIdentity(t).Public.String()
	raw, _ = json.Marshal(f)
	os.WriteFile(path, raw, 0o600)
	if _, err := ReadIdentity(path); err == nil {
		t.Fatal("read an identity with a foreign public key")
	}
}

func TestFingerprints(t *testing.T) {
	id := mustIdentity(t)
	fp := id.Fingerprint()
	if len(fp) != 24 || fp[4] != '-' {
		t.Fatalf("fingerprint %q", fp)
	}
	for _, in := range []string{fp, " " + toLower(fp) + " ", stripDashes(fp)} {
		if got, err := NormalizeFingerprint(in); err != nil || got != fp {
			t.Fatalf("normalize %q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "ABCD", fp + "A", "ABCD-EFGH-IJKL-MNOP-QRS1"} {
		if _, err := NormalizeFingerprint(bad); !errors.Is(err, ErrBadFP) {
			t.Fatalf("normalize %q: %v", bad, err)
		}
	}
	if mustIdentity(t).Fingerprint() == fp {
		t.Fatal("two keys, one fingerprint")
	}
}

func toLower(s string) string {
	return string(bytes.ToLower([]byte(s)))
}

func stripDashes(s string) string {
	return string(bytes.ReplaceAll([]byte(s), []byte("-"), nil))
}

// Keys wrapped for a runner open for it only, from the sender it names,
// for the project they were wrapped for; anything tampered with is refused.
func TestWrapUnwrap(t *testing.T) {
	alice, bob, eve := mustIdentity(t), mustIdentity(t), mustIdentity(t)
	k, err := newProjectKey(3, t0)
	if err != nil {
		t.Fatal(err)
	}
	ks := keyset{Project: "p1", Keys: []projectKey{k}, Holders: []string{"ra"}}
	w, err := alice.wrap("ra", "rb", bob.Public, ks)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(w.Box, k.Secret) {
		t.Fatal("the key travels in the clear")
	}
	got, err := bob.unwrap("rb", "p1", w)
	if err != nil || len(got.Keys) != 1 || !bytes.Equal(got.Keys[0].Secret, k.Secret) || got.Keys[0].Epoch != 3 {
		t.Fatalf("bob unwraps: %+v %v", got, err)
	}

	// Not for eve, not as another runner, not into another project.
	if _, err := eve.unwrap("rb", "p1", w); err == nil {
		t.Fatal("eve opened bob's key")
	}
	ew := w
	ew.ToKey = eve.Public
	if _, err := eve.unwrap("rb", "p1", ew); err == nil {
		t.Fatal("eve opened bob's key by relabelling it")
	}
	if _, err := bob.unwrap("rc", "p1", w); err == nil {
		t.Fatal("unwrapped as another runner")
	}
	if _, err := bob.unwrap("rb", "p2", w); err == nil {
		t.Fatal("a wrap for p1 opened for p2")
	}
	// A hub claiming the wrap came from someone else, or flipping a bit.
	forged := w
	forged.FromKey = eve.Public
	if _, err := bob.unwrap("rb", "p1", forged); err == nil {
		t.Fatal("a wrap opened under a sender that didn't make it")
	}
	flipped := w
	flipped.Box = slices.Clone(w.Box)
	flipped.Box[len(flipped.Box)-1] ^= 1
	if _, err := bob.unwrap("rb", "p1", flipped); err == nil {
		t.Fatal("a tampered wrap opened")
	}
}

func TestSealOpen(t *testing.T) {
	k1, _ := newProjectKey(1, t0)
	k2, _ := newProjectKey(2, t0)
	sl, err := seal(k1, []byte("hello notes"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sl.Box, []byte("hello")) {
		t.Fatal("plaintext in the box")
	}
	if got, err := open([]projectKey{k2, k1}, sl); err != nil || string(got) != "hello notes" {
		t.Fatalf("open: %q %v", got, err)
	}
	if _, err := open([]projectKey{k2}, sl); !errors.Is(err, ErrNoProjectKey) {
		t.Fatalf("opened without its key: %v", err)
	}
	sl.Box[0] ^= 1
	if _, err := open([]projectKey{k1}, sl); err == nil {
		t.Fatal("a tampered message opened")
	}
}

// A later epoch replaces the keys, the same epoch joins them, an older
// one is ignored; the sealing key is the same on every runner.
func TestKeyringEpochs(t *testing.T) {
	st := openStore(t, "ra")
	mine, err := st.Keys(t0)
	if err != nil || len(mine) != 1 || mine[0].Epoch != 1 {
		t.Fatalf("first key: %+v %v", mine, err)
	}
	theirs, _ := newProjectKey(1, t0)
	if added, err := st.AddKeys([]projectKey{theirs}, []string{"rb"}); !added || err != nil {
		t.Fatalf("same epoch: %v %v", added, err)
	}
	keys, _ := st.Keys(t0)
	if len(keys) != 2 {
		t.Fatalf("keys %d", len(keys))
	}
	want, _ := sealing([]projectKey{mine[0], theirs})
	if got, _ := sealing(keys); got.ID != want.ID {
		t.Fatal("the sealing key depends on the order keys came in")
	}
	k2, _ := st.Rotate(t0)
	if added, _ := st.AddKeys([]projectKey{theirs}, nil); added {
		t.Fatal("an older epoch's key was taken after a rotation")
	}
	keys, _ = st.Keys(t0)
	if len(keys) != 1 || keys[0].ID != k2.ID || k2.Epoch != 2 {
		t.Fatalf("after rotating: %+v", keys)
	}
	if h, _ := st.Holders(); !reflect.DeepEqual(h, []string{"ra"}) {
		t.Fatalf("holders of a fresh key: %v", h)
	}
}

// ---- runners trading keys through the relay ----

// projectKeys is x's project keys (ids, sorted) and their epoch.
func projectKeys(t *testing.T, x *testRunner) (ids []string, epoch uint64) {
	t.Helper()
	st, err := x.svc.store("p1")
	if err != nil {
		t.Fatal(err)
	}
	ks, err := st.Keys(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range ks {
		ids = append(ids, k.ID)
		epoch = k.Epoch
	}
	slices.Sort(ids)
	return ids, epoch
}

// A runner the user hasn't approved gets no key, and so reads nothing;
// it is listed as waiting, with its fingerprint. Once approved (on both
// sides) the runners trade keys and sync their notes.
func TestUnapprovedRunnerGetsNoKey(t *testing.T) {
	n := newNet(t)
	a, b := n.add("ra"), n.add("rb")
	ctx := context.Background()
	if _, err := a.svc.Put(ctx, "p1", Draft{Key: "from a", Body: "secret plan"}); err != nil {
		t.Fatal(err)
	}
	// b trusts a, but a doesn't know b's key yet.
	approve(t, b, a)
	if err := b.svc.CatchUp(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if got := keys(t, b); len(got) != 0 {
		t.Fatalf("an unapproved runner read notes: %v", got)
	}
	if ka, _ := projectKeys(t, a); slices.ContainsFunc(ka, func(id string) bool { kb, _ := projectKeys(t, b); return slices.Contains(kb, id) }) {
		t.Fatal("a and b share a key without a approving b")
	}
	p, err := ReadPeers(filepath.Join(a.dir, "shared"))
	if err != nil || len(p.Pending) != 1 || p.Pending[0].Runner != "rb" || p.Pending[0].Fingerprint != b.svc.Fingerprint() {
		t.Fatalf("a's pending: %+v %v", p.Pending, err)
	}
	// Approving some other key doesn't help b.
	if _, err := Approve(filepath.Join(a.dir, "shared"), mustIdentity(t).Fingerprint(), time.Now()); err != nil {
		t.Fatal(err)
	}
	a.svc.Reconcile(ctx, []string{"p1"})
	if got := keys(t, b); len(got) != 0 {
		t.Fatalf("b read notes after a approved another key: %v", got)
	}

	// The user compares fingerprints and approves b on a.
	approve(t, a, b)
	a.svc.Reconcile(ctx, []string{"p1"})
	eventually(t, "b to get a's notes", func() bool { return slices.Equal(keys(t, b), []string{"from a=secret plan"}) })
	ka, _ := projectKeys(t, a)
	kb, _ := projectKeys(t, b)
	if !slices.Equal(ka, kb) || len(ka) != 2 {
		t.Fatalf("keys after pairing: a %v, b %v", ka, kb)
	}
	if p, _ := ReadPeers(filepath.Join(a.dir, "shared")); len(p.Pending) != 0 {
		t.Fatalf("still pending: %+v", p.Pending)
	}
}

// A lying hub can't slip in its own key: a key posing as an approved
// runner's is refused and warned about, keys from a runner nobody approved
// are refused, and messages sealed with them don't open.
func TestHubCantSlipInItsOwnKey(t *testing.T) {
	n := newNet(t)
	a, b := n.add("ra"), n.add("rb")
	ctx := context.Background()
	n.pair(a, b)
	before, _ := projectKeys(t, a)

	// The hub swaps b's key for its own in b's hello.
	hub := mustIdentity(t)
	n.mu.Lock()
	n.tamper = func(peer string, msg []byte) []byte {
		var env envelope
		if json.Unmarshal(msg, &env) == nil && env.Hello != nil && env.Hello.Runner == "rb" {
			env.Hello.Key = hub.Public
			env.Hello.Have = nil
			return encode(env)
		}
		return msg
	}
	n.mu.Unlock()
	if err := b.svc.CatchUp(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	p, _ := ReadPeers(filepath.Join(a.dir, "shared"))
	if len(p.Warnings) != 1 || p.Warnings[0].Runner != "rb" || p.Warnings[0].Offered != hub.Fingerprint() || p.Warnings[0].Pinned != b.svc.Fingerprint() {
		t.Fatalf("no warning of b's changed key: %+v", p.Warnings)
	}
	if pin, _ := a.svc.trust.pinned("rb"); pin != b.svc.id.Public {
		t.Fatal("the hub's key replaced b's pin")
	}
	// Nothing came back wrapped for the hub's key.
	for _, m := range n.seen {
		var env envelope
		if json.Unmarshal(m, &env) == nil {
			for _, w := range env.Wraps {
				if w.ToKey == hub.Public {
					t.Fatal("a wrapped its key for the hub")
				}
			}
		}
	}

	// The hub, as a runner of its own, hands a a key: refused.
	n.mu.Lock()
	n.tamper = nil
	n.mu.Unlock()
	evil, _ := newProjectKey(99, time.Now())
	w, err := hub.wrap("hub-runner", "ra", a.svc.id.Public, keyset{Project: "p1", Keys: []projectKey{evil}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.receive(ctx, "p1", encode(envelope{V: envelopeV, Wraps: []wrapped{w}})); err != nil {
		t.Fatal(err)
	}
	if after, _ := projectKeys(t, a); !slices.Equal(after, before) {
		t.Fatalf("a took the hub's key: %v → %v", before, after)
	}
	// …and notes sealed with it don't open.
	sl, _ := seal(evil, encode(message{Kind: kindEntries, Entries: []Entry{{ID: "x:1", Key: "planted", Writer: "x", Seq: 1, Clock: 1}}}))
	if _, err := a.svc.receive(ctx, "p1", encode(envelope{V: envelopeV, Sealed: &sl})); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(keys(t, a), func(k string) bool { return k == "planted=" }) {
		t.Fatal("a note sealed with the hub's key was taken")
	}
	// Unsealed notes, as before encryption, are refused outright.
	if _, err := a.svc.receive(ctx, "p1", []byte(`{"kind":"entries","entries":[{"id":"x:1","key":"planted","writer":"x","seq":1,"clock":1}]}`)); !errors.Is(err, ErrBadEntry) {
		t.Fatalf("unsealed message: %v", err)
	}

	// A runner that really got a new key is taken once the user approves
	// the new fingerprint on a.
	approve(t, a, &testRunner{svc: &Service{id: hub}})
	if err := a.svc.trust.see("rb", "rb", hub.Public, nil); err != nil {
		t.Fatalf("an approved new key is still refused: %v", err)
	}
}

// When a runner leaves the project, the remaining ones rotate to a new key
// it doesn't have; notes sealed with the old key are refused from then on.
// Revoking a runner's key on a runner (dash runner notes revoke) rotates
// too, even if the hub still lists it.
func TestRotationAfterRevocation(t *testing.T) {
	n := newNet(t)
	a, b, c := n.add("ra"), n.add("rb"), n.add("rc")
	ctx := context.Background()
	n.pair(a, b, c)
	if _, err := a.svc.Put(ctx, "p1", Draft{Key: "before", Body: "all three"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "c to read the first note", func() bool { return slices.Equal(keys(t, c), []string{"before=all three"}) })
	old, epoch := projectKeys(t, c)
	if epoch != 1 {
		t.Fatalf("epoch %d", epoch)
	}
	// c keeps a message sealed with the key it holds, for later.
	cst, _ := c.svc.store("p1")
	stale, err := c.svc.sealMessage(cst, message{Kind: kindEntries, Entries: []Entry{{ID: "rc:99", Key: "from the past", Writer: "rc", Seq: 99, Clock: 99}}})
	if err != nil {
		t.Fatal(err)
	}

	// The hub unlinks c from the project. The next write tells a so; it
	// rotates, and b gets the new key from it.
	n.mu.Lock()
	c.member = false
	n.mu.Unlock()
	if _, err := a.svc.Put(ctx, "p1", Draft{Key: "after", Body: "a and b only"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a and b to share a new key", func() bool {
		ka, ea := projectKeys(t, a)
		kb, eb := projectKeys(t, b)
		return ea == 2 && eb == 2 && slices.Equal(ka, kb)
	})
	newKeys, _ := projectKeys(t, a)
	if slices.ContainsFunc(newKeys, func(id string) bool { return slices.Contains(old, id) }) {
		t.Fatal("the new key is an old one")
	}
	if _, err := b.svc.Put(ctx, "p1", Draft{Key: "later", Body: "sealed anew"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a to read b's note", func() bool { return len(keys(t, a)) == 3 })
	// c can't open what is sealed now…
	akeys, _ := a.svc.store("p1")
	now, _ := a.svc.sealMessage(akeys, message{Kind: kindWant})
	var env envelope
	json.Unmarshal(now, &env)
	if _, err := c.svc.openSealed(cst, *env.Sealed); !errors.Is(err, ErrNoProjectKey) {
		t.Fatalf("c opened a message sealed after it left: %v", err)
	}
	// …and what it sealed with the old key is refused.
	if _, err := a.svc.receive(ctx, "p1", stale); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(keys(t, a), func(k string) bool { return k == "from the past=" }) {
		t.Fatal("a took a note sealed with the old key")
	}

	// Revocation by the user: b revokes a's key (the hub lists a still).
	if _, err := Revoke(filepath.Join(b.dir, "shared"), a.svc.Fingerprint(), time.Now()); err != nil {
		t.Fatal(err)
	}
	b.svc.Reconcile(ctx, []string{"p1"})
	_, eb := projectKeys(t, b)
	if eb != 3 {
		t.Fatalf("b didn't rotate when a was revoked: epoch %d", eb)
	}
	kb, _ := projectKeys(t, b)
	ka, _ := projectKeys(t, a)
	if slices.ContainsFunc(kb, func(id string) bool { return slices.Contains(ka, id) }) {
		t.Fatal("the revoked runner holds b's new key")
	}
	// It stays so: a's next catch-up gets it nothing.
	if err := a.svc.CatchUp(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	ka, _ = projectKeys(t, a)
	if slices.ContainsFunc(kb, func(id string) bool { return slices.Contains(ka, id) }) {
		t.Fatal("the revoked runner got the new key on catching up")
	}
}

// Everything the relay carries is sealed: no message or reply it passes on
// holds a word of the notes, though the notes get through.
func TestRelaySeesNoPlaintext(t *testing.T) {
	n := newNet(t)
	a, b, c := n.add("ra"), n.add("rb"), n.add("rc")
	ctx := context.Background()
	n.setOnline(c, false)
	n.pair(a, b, c)
	const marker = "zq-plain-7f3a"
	e, err := a.svc.Put(ctx, "p1", Draft{Key: "how to build " + marker, Body: "make " + marker, Tags: []string{"tag-" + marker}, AuthorTask: "task-" + marker})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.svc.Put(ctx, "p1", Draft{ID: e.ID, Key: "how to build " + marker, Body: "make test " + marker}); err != nil {
		t.Fatal(err)
	}
	n.setOnline(c, true)
	if err := c.svc.CatchUp(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	for _, x := range []*testRunner{a, b, c} {
		if got := keys(t, x); !slices.Equal(got, []string{"how to build " + marker + "=make test " + marker}) {
			t.Fatalf("%s: %v", x.id, got)
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.seen) == 0 {
		t.Fatal("the relay carried nothing")
	}
	for _, m := range n.seen {
		if len(m) == 0 {
			continue // an empty reply
		}
		if bytes.Contains(m, []byte(marker)) || bytes.Contains(m, []byte("how to build")) {
			t.Fatalf("plaintext through the relay: %s", m)
		}
		// No project key either: every key in a message is wrapped.
		var env envelope
		if err := json.Unmarshal(m, &env); err != nil || env.V != envelopeV {
			t.Fatalf("not an envelope: %s", m)
		}
		for _, x := range []*testRunner{a, b, c} {
			st, _ := x.svc.store("p1")
			ks, _ := st.Keys(time.Now())
			for _, k := range ks {
				if bytes.Contains(m, k.Secret) || bytes.Contains(m, []byte(encodeB64(k.Secret))) {
					t.Fatal("a project key through the relay in the clear")
				}
			}
		}
	}
}

func encodeB64(b []byte) string {
	s, _ := json.Marshal(b)
	return string(s[1 : len(s)-1])
}
