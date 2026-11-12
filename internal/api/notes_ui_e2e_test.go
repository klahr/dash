package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/notes"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/tunnel"
)

// The hub's UI shows counts of a project's shared notes per runner, never
// a note, and has the runners remove notes by a filter they apply
// themselves; a removal asked while every runner is offline waits on the
// hub (the command only) and goes to the first that connects, and a runner
// offline during a removal doesn't bring the notes back when it catches up.
func TestSharedNotesStatusAndRemovalThroughTheHub(t *testing.T) {
	parallel(t)
	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	rh.OnState = func(id string, online bool) {
		if online {
			go hub.NotesRunnerOnline(id)
		}
	}
	srv := httptest.NewServer(hub.Routes())
	defer srv.Close()
	owner := testOwner(t, hub)
	sess := sessionOf(t, hub, owner.User, owner.Org)
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.mux().ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), sess)))
	})

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
	ca, cb := register("alpha"), register("beta")
	p, err := raw(hub).CreateProject(model.Project{Name: "web", OrgID: owner.Org, Runners: []string{ca.ID, cb.ID}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := raw(hub).CreateTodo(model.Todo{ProjectID: p.ID, Title: "t", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}

	type runner struct {
		id     string
		dir    string
		notes  *notes.Service
		cancel context.CancelFunc
		done   chan struct{}
	}
	var wg sync.WaitGroup
	t.Cleanup(wg.Wait)
	start := func(creds tunnel.Credentials, dir string) *runner {
		rs, api, _ := runnerSide(t, creds.OrgID)
		// The project, linked to the runner by the hub.
		if _, err := raw(rs).CreateProject(model.Project{Name: "web", OrgID: testOwner(t, rs).Org, HubProjectID: p.ID, HubCheckout: model.HubCloned}); err != nil {
			t.Fatal(err)
		}
		wire := &notes.HubTransport{Post: func(ctx context.Context, project string, body []byte) ([]byte, error) {
			return tunnel.Notes(ctx, creds, project, body)
		}}
		ident, err := notes.LoadIdentity(notes.IdentityPath(dir))
		if err != nil {
			t.Fatal(err)
		}
		svc, err := notes.NewService(filepath.Join(dir, "shared"), creds.ID, ident, wire, func(x string) bool { return x == p.ID })
		if err != nil {
			t.Fatal(err)
		}
		rs.NotesPeer, rs.Notes = wire, svc
		ctx, cancel := context.WithCancel(context.Background())
		x := &runner{id: creds.ID, dir: dir, notes: svc, cancel: cancel, done: make(chan struct{})}
		client := &tunnel.Client{Creds: creds, Handler: api, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
			NotesKey: ident.Public.String()}
		wg.Add(1)
		go func() { defer wg.Done(); defer close(x.done); _ = client.Run(ctx) }()
		eventually(t, creds.Name+" to come online", func() bool { return rh.Online(creds.ID) })
		return x
	}
	stop := func(x *runner) {
		x.cancel()
		<-x.done
		eventually(t, "the runner to go offline", func() bool { return !rh.Online(x.id) })
		x.notes.Close()
	}
	alphaDir, betaDir := t.TempDir(), t.TempDir()
	alpha, beta := start(ca, alphaDir), start(cb, betaDir)
	for _, pair := range [][2]*runner{{alpha, beta}, {beta, alpha}} {
		if _, err := notes.Approve(filepath.Join(pair[0].dir, "shared"), pair[1].notes.Fingerprint(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := alpha.notes.CatchUp(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	keys := func(x *runner) []string {
		t.Helper()
		es, err := x.notes.List(p.ID, notes.Query{})
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
	put := func(x *runner, d notes.Draft) {
		t.Helper()
		if _, err := x.notes.Put(ctx, p.ID, d); err != nil {
			t.Fatal(err)
		}
	}
	const marker = "zq-secretish-9b1"
	put(alpha, notes.Draft{Key: "from the task " + marker, Body: "body " + marker, Tags: []string{"tag-" + marker}, AuthorTask: task.ID})
	put(beta, notes.Draft{Key: "beta wrote " + marker, Body: marker})
	put(alpha, notes.Draft{Key: "alpha wrote " + marker, Body: marker})
	put(alpha, notes.Draft{Key: "short-lived " + marker, Body: marker, TTL: time.Millisecond})
	time.Sleep(5 * time.Millisecond)
	if got := keys(beta); len(got) != 3 {
		t.Fatalf("beta has %v", got)
	}

	view := func() notesView {
		t.Helper()
		w := do(t, ui, "GET", "/api/projects/"+p.ID+"/notes", "")
		if w.Code != http.StatusOK {
			t.Fatalf("notes status: %d %s", w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), marker) {
			t.Fatalf("the status holds note content: %s", w.Body)
		}
		var v notesView
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	// Counts only, per runner, the same on both copies.
	v := view()
	if v.Online != 2 || len(v.Runners) != 2 {
		t.Fatalf("view: %+v", v)
	}
	for _, r := range v.Runners {
		st := r.Status
		if st == nil || st.Notes != 3 || st.Expired != 1 || st.Bytes == 0 || st.Newest.IsZero() || st.KeyID == "" || r.Fingerprint != st.Fingerprint {
			t.Fatalf("%s: %+v (%s)", r.Name, st, r.Error)
		}
	}
	if a, b := v.Runners[0].Status, v.Runners[1].Status; a.KeyID != b.KeyID || len(a.Vector) != len(b.Vector) {
		t.Fatalf("the copies disagree: %+v %+v", a, b)
	}

	type removed struct {
		Results []notesRemoved `json:"results"`
		Pending bool           `json:"pending"`
	}
	remove := func(body string) removed {
		t.Helper()
		w := do(t, ui, "POST", "/api/projects/"+p.ID+"/notes/remove", body)
		if w.Code != http.StatusOK {
			t.Fatalf("remove %s: %d %s", body, w.Code, w.Body)
		}
		var out removed
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	total := func(r removed) int {
		n := 0
		for _, x := range r.Results {
			if x.Error != "" {
				t.Fatalf("%s: %s", x.Name, x.Error)
			}
			n += x.Removed
		}
		return n
	}
	// A task of another project, or an unknown kind, is refused.
	if w := do(t, ui, "POST", "/api/projects/"+p.ID+"/notes/remove", `{"kind":"task","task":"nope"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("an unknown task: %d", w.Code)
	}
	if w := do(t, ui, "POST", "/api/projects/"+p.ID+"/notes/remove", `{"kind":"some"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("an unknown kind: %d", w.Code)
	}

	// By task: only the note it wrote.
	if r := remove(`{"kind":"task","task":"` + task.ID + `"}`); len(r.Results) != 2 || total(r) < 1 || r.Pending {
		t.Fatalf("by task: %+v", r)
	}
	want := []string{"alpha wrote " + marker, "beta wrote " + marker}
	for _, x := range []*runner{alpha, beta} {
		if got := keys(x); !slices.Equal(got, want) {
			t.Fatalf("%s after removing the task's notes: %v", x.id, got)
		}
	}
	// By runner: only what beta wrote.
	if r := remove(`{"kind":"runner","runner":"` + cb.ID + `"}`); total(r) < 1 {
		t.Fatalf("by runner: %+v", r)
	}
	for _, x := range []*runner{alpha, beta} {
		if got := keys(x); !slices.Equal(got, []string{"alpha wrote " + marker}) {
			t.Fatalf("%s after removing beta's notes: %v", x.id, got)
		}
	}
	// Expired: purged now, on both.
	if r := remove(`{"kind":"expired"}`); total(r) != 2 {
		t.Fatalf("expired: %+v", r)
	}
	for _, r := range view().Runners {
		if r.Status.Expired != 0 || r.Status.Notes != 1 {
			t.Fatalf("%s after removing expired: %+v", r.Name, r.Status)
		}
	}

	// Remove all while beta is offline: beta catches up without bringing
	// its copy back.
	put(beta, notes.Draft{Key: "second " + marker})
	stop(beta)
	if r := remove(`{"kind":"all"}`); total(r) != 2 || len(r.Results) != 1 {
		t.Fatalf("remove all: %+v", r)
	}
	if got := keys(alpha); len(got) != 0 {
		t.Fatalf("alpha after remove all: %v", got)
	}
	beta = start(cb, betaDir)
	if err := beta.notes.CatchUp(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	put(alpha, notes.Draft{Key: "after " + marker})
	for _, x := range []*runner{alpha, beta} {
		if got := keys(x); !slices.Equal(got, []string{"after " + marker}) {
			t.Fatalf("%s after beta caught up: %v", x.id, got)
		}
	}

	// With no runner online, a removal waits on the hub, then goes to the
	// first that connects.
	stop(alpha)
	stop(beta)
	if v := view(); v.Online != 0 {
		t.Fatalf("online: %+v", v)
	}
	r := remove(`{"kind":"all"}`)
	if !r.Pending || len(r.Results) != 0 {
		t.Fatalf("remove with nobody online: %+v", r)
	}
	if v := view(); len(v.Pending) != 1 || v.Pending[0].Kind != notes.RemoveAll || v.Pending[0].By != owner.User {
		t.Fatalf("pending: %+v", v.Pending)
	}
	beta = start(cb, betaDir)
	t.Cleanup(func() { stop(beta) })
	eventually(t, "the pending removal to reach beta", func() bool {
		got, _ := raw(hub).GetProject(p.ID)
		return len(got.NotesPending) == 0
	})
	if got := keys(beta); len(got) != 0 {
		t.Fatalf("beta after the pending removal: %v", got)
	}
	alpha = start(ca, alphaDir)
	t.Cleanup(func() { stop(alpha) })
	if err := alpha.notes.CatchUp(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if got := keys(alpha); len(got) != 0 {
		t.Fatalf("alpha after catching up with the pending removal: %v", got)
	}

	// Nobody reaches these through the hub's proxy to a runner.
	px, _ := rh.Proxy("beta")
	for _, path := range []string{notes.StatusPathFor(p.ID), notes.RemovePathFor(p.ID)} {
		req := httptest.NewRequest("POST", "/peer/beta"+path, strings.NewReader(`{"kind":"expired"}`))
		req.Header.Set(notes.CommandHeader, "1")
		w := httptest.NewRecorder()
		px.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s through the proxy: %d %s", path, w.Code, w.Body)
		}
	}
}
