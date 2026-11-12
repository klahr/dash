package notes

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const day = 24 * time.Hour

// fakeClock is the time of a test's runners.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

func tidy(t *testing.T, s *Store, now time.Time) TidyReport {
	t.Helper()
	r, err := s.Tidy(now)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func deliver(t *testing.T, to *Store, writes []Entry) {
	t.Helper()
	if _, _, err := to.Apply(slices.Clone(writes)); err != nil {
		t.Fatal(err)
	}
}

// syncAll gives b everything a has and a everything b has.
func syncAll(t *testing.T, a, b *Store, now time.Time) {
	t.Helper()
	for _, p := range [][2]*Store{{a, b}, {b, a}} {
		v, _ := p[1].Vector()
		es, pv, err := p[0].Since(v, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p[1].Merge(es, pv); err != nil {
			t.Fatal(err)
		}
	}
}

func liveKeys(t *testing.T, s *Store, now time.Time) []string {
	t.Helper()
	es, err := s.List(Query{}, now)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Key)
	}
	slices.Sort(out)
	return out
}

// Two runners holding the same notes tidy them, at different moments (a
// note expires in between), each on its own: once their tidying writes
// reach each other, the copies are the same, and tidying again changes
// nothing.
func TestTidyingConverges(t *testing.T) {
	a, b := openStore(t, "ra"), openStore(t, "rb")
	p := Policy{MaxEntries: 8}
	a.SetPolicy(p)
	b.SetPolicy(p)
	at := func(i int) time.Time { return t0.Add(time.Duration(i) * time.Minute) }
	for i := range 12 {
		d := Draft{Key: fmt.Sprint("note ", i), Body: strings.Repeat("x", i)}
		switch i {
		case 0, 5:
			d.Permanent = true
		case 3:
			d.TTL = 90 * time.Minute // expires between the two tidyings
		}
		w := a
		if i%2 == 1 {
			w = b
		}
		if _, err := w.Write(d, at(i)); err != nil {
			t.Fatal(err)
		}
	}
	// The same title, created on both before they heard of each other.
	a.Write(Draft{Key: "Build", Body: "from a"}, at(20))
	b.Write(Draft{Key: "build ", Body: "from b"}, at(21))
	// A note deleted long ago: its tombstone goes past the retention window.
	old, _ := a.Write(Draft{Key: "old"}, t0.Add(-90*day))
	a.Delete(old.ID, "", t0.Add(-70*day))
	syncAll(t, a, b, at(30))
	sameCopies(t, a, b)

	ra := tidy(t, a, at(80))
	rb := tidy(t, b, at(100))
	if ra.Purged != 1 || ra.Merged != 1 || ra.Evicted == 0 || rb.Expired != 1 {
		t.Fatalf("a: %+v\nb: %+v", ra, rb)
	}
	deliver(t, b, ra.Writes)
	deliver(t, a, rb.Writes)
	ra, rb = tidy(t, a, at(100)), tidy(t, b, at(100))
	if len(ra.Writes)+len(rb.Writes) != 0 {
		t.Fatalf("tidying again wrote: %+v %+v", ra.Writes, rb.Writes)
	}
	sameCopies(t, a, b)
	keys := liveKeys(t, a, at(100))
	if len(keys) > p.MaxEntries || !slices.Contains(keys, "note 0") || !slices.Contains(keys, "note 5") {
		t.Fatalf("live after tidying: %v", keys)
	}
	if n := len(sameTitle(func() []Entry { es, _ := a.List(Query{}, at(100)); return es }(), "build")); n != 1 {
		t.Fatalf("%d notes titled build", n)
	}
}

