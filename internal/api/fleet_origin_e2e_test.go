package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/queue"
	"github.com/klahr/dash/internal/tunnel"
)

// fleetRunner is a runner connected to the hub in a test: its server, merge
// queue and reporter.
type fleetRunner struct {
	id       string
	s        *Server
	merges   *queue.MergeQueue
	reporter *dispatch.Reporter
}

type pubFunc func(model.Event)

func (f pubFunc) Publish(ev model.Event) { f(ev) }

// drainHandler serves h until drain, which refuses new requests and waits
// for those in flight.
type drainHandler struct {
	h       http.Handler
	mu      sync.Mutex
	drained bool
	running sync.WaitGroup
}

func (d *drainHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	if d.drained {
		d.mu.Unlock()
		http.Error(w, "stopped", http.StatusServiceUnavailable)
		return
	}
	d.running.Add(1)
	d.mu.Unlock()
	defer d.running.Done()
	d.h.ServeHTTP(w, r)
}

func (d *drainHandler) drain() {
	d.mu.Lock()
	d.drained = true
	d.mu.Unlock()
	d.running.Wait()
}

// When one runner pushes, the hub tells the project's other runners, which
// fetch at once: a clean checkout follows origin, one with uncommitted
// changes stays as it is and shows on the hub as behind, with a Pull that
// refuses until it can fast-forward.
func TestRunnersFollowEachOthersPushes(t *testing.T) {
	parallel(t)

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	var hubMu sync.Mutex
	hubChecklists := map[string]int{} // checklist.updated events the hub published, per project
	d := &dispatch.Dispatcher{Store: raw(hub), Fleet: rh, Grace: time.Minute, Pub: pubFunc(func(ev model.Event) {
		if ev.Type == model.EvChecklist {
			hubMu.Lock()
			hubChecklists[ev.ProjectID]++
			hubMu.Unlock()
		}
		hub.Hub.Publish(ev)
	})}
	rh.OnState, rh.OnRevoke, rh.OnReport = d.State, d.Revoked, d.Report
	hub.Dispatch = d
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	srv := httptest.NewServer(hub.Routes())
	defer srv.Close()

	origin := filepath.Join(t.TempDir(), "origin.git")
	git(t, filepath.Dir(origin), "init", "-q", "--bare", "-b", "main", origin)
	seed := filepath.Join(t.TempDir(), "seed")
	git(t, filepath.Dir(seed), "clone", "-q", origin, seed)
	if err := os.WriteFile(filepath.Join(seed, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-q", "-m", "first")
	git(t, seed, "push", "-q", "origin", "HEAD:main")

	owner := testOwner(t, hub)
	connect := func(name string) *fleetRunner {
		code, _, err := rh.NewCode(owner)
		if err != nil {
			t.Fatal(err)
		}
		creds, err := tunnel.Register(ctx, srv.URL, code, name, "")
		if err != nil {
			t.Fatal(err)
		}
		s, api, _ := runnerSide(t, creds.OrgID)
		// The tunnel's server doesn't wait for its handlers when it stops, so
		// track them: s.following.Add runs in a handler, and must be done
		// before s.following.Wait.
		served := &drainHandler{h: api}
		// Before runnerSide's folders go (cleanups run last first): no
		// more news over the tunnels, and the fetches the last push's news
		// started (git still writing in the clone) are done.
		stop := func() { cancel(); wg.Wait(); served.drain(); s.following.Wait() }
		t.Cleanup(stop)
		merges := queue.NewMergeQueue(raw(s), s.Git, s.Hub, &queue.TodoLog{Pub: s.Hub, Root: t.TempDir()}, 16)
		merges.WorktreeRoot = filepath.Join(t.TempDir(), "worktrees")
		s.Merges = merges
		fr := &fleetRunner{id: creds.ID, s: s, merges: merges}
		fr.reporter = &dispatch.Reporter{Store: raw(s), Every: time.Hour,
			Send: func(ctx context.Context, r dispatch.Report) error { return tunnel.Report(ctx, creds, r) },
			Checkouts: func() []dispatch.CheckoutState {
				var out []dispatch.CheckoutState
				for _, rs := range merges.Remote.Behind() {
					if p, err := raw(s).GetProject(rs.ProjectID); err == nil {
						out = append(out, dispatch.CheckoutState{HubProjectID: p.HubProjectID, Branch: rs.Branch, Behind: rs.Behind, Stuck: rs.Stuck})
					}
				}
				return out
			}}
		// As in main: remote news nudges a report.
		merges.Remote.Pub = pubFunc(func(ev model.Event) { s.Hub.Publish(ev); fr.reporter.Observe(ev) })
		merges.OnPushed = func(p model.Project, branch, sha string) {
			fr.reporter.Pushed(dispatch.Push{HubProjectID: p.HubProjectID, Branch: branch, SHA: sha})
		}
		client := &tunnel.Client{Creds: creds, Handler: served, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
		wg.Add(3)
		go func() { defer wg.Done(); merges.Run(ctx) }()
		go func() { defer wg.Done(); fr.reporter.Run(ctx) }()
		go func() { defer wg.Done(); _ = client.Run(ctx) }()
		// Cleanups run last first: stop the runner, and wait for the fetches
		// it follows origin with, before its directories above are removed.
		t.Cleanup(stop)
		eventually(t, name+" to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })
		return fr
	}
	a, b := connect("alpha"), connect("beta")

	p, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, RemoteURL: origin, DefaultBranch: "main",
		MergeMode: model.MergeLocal, MergeModeSet: true, Runners: []string{a.id, b.id}})
	if err != nil {
		t.Fatal(err)
	}
	clone := func(fr *fleetRunner) model.Project {
		t.Helper()
		linked, err := d.Link(ctx, fr.id, p)
		if err != nil {
			t.Fatal(err)
		}
		rp, err := raw(fr.s).GetProject(linked.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		return rp
	}
	pa, pb := clone(a), clone(b)

	// alpha pushes its default branch, the way Dash does (Push on the project).
	commit := func(dir, name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "-q", "-m", name)
	}
	commit(pa.Path, "one.txt")
	if err := a.merges.PushDefault(ctx, pa); err != nil {
		t.Fatal(err)
	}
	tip := git(t, origin, "rev-parse", "main")
	eventually(t, "beta's checkout to follow alpha's push", func() bool {
		_, err := os.Stat(filepath.Join(pb.Path, "one.txt")) // the files move just after the branch
		return git(t, pb.Path, "rev-parse", "main") == tip && err == nil && len(d.Checkouts(p.ID)) == 0
	})

	// beta has uncommitted changes when alpha pushes again: left alone,
	// and the hub shows it behind.
	if err := os.WriteFile(filepath.Join(pb.Path, "README"), []byte("beta's edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := git(t, pb.Path, "rev-parse", "main")
	commit(pa.Path, "two.txt")
	if err := a.merges.PushDefault(ctx, pa); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the hub to show beta behind", func() bool {
		cs := d.Checkouts(p.ID)
		return len(cs) == 1 && cs[0].Runner == b.id && cs[0].Behind == 1 && strings.Contains(cs[0].Stuck, "uncommitted")
	})
	if git(t, pb.Path, "rev-parse", "main") != before {
		t.Fatal("beta's dirty checkout was moved")
	}
	if c := d.Checkouts(p.ID); c[0].RunnerName != "beta" {
		t.Errorf("runner name %q", c[0].RunnerName)
	}

	call := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		hub.mux().ServeHTTP(w, asOwner(t, hub, httptest.NewRequest(method, path, nil)))
		return w
	}
	w := call("GET", "/api/projects/"+p.ID+"/checkouts")
	var listed []model.Checkout
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed) != 1 || listed[0].Runner != b.id {
		t.Fatalf("GET checkouts: %d %s", w.Code, w.Body)
	}
	pull := "/api/projects/" + p.ID + "/remote/pull?runner=" + b.id
	if w := call("POST", pull); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "uncommitted") {
		t.Fatalf("pull into a dirty checkout: %d %s; want 409", w.Code, w.Body)
	}
	if git(t, pb.Path, "rev-parse", "main") != before {
		t.Fatal("a refused pull moved the branch")
	}
	git(t, pb.Path, "checkout", "--", "README")
	if w := call("POST", pull); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"moved":1`) {
		t.Fatalf("pull into a clean checkout: %d %s", w.Code, w.Body)
	}
	if git(t, pb.Path, "rev-parse", "main") != git(t, origin, "rev-parse", "main") {
		t.Fatal("beta isn't at origin's main after the pull")
	}
	eventually(t, "the hub to see beta caught up", func() bool { return len(d.Checkouts(p.ID)) == 0 })
	// alpha, which pushed, was never told about its own pushes.
	if st := a.merges.Remote.Status(pa); st.Behind != 0 || st.Stuck != "" {
		t.Errorf("alpha's status %+v", st)
	}

	// A checklist edit on the hub is committed on a runner; the hub's
	// dashboards hear that TODO.md changed, to read it again.
	hubMu.Lock()
	if n := hubChecklists[p.ID]; n != 0 {
		t.Errorf("checklist news before TODO.md existed: %d", n)
	}
	hubMu.Unlock()
	w = httptest.NewRecorder()
	hub.mux().ServeHTTP(w, asOwner(t, hub, httptest.NewRequest("POST", "/api/projects/"+p.ID+"/checklist",
		strings.NewReader(`{"ops":[{"op":"add","text":"From the hub"}]}`))))
	if w.Code != http.StatusOK {
		t.Fatalf("checklist edit on the hub: %d %s", w.Code, w.Body)
	}
	eventually(t, "the hub to announce the checklist change", func() bool {
		hubMu.Lock()
		defer hubMu.Unlock()
		return hubChecklists[p.ID] > 0
	})
}

// A runner takes "origin moved" only from its hub, about a project linked
// to it, naming a branch and a commit.
func TestRunnerChecksOriginNews(t *testing.T) {
	parallel(t)

	s, h, remote := runnerSide(t, 7)
	s.Merges = queue.NewMergeQueue(raw(s), s.Git, s.Hub, &queue.TodoLog{Root: t.TempDir()}, 4)
	b, _ := json.Marshal(dispatch.Link{Name: "Shared App", RemoteURL: remote, DefaultBranch: "main", OrgID: 7})
	if w := send(h, "PUT", "/api/hub/projects/hub-p1", string(b)); w.Code/100 != 2 {
		t.Fatalf("link: %d %s", w.Code, w.Body)
	}
	news := func(project, body string) int {
		return send(h, "POST", "/api/hub/projects/"+project+"/origin", body).Code
	}
	good := `{"branch":"main","sha":"0123456789abcdef0123456789abcdef01234567"}`
	if c := news("hub-p1", good); c != http.StatusAccepted {
		t.Errorf("news of a linked project: %d", c)
	}
	s.following.Wait() // its fetch runs git in the clone; let it end before the clone goes
	if c := news("hub-other", good); c != http.StatusForbidden {
		t.Errorf("news of a project never linked: %d", c)
	}
	for _, bad := range []string{`{"branch":"main","sha":"--upload-pack=x"}`, `{"branch":"ma in","sha":"0123456789abcdef"}`, `{}`} {
		if c := news("hub-p1", bad); c != http.StatusBadRequest {
			t.Errorf("news %s: %d, want 400", bad, c)
		}
	}
	// Not over the tunnel: refused.
	w := httptest.NewRecorder()
	s.mux().ServeHTTP(w, asOwner(t, s, httptest.NewRequest("POST", "/api/hub/projects/hub-p1/origin", strings.NewReader(good))))
	if w.Code != http.StatusForbidden {
		t.Errorf("news from someone but the hub: %d", w.Code)
	}
}
