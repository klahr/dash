package notes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

func openStore(t *testing.T, self string) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), self+".db"), self)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func dump(t *testing.T, s *Store) []Entry {
	t.Helper()
	es, err := s.all()
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func sameCopies(t *testing.T, stores ...*Store) {
	t.Helper()
	want := dump(t, stores[0])
	for _, s := range stores[1:] {
		if got := dump(t, s); !reflect.DeepEqual(got, want) {
			t.Fatalf("copies differ:\n%s: %+v\n%s: %+v", stores[0].self, want, s.self, got)
		}
	}
}

// Writes made concurrently on three runners, updates and deletes of each
// other's entries included, converge to the same copy whatever order each
// runner receives them in.
func TestConcurrentWritesConverge(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	stores := []*Store{openStore(t, "ra"), openStore(t, "rb"), openStore(t, "rc")}
	var written [][]Entry // per store, its writes in order
	for range stores {
		written = append(written, nil)
	}
	ids := []string{}
	for round := range 40 {
		i := rng.IntN(len(stores))
		s := stores[i]
		now := t0.Add(time.Duration(round) * time.Second)
		var e Entry
		var err error
		switch {
		case len(ids) > 0 && rng.IntN(4) == 0:
			e, err = s.Delete(ids[rng.IntN(len(ids))], "t", now)
		case len(ids) > 0 && rng.IntN(2) == 0:
			e, err = s.Write(Draft{ID: ids[rng.IntN(len(ids))], Key: "k", Body: fmt.Sprint("v", round)}, now)
		default:
			e, err = s.Write(Draft{Key: fmt.Sprint("note ", round), Body: "b", Tags: []string{"x"}}, now)
		}
		if errors.Is(err, ErrNotFound) {
			continue // deleted here already
		}
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(ids, e.ID) {
			ids = append(ids, e.ID)
		}
		written[i] = append(written[i], e)
		// Sometimes a write reaches some of the others at once.
		for j, o := range stores {
			if j != i && rng.IntN(3) == 0 {
				if _, _, err := o.Apply([]Entry{e}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// Then everything reaches everyone, each in its own shuffled order.
	for j, o := range stores {
		var all []Entry
		for i, ws := range written {
			if i != j {
				all = append(all, ws...)
			}
		}
		rng.Shuffle(len(all), func(a, b int) { all[a], all[b] = all[b], all[a] })
		for _, e := range all {
			if _, _, err := o.Apply([]Entry{e}); err != nil {
				t.Fatal(err)
			}
		}
	}
	sameCopies(t, stores...)
}

// Two runners update the same entry without seeing each other's write:
// both keep the same winner. A write made after seeing another wins over it.
func TestLastWriterWins(t *testing.T) {
	a, b := openStore(t, "ra"), openStore(t, "rb")
	e, err := a.Write(Draft{Key: "build", Body: "make"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Apply([]Entry{e}); err != nil {
		t.Fatal(err)
	}
	ea, _ := a.Write(Draft{ID: e.ID, Key: "build", Body: "from a"}, t0.Add(time.Second))
	eb, _ := b.Write(Draft{ID: e.ID, Key: "build", Body: "from b"}, t0.Add(time.Second))
	a.Apply([]Entry{eb})
	b.Apply([]Entry{ea})
	ga, _ := a.Get(e.ID, t0)
	gb, _ := b.Get(e.ID, t0)
	if ga.Body != gb.Body || ga.Body != "from b" { // same clock: the larger writer id wins
		t.Fatalf("a has %q, b has %q", ga.Body, gb.Body)
	}
	// a, having seen b's write, writes again: its clock is past b's.
	ea2, _ := a.Write(Draft{ID: e.ID, Key: "build", Body: "a again"}, t0.Add(2*time.Second))
	b.Apply([]Entry{ea2})
	if gb, _ := b.Get(e.ID, t0); gb.Body != "a again" {
		t.Fatalf("b has %q after a's later write", gb.Body)
	}
	// An old version arriving late changes nothing.
	b.Apply([]Entry{ea})
	if gb, _ := b.Get(e.ID, t0); gb.Body != "a again" {
		t.Fatalf("a stale write won: %q", gb.Body)
	}
	// A delete is a write like any other, and wins over what it saw.
	d, err := b.Delete(e.ID, "task-1", t0.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	a.Apply([]Entry{d})
	if _, err := a.Get(e.ID, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted entry still shown on a: %v", err)
	}
	sameCopies(t, a, b)
}

// A write that arrives before one it follows leaves a gap: the vector
// doesn't move past it, so a catch-up asks for it again.
func TestGapsHoldTheVectorBack(t *testing.T) {
	a, b := openStore(t, "ra"), openStore(t, "rb")
	e1, _ := a.Write(Draft{Key: "one"}, t0)
	e2, _ := a.Write(Draft{Key: "two"}, t0)
	if _, gap, _ := b.Apply([]Entry{e2}); !gap {
		t.Fatal("no gap reported")
	}
	if v, _ := b.Vector(); v["ra"] != 0 {
		t.Fatalf("vector moved past a gap: %v", v)
	}
	if _, gap, _ := b.Apply([]Entry{e1}); gap {
		t.Fatal("gap after the missing write came")
	}
	// e2 is already here, but the vector only now reaches 1: e2 comes
	// again in a catch-up and changes nothing.
	vb, _ := b.Vector()
	es, va, _ := a.Since(vb, t0)
	if len(es) != 1 || es[0].ID != e2.ID {
		t.Fatalf("since %v: %+v", vb, es)
	}
	b.Merge(es, va)
	if v, _ := b.Vector(); v["ra"] != 2 {
		t.Fatalf("vector after catch-up: %v", v)
	}
	sameCopies(t, a, b)
}

func TestLimitsAndExpiry(t *testing.T) {
	s := openStore(t, "ra")
	if _, err := s.Write(Draft{Key: "big", Body: strings.Repeat("x", MaxEntrySize)}, t0); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized entry: %v", err)
	}
	if _, err := s.Write(Draft{Key: "  "}, t0); !errors.Is(err, ErrNoKey) {
		t.Fatalf("entry without key: %v", err)
	}
	e, err := s.Write(Draft{Key: "soon gone", TTL: time.Hour}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if es, _ := s.List(Query{}, t0.Add(59*time.Minute)); len(es) != 1 {
		t.Fatalf("before expiry: %+v", es)
	}
	if es, _ := s.List(Query{}, t0.Add(time.Hour)); len(es) != 0 {
		t.Fatalf("after expiry: %+v", es)
	}
	if _, err := s.Get(e.ID, t0.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired entry: %v", err)
	}
	if es, _, _ := s.Since(Vector{}, t0.Add(2*time.Hour)); len(es) != 0 {
		t.Fatalf("expired entry offered to a peer: %+v", es)
	}
	for i := range MaxEntries - 1 { // "soon gone" is still live at t0
		if _, err := s.Write(Draft{Key: fmt.Sprint(i)}, t0); err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
	}
	if _, err := s.Write(Draft{Key: "one more"}, t0); !errors.Is(err, ErrTooMany) {
		t.Fatalf("past the limit: %v", err)
	}
	// Peers' entries that break the rules are skipped, the rest kept.
	o := openStore(t, "rb")
	bad := Entry{ID: "rx:1", Key: "k", Body: strings.Repeat("x", MaxEntrySize+1), Clock: 1, Writer: "rx", Seq: 1}
	good := Entry{ID: "rx:2", Key: "k", Clock: 2, Writer: "rx", Seq: 2}
	if n, _, err := o.Apply([]Entry{bad, good}); err != nil || n != 1 {
		t.Fatalf("applied %d, %v", n, err)
	}
}

func TestListFilters(t *testing.T) {
	s := openStore(t, "ra")
	s.Write(Draft{Key: "Build", Body: "run make", Tags: []string{"ci", " ci ", ""}}, t0)
	s.Write(Draft{Key: "Deploy", Body: "fly deploy", Tags: []string{"ops"}}, t0.Add(time.Second))
	if es, _ := s.List(Query{Tag: "ci"}, t0); len(es) != 1 || es[0].Key != "Build" || !reflect.DeepEqual(es[0].Tags, []string{"ci"}) {
		t.Fatalf("by tag: %+v", es)
	}
	if es, _ := s.List(Query{Text: "FLY"}, t0); len(es) != 1 || es[0].Key != "Deploy" {
		t.Fatalf("by text: %+v", es)
	}
	if es, _ := s.List(Query{}, t0); len(es) != 2 || es[0].Key != "Deploy" {
		t.Fatalf("newest first: %+v", es)
	}
}

// A runner whose database was lost doesn't reuse its write counters: a
// catch-up tells it how far it had got.
func TestLostDatabaseDoesNotReuseCounters(t *testing.T) {
	a, b := openStore(t, "ra"), openStore(t, "rb")
	e, _ := a.Write(Draft{Key: "first"}, t0)
	b.Apply([]Entry{e})
	a2 := openStore(t, "ra") // a, from scratch
	vb, _ := a2.Vector()
	es, v, _ := b.Since(vb, t0)
	a2.Merge(es, v)
	e2, err := a2.Write(Draft{Key: "second"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if e2.ID == e.ID || e2.Seq <= e.Seq {
		t.Fatalf("reused a counter: %+v after %+v", e2, e)
	}
}

// ---- the relay ----

// testNet is a hub relaying between runners over HTTP, as in production:
// Relay on the hub, HubTransport on each runner.
type testNet struct {
	t       *testing.T
	hub     *httptest.Server
	mu      sync.Mutex
	runners map[string]*testRunner
	relayed int // messages the hub passed on
}

type testRunner struct {
	id     string
	srv    *httptest.Server
	svc    *Service
	online bool
}

func newNet(t *testing.T) *testNet {
	n := &testNet{t: t, runners: map[string]*testRunner{}}
	relay := &Relay{
		Route: func(r *http.Request, project string) ([]string, error) {
			from := r.Header.Get("X-Test-Runner")
			n.mu.Lock()
			defer n.mu.Unlock()
			if x := n.runners[from]; x == nil || !x.online {
				return nil, &RelayError{Status: http.StatusUnauthorized, Msg: "who?"}
			}
			var peers []string
			for id, x := range n.runners {
				if id != from && x.online {
					peers = append(peers, id)
				}
			}
			slices.Sort(peers)
			return peers, nil
		},
		Forward: func(ctx context.Context, peer, project string, msg []byte) ([]byte, error) {
			n.mu.Lock()
			x := n.runners[peer]
			n.relayed++
			n.mu.Unlock()
			req, _ := http.NewRequestWithContext(ctx, "POST", x.srv.URL+PeerPathFor(project), bytes.NewReader(msg))
			req.Header.Set(RelayHeader, "1")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				return nil, err
			}
			defer res.Body.Close()
			b, err := io.ReadAll(io.LimitReader(res.Body, MaxReply))
			if res.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("%s: %s", res.Status, b)
			}
			return b, err
		},
	}
	n.hub = httptest.NewServer(relay)
	t.Cleanup(n.hub.Close)
	return n
}

func (n *testNet) add(id string) *testRunner {
	tr := &HubTransport{}
	x := &testRunner{id: id, online: true}
	tr.Post = func(ctx context.Context, project string, body []byte) ([]byte, error) {
		n.mu.Lock()
		on := x.online
		n.mu.Unlock()
		if !on {
			return nil, errors.New("hub unreachable")
		}
		req, _ := http.NewRequestWithContext(ctx, "POST", n.hub.URL+RelayPath+"?project="+project, bytes.NewReader(body))
		req.Header.Set("X-Test-Runner", id)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", res.Status, b)
		}
		return b, nil
	}
	mux := http.NewServeMux()
	mux.Handle("POST "+PeerPath, tr)
	x.srv = httptest.NewServer(mux)
	n.t.Cleanup(x.srv.Close)
	svc, err := NewService(n.t.TempDir(), id, tr, func(p string) bool { return p == "p1" })
	if err != nil {
		n.t.Fatal(err)
	}
	n.t.Cleanup(func() { svc.Close() })
	x.svc = svc
	n.mu.Lock()
	n.runners[id] = x
	n.mu.Unlock()
	return x
}

func (n *testNet) setOnline(x *testRunner, on bool) {
	n.mu.Lock()
	x.online = on
	n.mu.Unlock()
}

func keys(t *testing.T, x *testRunner) []string {
	t.Helper()
	es, err := x.svc.List("p1", Query{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Key+"="+e.Body)
	}
	slices.Sort(out)
	return out
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for range 200 {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + what)
}

// Writes reach the project's online runners at once through the relay; a
// runner that was offline during them catches up when it is back, and the
// writes it made while offline reach the others.
func TestRelayWithARunnerOffline(t *testing.T) {
	n := newNet(t)
	a, b, c := n.add("ra"), n.add("rb"), n.add("rc")
	ctx := context.Background()
	n.setOnline(c, false)

	e, err := a.svc.Put(ctx, "p1", Draft{Key: "build", Body: "make all", AuthorTask: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.svc.Put(ctx, "p1", Draft{ID: e.ID, Key: "build", Body: "make test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.Put(ctx, "p1", Draft{Key: "flaky", Body: "TestX"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"build=make test", "flaky=TestX"}
	if got := keys(t, a); !reflect.DeepEqual(got, want) {
		t.Fatalf("a: %v", got)
	}
	if got := keys(t, b); !reflect.DeepEqual(got, want) {
		t.Fatalf("b: %v", got)
	}
	// c, offline, still reads and writes its own copy.
	if got := keys(t, c); len(got) != 0 {
		t.Fatalf("c got writes while offline: %v", got)
	}
	if _, err := c.svc.Put(ctx, "p1", Draft{Key: "from c", Body: "offline"}); err != nil {
		t.Fatalf("an offline write: %v", err)
	}
	if got := keys(t, c); !reflect.DeepEqual(got, []string{"from c=offline"}) {
		t.Fatalf("c reads its own write: %v", got)
	}

	// Back online: one catch-up, and all three agree.
	n.setOnline(c, true)
	if err := c.svc.CatchUp(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	want = []string{"build=make test", "flaky=TestX", "from c=offline"}
	for _, x := range []*testRunner{a, b, c} {
		if got := keys(t, x); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s after catch-up: %v", x.id, got)
		}
	}
	sameCopies(t, a.svc.stores["p1"], b.svc.stores["p1"], c.svc.stores["p1"])

	// A delete reaches everyone too.
	if err := c.svc.Delete(ctx, "p1", e.ID, "t9"); err != nil {
		t.Fatal(err)
	}
	for _, x := range []*testRunner{a, b} {
		if _, err := x.svc.Get("p1", e.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s still has the deleted note: %v", x.id, err)
		}
	}
}

// A runner that missed a write (the hub was briefly away) and then gets the
// next one notices the gap and catches up by itself.
func TestMissedWriteIsFetchedOnTheNextOne(t *testing.T) {
	n := newNet(t)
	a, b := n.add("ra"), n.add("rb")
	ctx := context.Background()
	n.setOnline(b, false)
	a.svc.Put(ctx, "p1", Draft{Key: "one"})
	n.setOnline(b, true)
	a.svc.Put(ctx, "p1", Draft{Key: "two"})
	eventually(t, "b to fetch the write it missed", func() bool {
		return reflect.DeepEqual(keys(t, b), []string{"one=", "two="})
	})
}

// Notes for projects a runner doesn't keep are refused; secrets never
// leave the writing runner, nor are they saved.
func TestRefusals(t *testing.T) {
	n := newNet(t)
	a, b := n.add("ra"), n.add("rb")
	ctx := context.Background()
	if _, err := a.svc.Put(ctx, "p2", Draft{Key: "x"}); !errors.Is(err, ErrNoProject) {
		t.Fatalf("project not kept here: %v", err)
	}
	before := n.relayed
	_, err := a.svc.Put(ctx, "p1", Draft{Key: "token", Body: "GH_TOKEN=ghp_" + strings.Repeat("a1B2", 9)})
	if !errors.Is(err, ErrSecret) || strings.Contains(err.Error(), "a1B2") {
		t.Fatalf("secret: %v", err)
	}
	if n.relayed != before || len(keys(t, a)) != 0 || len(keys(t, b)) != 0 {
		t.Fatal("a note with a secret was saved or sent")
	}
	// The runner's endpoint takes messages from the hub's relay only.
	res, err := http.Post(b.srv.URL+PeerPathFor("p1"), "application/octet-stream", strings.NewReader(`{"kind":"want"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("without the relay header: %d", res.StatusCode)
	}
}

// The relay keeps nothing: it is two functions, and the bytes it passes on
// are not inspected (any bytes go through, JSON or not).
func TestRelayIsStateless(t *testing.T) {
	typ := reflect.TypeFor[Relay]()
	for i := range typ.NumField() {
		if f := typ.Field(i); f.Type.Kind() != reflect.Func {
			t.Fatalf("Relay has a %s field %s: it must hold no state", f.Type.Kind(), f.Name)
		}
	}
	var got []byte
	relay := &Relay{
		Route: func(*http.Request, string) ([]string, error) { return []string{"x"}, nil },
		Forward: func(_ context.Context, _, _ string, msg []byte) ([]byte, error) {
			got = msg
			return []byte{0xff, 0x00}, nil
		},
	}
	opaque := []byte{0x00, 0x01, 0xfe, 'n', 'o', 't', ' ', 'j', 's', 'o', 'n'}
	w := httptest.NewRecorder()
	relay.ServeHTTP(w, httptest.NewRequest("POST", RelayPath+"?project=p1", bytes.NewReader(opaque)))
	if w.Code != http.StatusOK || !bytes.Equal(got, opaque) {
		t.Fatalf("%d, forwarded %v", w.Code, got)
	}
	tr := &HubTransport{Post: func(context.Context, string, []byte) ([]byte, error) { return w.Body.Bytes(), nil }}
	replies, err := tr.SendToPeers(context.Background(), "p1", opaque)
	if err != nil || len(replies) != 1 || !bytes.Equal(replies[0], []byte{0xff, 0x00}) {
		t.Fatalf("replies %v, %v", replies, err)
	}
}
