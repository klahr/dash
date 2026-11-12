package api

import (
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/hub"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/notes"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/perm"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// notesRunner is a runner with shared notes, connected to the hub.
type notesRunner struct {
	id     string
	dir    string
	notes  *notes.Service
	cancel context.CancelFunc
	done   chan struct{}
}

// Shared notes travel between a project's runners through the hub's relay,
// over their tunnels: a runner offline during writes catches up when back,
// runners not on the project get nothing, nobody reaches a runner's notes
// through the hub's API, and the hub keeps no trace of them. Runners share
// a project's key only once the user approved each other's keys on them.
func TestSharedNotesRelayThroughTheHub(t *testing.T) {
	parallel(t)
	hubDir := t.TempDir()
	st, err := store.Open(filepath.Join(hubDir, "dash.db"), &events{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Accounts().Bootstrap("u", "p"); err != nil {
		t.Fatal(err)
	}
	a := auth.New(st.Accounts())
	a.NoFailDelayInTests()
	h := &Server{Store: st, Hub: hub.New(), Auth: a, Perms: perm.NewBroker(nil), Board: &mcptools.Tokens{},
		Todos: queue.NewTodoEngine(&queue.TodoEngine{Log: &queue.TodoLog{Root: t.TempDir()}}), Static: fstest.MapFS{"index.html": {Data: []byte("ok")}}}
	rh := &tunnel.Hub{Accounts: st.Accounts()}
	if err := rh.Keep(filepath.Join(hubDir, "runners.json")); err != nil {
		t.Fatal(err)
	}
	h.Peers = &peer.Hub{Self: "hub", Runners: rh}
	srv := httptest.NewServer(h.Routes())
	defer srv.Close()
	owner := testOwner(t, h)

	var wg sync.WaitGroup
	register := func(name string) tunnel.Credentials {
		code, _, err := rh.NewCode(owner)
		if err != nil {
			t.Fatal(err)
		}
		creds, err := tunnel.Register(context.Background(), srv.URL, code, name, "")
		if err != nil {
			t.Fatal(err)
		}
		return creds
	}
	var project string // the hub's project id, set below
	// start brings a runner up: its notes, and its tunnel to the hub.
	start := func(creds tunnel.Credentials, dir string) *notesRunner {
		s, api, _ := runnerSide(t, creds.OrgID)
		wire := &notes.HubTransport{Post: func(ctx context.Context, p string, body []byte) ([]byte, error) {
			return tunnel.Notes(ctx, creds, p, body)
		}}
		ident, err := notes.LoadIdentity(notes.IdentityPath(dir))
		if err != nil {
			t.Fatal(err)
		}
		svc, err := notes.NewService(filepath.Join(dir, "shared"), creds.ID, ident, wire, func(p string) bool { return p == project })
		if err != nil {
			t.Fatal(err)
		}
		svc.Name = creds.Name
		s.NotesPeer = wire
		ctx, cancel := context.WithCancel(context.Background())
		x := &notesRunner{id: creds.ID, dir: dir, notes: svc, cancel: cancel, done: make(chan struct{})}
		client := &tunnel.Client{Creds: creds, Handler: api, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
			NotesKey: ident.Public.String()}
		wg.Add(1)
		go func() { defer wg.Done(); defer close(x.done); _ = client.Run(ctx) }()
		eventually(t, creds.Name+" to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })
		return x
	}
	stop := func(x *notesRunner) {
		x.cancel()
		<-x.done
		eventually(t, "the runner to go offline", func() bool { return !rh.Online(x.id) })
		x.notes.Close()
	}
	t.Cleanup(func() { wg.Wait() })

	ca, cb, cc, cx := register("alpha"), register("beta"), register("gamma"), register("outsider")
	p, err := st.CreateProject(model.Project{Name: "web", OrgID: owner.Org, Runners: []string{ca.ID, cb.ID, cc.ID}})
	if err != nil {
		t.Fatal(err)
	}
	project = p.ID
	alpha, beta := start(ca, t.TempDir()), start(cb, t.TempDir())
	outsider := start(cx, t.TempDir())
	t.Cleanup(func() { stop(alpha); stop(beta); stop(outsider) })
	gammaDir := t.TempDir() // gamma is offline for now
	gammaID, err := notes.LoadIdentity(notes.IdentityPath(gammaDir))
	if err != nil {
		t.Fatal(err)
	}

	// The hub shows each runner's key fingerprint (public), the one its
	// own `dash runner notes` shows.
	if r, _ := rh.Get(ca.ID); r.NotesFingerprint != alpha.notes.Fingerprint() {
		t.Fatalf("the hub shows alpha's fingerprint as %q, alpha %q", r.NotesFingerprint, alpha.notes.Fingerprint())
	}
	// The user approves the project's runners on each other, outsider not.
	fps := map[string]string{alpha.dir: alpha.notes.Fingerprint(), beta.dir: beta.notes.Fingerprint(), gammaDir: gammaID.Fingerprint()}
	for dir := range fps {
		for other, fp := range fps {
			if other != dir {
				if _, err := notes.Approve(filepath.Join(dir, "shared"), fp, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	ctx := context.Background()
	if err := alpha.notes.CatchUp(ctx, project); err != nil { // alpha and beta trade keys
		t.Fatal(err)
	}
	const marker = "zq-marker-7f3a" // a word that is in the notes and nowhere else
	e, err := alpha.notes.Put(ctx, project, notes.Draft{Key: "how to build", Body: "run make " + marker, Tags: []string{"build"}, AuthorTask: "t-1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := beta.notes.Get(project, e.ID)
	if err != nil || got.Body != e.Body || got.AuthorTask != "t-1" {
		t.Fatalf("beta: %+v, %v", got, err)
	}
	if _, err := beta.notes.Put(ctx, project, notes.Draft{Key: "flaky test", Body: "TestFoo " + marker}); err != nil {
		t.Fatal(err)
	}
	// The outsider runner isn't on the project: the hub refuses its
	// messages and sends it none.
	if _, err := tunnel.Notes(ctx, cx, project, []byte(`{"kind":"want"}`)); err == nil || !strings.Contains(err.Error(), "no such project") {
		t.Fatalf("a runner not on the project used its relay: %v", err)
	}
	if es, err := outsider.notes.List(project, notes.Query{}); err != nil || len(es) != 0 {
		t.Fatalf("the outsider got %d notes (%v)", len(es), err)
	}

	// gamma comes online after the writes: it catches up, and what it
	// wrote offline (before ever connecting) reaches the others.
	gamma := func() *notesRunner {
		svc, err := notes.NewService(filepath.Join(gammaDir, "shared"), cc.ID, gammaID, &notes.HubTransport{Post: func(context.Context, string, []byte) ([]byte, error) {
			return nil, tunnel.ErrOffline
		}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Put(ctx, project, notes.Draft{Key: "from gamma", Body: "written offline"}); err != nil {
			t.Fatal(err)
		}
		svc.Close()
		return start(cc, gammaDir)
	}()
	t.Cleanup(func() { stop(gamma) })
	if err := gamma.notes.CatchUp(ctx, project); err != nil {
		t.Fatal(err)
	}
	keys := func(x *notesRunner) []string {
		es, err := x.notes.List(project, notes.Query{})
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
	want := []string{"flaky test", "from gamma", "how to build"}
	for _, x := range []*notesRunner{alpha, beta, gamma} {
		if got := keys(x); !slices.Equal(got, want) {
			t.Fatalf("%s has %v", x.id, got)
		}
	}

	// The hub's proxy to a runner (/peer/…) doesn't carry the relay's
	// mark: through it, a runner's notes can't be read.
	px, _ := rh.Proxy("beta")
	req := httptest.NewRequest("POST", "/peer/beta"+notes.PeerPathFor(project), strings.NewReader(`{"kind":"want","vector":{}}`))
	req.Header.Set(notes.RelayHeader, "1")
	w := httptest.NewRecorder()
	px.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), marker) {
		t.Fatalf("notes read through the hub's proxy: %d %s", w.Code, w.Body)
	}

	// The hub kept nothing: no file of its data holds a word of the notes,
	// and it has no notes store nor identity.
	rh.Revoke(cx.ID) // flush the runners file once more
	h.Store.(*store.Store).Close()
	err = filepath.WalkDir(hubDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && d.Name() == "shared" {
				t.Errorf("the hub has a shared notes folder: %s", path)
			}
			return err
		}
		if d.Name() == filepath.Base(notes.IdentityPath("")) {
			t.Errorf("the hub has a shared notes identity: %s", path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(b, []byte(marker)) || bytes.Contains(b, []byte("how to build")) {
			t.Errorf("the hub kept note data in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
