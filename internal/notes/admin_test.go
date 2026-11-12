package notes

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A runner's status of its copy is counts: no title, body, tag, task or
// note id is in it.
func TestStatusHoldsNoNoteContent(t *testing.T) {
	n := newNet(t)
	a, b := n.add("ra"), n.add("rb")
	n.pair(a, b)
	ctx := context.Background()
	const marker = "zq-content-4c2"
	e, err := a.svc.Put(ctx, "p1", Draft{Key: "title " + marker, Body: "body " + marker, Tags: []string{"tag-" + marker}, AuthorTask: "task-" + marker})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.svc.Put(ctx, "p1", Draft{Key: "gone " + marker, Body: marker, TTL: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	st, err := a.svc.Status("p1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Notes != 1 || st.Expired != 1 || st.Bytes != e.size() || st.Newest.IsZero() || st.KeyID == "" || st.Fingerprint != a.svc.Fingerprint() {
		t.Fatalf("status: %+v", st)
	}
	stb, _ := b.svc.Status("p1")
	if !reflect.DeepEqual(st.Vector, stb.Vector) || st.KeyID != stb.KeyID {
		t.Fatalf("the copies disagree: %+v %+v", st, stb)
	}
	raw, _ := json.Marshal(st)
	for _, s := range []string{marker, e.ID} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("the status holds %q: %s", s, raw)
		}
	}
	// A runner waiting for approval shows, by its public fingerprint.
	c := n.add("rc")
	approve(t, c, a)
	if err := c.svc.CatchUp(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	st, _ = a.svc.Status("p1")
	if len(st.Waiting) != 1 || st.Waiting[0].Runner != "rc" || st.Waiting[0].Fingerprint != c.svc.Fingerprint() {
		t.Fatalf("waiting: %+v", st.Waiting)
	}
}

// Each filter removes what it names, on every copy, as synced tombstones,
// and each runner counts what it removed from its own copy.
func TestRemovalFilters(t *testing.T) {
	n := newNet(t)
	a, b := n.add("ra"), n.add("rb")
	n.pair(a, b)
	ctx := context.Background()
	put := func(x *testRunner, d Draft) Entry {
		t.Helper()
		e, err := x.svc.Put(ctx, "p1", d)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	put(a, Draft{Key: "t1 a", AuthorTask: "hub-t1"})
	put(b, Draft{Key: "t1 b", AuthorTask: "local-t1"})
	put(b, Draft{Key: "t2", AuthorTask: "hub-t2"})
	put(a, Draft{Key: "by a"})
	put(a, Draft{Key: "dies", TTL: time.Millisecond})
	time.Sleep(5 * time.Millisecond)
	both := func(want ...string) {
		t.Helper()
		for _, x := range []*testRunner{a, b} {
			var got []string
			for _, k := range keys(t, x) {
				got = append(got, strings.TrimSuffix(k, "="))
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: %v, want %v", x.id, got, want)
			}
		}
	}
	both("by a", "t1 a", "t1 b", "t2")

	// By task, by any of its ids.
	got, err := a.svc.Remove(ctx, "p1", Removal{Kind: RemoveTask, Tasks: []string{"hub-t1", "local-t1"}})
	if err != nil || got != 2 {
		t.Fatalf("by task: %d %v", got, err)
	}
	both("by a", "t2")
	// By the runner that last wrote them.
	if got, err := b.svc.Remove(ctx, "p1", Removal{Kind: RemoveRunner, Runner: "rb"}); err != nil || got != 1 {
		t.Fatalf("by runner: %d %v", got, err)
	}
	both("by a")
	// Expired: each copy purges its own.
	for _, x := range []*testRunner{a, b} {
		if got, err := x.svc.Remove(ctx, "p1", Removal{Kind: RemoveExpired}); err != nil || got != 1 {
			t.Fatalf("%s expired: %d %v", x.id, got, err)
		}
		if st, _ := x.svc.Status("p1"); st.Expired != 0 {
			t.Fatalf("%s still has expired notes: %+v", x.id, st)
		}
	}
	// All, before the time it was asked: a later note stays.
	asked := time.Now()
	time.Sleep(2 * time.Millisecond)
	put(b, Draft{Key: "later"})
	if got, err := b.svc.Remove(ctx, "p1", Removal{Kind: RemoveAll, Before: asked}); err != nil || got != 1 {
		t.Fatalf("all: %d %v", got, err)
	}
	both("later")
	sameCopies(t, a.svc.stores["p1"], b.svc.stores["p1"])
}

// Remove-all is one project-wide tombstone: a runner offline during it
// catches up later and loses its copies of the notes too, without sending
// them back; what it wrote after the removal was asked stays.
func TestRemoveAllWithARunnerOffline(t *testing.T) {
	n := newNet(t)
	a, b, c := n.add("ra"), n.add("rb"), n.add("rc")
	n.pair(a, b, c)
	ctx := context.Background()
	for i, x := range []*testRunner{a, b, c} {
		if _, err := x.svc.Put(ctx, "p1", Draft{Key: "note " + x.id, Permanent: i == 0}); err != nil {
			t.Fatal(err)
		}
	}
	n.setOnline(c, false)
	if _, err := c.svc.Put(ctx, "p1", Draft{Key: "offline before"}); err != nil {
		t.Fatal(err)
	}
	asked := time.Now()
	time.Sleep(2 * time.Millisecond)
	got, err := a.svc.Remove(ctx, "p1", Removal{ID: "r1", Kind: RemoveAll, Before: asked})
	if err != nil || got != 3 {
		t.Fatalf("remove all: %d %v", got, err)
	}
	if k := keys(t, b); len(k) != 0 {
		t.Fatalf("b: %v", k)
	}
	if _, err := c.svc.Put(ctx, "p1", Draft{Key: "offline after"}); err != nil {
		t.Fatal(err)
	}
	if k := keys(t, c); len(k) != 5 {
		t.Fatalf("c, offline, still has its copy: %v", k)
	}
	// c is back.
	n.setOnline(c, true)
	if err := c.svc.CatchUp(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	for _, x := range []*testRunner{a, b, c} {
		if k := keys(t, x); !reflect.DeepEqual(k, []string{"offline after="}) {
			t.Fatalf("%s after c caught up: %v", x.id, k)
		}
	}
	// One clear, not a tombstone per note.
	clears := 0
	for _, e := range dump(t, a.svc.stores["p1"]) {
		if e.isClear() {
			clears++
		}
	}
	if clears != 1 {
		t.Fatalf("%d clears", clears)
	}
	// Catching up again changes nothing.
	if err := b.svc.CatchUp(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	sameCopies(t, a.svc.stores["p1"], b.svc.stores["p1"], c.svc.stores["p1"])
}

// A runner takes removals only for projects it keeps notes for, and only
// well-formed ones.
func TestRemovalRefusals(t *testing.T) {
	n := newNet(t)
	a := n.add("ra")
	ctx := context.Background()
	if _, err := a.svc.Remove(ctx, "p2", Removal{Kind: RemoveExpired}); !errors.Is(err, ErrNoProject) {
		t.Fatalf("a project it isn't on: %v", err)
	}
	if _, err := a.svc.Status("p2"); !errors.Is(err, ErrNoProject) {
		t.Fatalf("status of a project it isn't on: %v", err)
	}
	for _, r := range []Removal{
		{Kind: "some"},
		{Kind: RemoveAll}, // no time
		{Kind: RemoveTask},
		{Kind: RemoveTask, Tasks: []string{""}},
		{Kind: RemoveRunner},
		{Kind: RemoveExpired, ID: strings.Repeat("x", 65)},
	} {
		if _, err := a.svc.Remove(ctx, "p1", r); !errors.Is(err, ErrBadRemoval) {
			t.Fatalf("%+v: %v", r, err)
		}
	}
}