// Past a cap, the notes updated longest ago go first, by count or by
// size; permanent ones never. A project full of permanent notes refuses
// a write; a single note over the size limit is always refused.
func TestEvictionOrderAndPermanentNotes(t *testing.T) {
	s := openStore(t, "ra")
	s.SetPolicy(Policy{MaxEntries: 4, MaxBytes: 1000})
	at := func(i int) time.Time { return t0.Add(time.Duration(i) * time.Minute) }
	perm, _ := s.Write(Draft{Key: "perm", Permanent: true}, at(0)) // the oldest
	n1, _ := s.Write(Draft{Key: "n1"}, at(1))
	n2, _ := s.Write(Draft{Key: "n2"}, at(2))
	n3, _ := s.Write(Draft{Key: "n3"}, at(3))
	// n1 is rewritten: now n2 is the one updated longest ago.
	if _, err := s.Write(Draft{ID: n1.ID, Key: "n1", Body: "again"}, at(4)); err != nil {
		t.Fatal(err)
	}
	_, ev, err := s.Put(Draft{Key: "n4"}, at(5))
	if err != nil || len(ev) != 1 || ev[0].ID != n2.ID {
		t.Fatalf("evicted %+v, %v", ev, err)
	}
	if got := liveKeys(t, s, at(5)); !reflect.DeepEqual(got, []string{"n1", "n3", "n4", "perm"}) {
		t.Fatalf("live: %v", got)
	}
	// By size: a big note pushes out as many old ones as it needs.
	_, ev, err = s.Put(Draft{Key: "big", Body: strings.Repeat("b", 990)}, at(6))
	if err != nil || len(ev) != 2 || ev[0].ID != n3.ID || ev[1].ID != n1.ID {
		t.Fatalf("evicted %+v, %v", ev, err)
	}
	if got := liveKeys(t, s, at(6)); !reflect.DeepEqual(got, []string{"big", "n4", "perm"}) {
		t.Fatalf("live: %v", got)
	}
	if g, err := s.Get(perm.ID, at(6)); err != nil || !g.Permanent || !g.Expires.IsZero() {
		t.Fatalf("permanent note: %+v, %v", g, err)
	}
	// Only permanent notes left, and no room: the write is refused.
	s.SetPolicy(Policy{MaxEntries: 2})
	if _, err := s.Write(Draft{Key: "big", Body: "small now", Permanent: true}, at(7)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(Draft{Key: "third"}, at(8)); !errors.Is(err, ErrTooMany) {
		t.Fatalf("full of permanent notes: %v", err)
	}
	if _, err := s.Write(Draft{Key: "huge", Body: strings.Repeat("x", MaxEntrySize)}, at(9)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the entry limit: %v", err)
	}
	// Tidying evicts too, when peers' writes took the copy past a cap.
	o := openStore(t, "rb")
	for i := range 5 {
		if _, err := o.Write(Draft{Key: fmt.Sprint("p", i)}, at(10+i)); err != nil {
			t.Fatal(err)
		}
	}
	o.SetPolicy(Policy{MaxEntries: 3})
	r := tidy(t, o, at(20))
	if r.Evicted != 2 || r.LiveNotes != 3 {
		t.Fatalf("tidy: %+v", r)
	}
	if got := liveKeys(t, o, at(20)); !reflect.DeepEqual(got, []string{"p2", "p3", "p4"}) {
		t.Fatalf("live: %v", got)
	}
}

// Writing a title that exists updates that note. Two runners that create
// the same title before hearing of each other end with one note: the one
// created first, holding the newest content; the other a tombstone.
func TestSameTitleConverges(t *testing.T) {
	a, b := openStore(t, "ra"), openStore(t, "rb")
	e1, _ := a.Write(Draft{Key: "Deploy", Body: "v1"}, t0)
	e2, err := a.Write(Draft{Key: " deploy", Body: "v2", Tags: []string{"ops"}}, t0.Add(time.Minute))
	if err != nil || e2.ID != e1.ID || e2.Created != e1.Created {
		t.Fatalf("a write by title made a new note: %+v, %v", e2, err)
	}
	other, _ := a.Write(Draft{Key: "Other"}, t0)
	if _, err := a.Write(Draft{ID: other.ID, Key: "DEPLOY"}, t0); !errors.Is(err, ErrTitleTaken) {
		t.Fatalf("renaming onto a taken title: %v", err)
	}

	// b creates "deploy" too, later, without having heard of a's.
	eb, _ := b.Write(Draft{Key: "deploy", Body: "from b", Permanent: true}, t0.Add(2*time.Minute))
	// a updates its own once more, after b did (by wall clock).
	ea, _ := a.Write(Draft{ID: e1.ID, Key: "Deploy", Body: "from a, latest"}, t0.Add(3*time.Minute))
	syncAll(t, a, b, t0.Add(4*time.Minute))
	// Both merge at once, each on its own.
	wa, err := a.Dedup(t0.Add(5 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	wb, _ := b.Dedup(t0.Add(5 * time.Minute))
	if len(wa) == 0 || len(wb) == 0 {
		t.Fatalf("no merge: %v %v", wa, wb)
	}
	deliver(t, a, wb)
	deliver(t, b, wa)
	sameCopies(t, a, b)
	for _, s := range []*Store{a, b} {
		es := sameTitle(func() []Entry { es, _ := s.List(Query{}, t0.Add(time.Hour)); return es }(), "deploy")
		if len(es) != 1 {
			t.Fatalf("%s: %+v", s.self, es)
		}
		got := es[0]
		// a's note was created first: it stays, with the newest body, and
		// stays permanent as b's was.
		if got.ID != ea.ID || got.Body != "from a, latest" || !got.Permanent {
			t.Fatalf("%s kept %+v", s.self, got)
		}
		if g, err := s.Get(eb.ID, t0.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s still shows b's duplicate: %+v", s.self, g)
		}
	}
	if w, _ := a.Dedup(t0.Add(time.Hour)); len(w) != 0 {
		t.Fatalf("merged again: %+v", w)
	}
}

// Confirming a note pushes its expiry forward on every copy: it is an
// ordinary synced write.
func TestConfirmExtendsEverywhere(t *testing.T) {
	clk := &fakeClock{t: t0}
	n := newNet(t)
	a, b := n.add("ra"), n.add("rb")
	a.svc.Now, b.svc.Now = clk.now, clk.now
	ctx := context.Background()
	n.pair(a, b)
	e, err := a.svc.Put(ctx, "p1", Draft{Key: "flaky", Body: "TestX"})
	if err != nil {
		t.Fatal(err)
	}
	if !e.Expires.Equal(t0.Add(DefaultTTL)) {
		t.Fatalf("default expiry: %v", e.Expires)
	}
	clk.set(t0.Add(25 * day))
	c, err := b.svc.Confirm(ctx, "p1", e.ID, "t2", 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := t0.Add(25*day + DefaultTTL); !c.Expires.Equal(want) {
		t.Fatalf("confirmed expiry %v, want %v", c.Expires, want)
	}
	clk.set(t0.Add(40 * day)) // past the first expiry
	for _, x := range []*testRunner{a, b} {
		if _, err := x.svc.Tidy(ctx, "p1"); err != nil {
			t.Fatal(err)
		}
		g, err := x.svc.Get("p1", e.ID)
		if err != nil || !g.Expires.Equal(c.Expires) {
			t.Fatalf("%s: %+v, %v", x.id, g, err)
		}
	}
	clk.set(c.Expires)
	for _, x := range []*testRunner{a, b} {
		r, _ := x.svc.Tidy(ctx, "p1")
		if r.Expired != 1 || len(keys(t, x)) != 0 {
			t.Fatalf("%s after the confirmed expiry: %+v", x.id, r)
		}
	}
}

// A runner away from the hub longer than the retention window doesn't
// bring back what the others deleted and then purged: it discards its
// copy and resyncs in full from a peer.
func TestOfflinePastRetentionResyncs(t *testing.T) {
	clk := &fakeClock{t: t0}
	n := newNet(t)
	a, b, c := n.add("ra"), n.add("rb"), n.add("rc")
	for _, x := range []*testRunner{a, b, c} {
		x.svc.Now = clk.now
	}
	ctx := context.Background()
	n.pair(a, b, c)
	x, err := a.svc.Put(ctx, "p1", Draft{Key: "x", Body: "v1", Permanent: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(t, c); !reflect.DeepEqual(got, []string{"x=v1"}) {
		t.Fatalf("c: %v", got)
	}
	// c goes away, and edits x and writes a note meanwhile.
	n.setOnline(c, false)
	if _, err := c.svc.Put(ctx, "p1", Draft{ID: x.ID, Key: "x", Body: "c's edit"}); err != nil {
		t.Fatal(err)
	}
	c.svc.Put(ctx, "p1", Draft{Key: "c's note", Permanent: true})
	// The others write on, and delete x.
	clk.set(t0.Add(time.Hour))
	for i := range 3 {
		a.svc.Put(ctx, "p1", Draft{Key: fmt.Sprint("a", i), Permanent: true})
	}
	if err := b.svc.Delete(ctx, "p1", x.ID, ""); err != nil {
		t.Fatal(err)
	}
	// Seventy days pass; a and b stay in touch, and tidy: x's tombstone
	// goes past the retention window and is purged.
	for d := 20; d <= 70; d += 10 {
		clk.set(t0.Add(time.Duration(d) * day))
		for _, y := range []*testRunner{a, b} {
			if err := y.svc.CatchUp(ctx, "p1"); err != nil {
				t.Fatal(err)
			}
			y.svc.TidyAll(ctx, []string{"p1"})
		}
	}
	for _, y := range []*testRunner{a, b} {
		for _, e := range dump(t, y.svc.stores["p1"]) {
			if e.ID == x.ID {
				t.Fatalf("%s still holds x: %+v", y.id, e)
			}
		}
	}
	if stale, _ := c.svc.stores["p1"].Stale(clk.now()); !stale {
		t.Fatal("c's copy isn't stale")
	}
	// c's own tidying, while stale, writes nothing.
	if r, _ := c.svc.Tidy(ctx, "p1"); !r.Stale || len(r.Writes) != 0 {
		t.Fatalf("stale tidy: %+v", r)
	}
	// c is back: its copy is replaced by theirs; x doesn't come back, nor
	// does what c wrote while away.
	n.setOnline(c, true)
	if err := c.svc.CatchUp(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"a0=", "a1=", "a2="}
	for _, y := range []*testRunner{a, b, c} {
		if err := y.svc.CatchUp(ctx, "p1"); err != nil {
			t.Fatal(err)
		}
		if got := keys(t, y); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %v", y.id, got)
		}
	}
	sameCopies(t, a.svc.stores["p1"], b.svc.stores["p1"], c.svc.stores["p1"])
	// c is in sync again, and its new writes reach the others.
	if stale, _ := c.svc.stores["p1"].Stale(clk.now()); stale {
		t.Fatal("c is still stale after the resync")
	}
	if _, err := c.svc.Put(ctx, "p1", Draft{Key: "c again"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "c's new note to reach a", func() bool { return slices.Contains(keys(t, a), "c again=") })
}

// Two runners both away past the retention window: neither copy is
// fresher, so the first to catch up keeps its own and the other resyncs
// from it.
func TestAllStaleBreakTheTie(t *testing.T) {
	clk := &fakeClock{t: t0}
	n := newNet(t)
	a, b := n.add("ra"), n.add("rb")
	a.svc.Now, b.svc.Now = clk.now, clk.now
	ctx := context.Background()
	n.pair(a, b)
	a.svc.Put(ctx, "p1", Draft{Key: "shared", Permanent: true})
	clk.set(t0.Add(90 * day))
	if err := a.svc.CatchUp(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if err := b.svc.CatchUp(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	for _, x := range []*testRunner{a, b} {
		if stale, _ := x.svc.stores["p1"].Stale(clk.now()); stale {
			t.Fatalf("%s still stale", x.id)
		}
		if got := keys(t, x); !reflect.DeepEqual(got, []string{"shared="}) {
			t.Fatalf("%s: %v", x.id, got)
		}
	}
}

// A clear deletes every note updated before its time, on every copy, and
// drops older versions that arrive later, also after the clear itself was
// purged at the end of the retention window.
func TestClearIsRespected(t *testing.T) {
	a, b := openStore(t, "ra"), openStore(t, "rb")
	n1, _ := a.Write(Draft{Key: "n1", Permanent: true}, t0)
	n2, _ := a.Write(Draft{Key: "n2", Permanent: true}, t0.Add(time.Hour))
	cl, err := a.Clear(t0.Add(30*time.Minute), "ui", t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got := liveKeys(t, a, t0.Add(2*time.Hour)); !reflect.DeepEqual(got, []string{"n2"}) {
		t.Fatalf("a after the clear: %v", got)
	}
	// b gets the clear first, then the notes, out of order.
	deliver(t, b, []Entry{cl, n2, n1})
	if got := liveKeys(t, b, t0.Add(2*time.Hour)); !reflect.DeepEqual(got, []string{"n2"}) {
		t.Fatalf("b: %v", got)
	}
	sameCopies(t, a, b)
	// The clear is kept through the retention window, then purged…
	tidy(t, a, t0.Add(2*time.Hour+59*day))
	if !slices.ContainsFunc(dump(t, a), func(e Entry) bool { return e.ID == cl.ID }) {
		t.Fatal("clear purged too early")
	}
	tidy(t, a, t0.Add(2*time.Hour+61*day))
	if slices.ContainsFunc(dump(t, a), func(e Entry) bool { return e.ID == cl.ID }) {
		t.Fatal("clear kept past the retention window")
	}
	// …and what it deleted still doesn't come back.
	deliver(t, a, []Entry{n1})
	if got := liveKeys(t, a, t0.Add(62*day)); !reflect.DeepEqual(got, []string{"n2"}) {
		t.Fatalf("a: %v", got)
	}
}

// A database made before tidying gets the new columns.
func TestMigration(t *testing.T) {
	s := openStore(t, "ra")
	path := s.db
	if _, err := path.Exec(`ALTER TABLE entries DROP COLUMN permanent`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(Draft{Key: "k", Permanent: true}, t0); err != nil {
		t.Fatal(err)
	}
}
