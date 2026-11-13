package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/httpjson"
)

// updatingRunner stands in for a runner's side of an update: it records
// the versions the hub asks for and answers with status.
type updatingRunner struct {
	mu     sync.Mutex
	status int
	asked  []string
}

func (u *updatingRunner) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+UpdatePath, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Version string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		u.mu.Lock()
		u.asked = append(u.asked, req.Version)
		st := u.status
		u.mu.Unlock()
		if st == 0 {
			st = http.StatusOK
		}
		httpjson.Error(w, st, "checksum mismatch for dash_1.3.0_linux_amd64.tar.gz")
	})
	return mux
}

func (u *updatingRunner) calls() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.asked...)
}

// connectAs runs a runner with creds, reporting version, until the
// returned stop is called (or the test ends).
func (th testHub) connectAs(t *testing.T, creds Credentials, version string, h http.Handler) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	cl := &Client{Creds: creds, Handler: h, Version: version, MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond}
	go func() { _ = cl.Run(ctx); close(done) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			<-done
			eventually(t, "the hub to see it go", func() bool { r, _ := th.Get(creds.ID); return !r.Online })
		})
	}
	t.Cleanup(stop)
	eventually(t, "it to connect running "+version, func() bool {
		r, _ := th.Get(creds.ID)
		return r.Online && r.Version == version
	})
	return stop
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("waited too long for %s", what)
		}
	}
}

func (th testHub) runner(t *testing.T, id string) Runner {
	t.Helper()
	r, ok := th.Get(id)
	if !ok {
		t.Fatalf("no runner %s", id)
	}
	return r
}

// An update puts the runner in maintenance mode, which the hub keeps
// across the runner's reconnects and its own restarts. The runner is told
// to update only once it is idle, and the update ends when it connects
// running the release.
func TestUpdateWaitsUntilIdleAndSurvivesReconnects(t *testing.T) {
	t.Parallel()

	th := newTestHub(t)
	path := filepath.Join(t.TempDir(), "runners.json")
	if err := th.Keep(path); err != nil {
		t.Fatal(err)
	}
	th.Release = "1.3.0"
	creds := th.register(t, "work")
	ur := &updatingRunner{}
	stop := th.connectAs(t, creds, "1.2.0", ur.handler())

	if err := th.StartUpdate(creds.ID); err != nil {
		t.Fatal(err)
	}
	if u := th.runner(t, creds.ID).Update; u == nil || u.Phase != UpdateWaiting || u.Target != "1.3.0" || u.Auto {
		t.Fatalf("update %+v, want waiting for 1.3.0", u)
	}

	// Busy: it isn't told yet.
	busy := 2
	work := func(string) int { return busy }
	th.UpdateIdle(context.Background(), work)
	if got := ur.calls(); len(got) != 0 || th.runner(t, creds.ID).Update.Phase != UpdateWaiting {
		t.Fatalf("a busy runner was told to update: %v", got)
	}

	// The runner reconnects, and the hub restarts: still in maintenance.
	stop()
	stop = th.connectAs(t, creds, "1.2.0", ur.handler())
	if u := th.runner(t, creds.ID).Update; u == nil || u.Phase != UpdateWaiting {
		t.Fatalf("after a reconnect: update %+v", u)
	}
	again := &Hub{Accounts: th.Accounts, Release: "1.3.0"}
	if err := again.Keep(path); err != nil {
		t.Fatal(err)
	}
	if r, _ := again.Get(creds.ID); r.Update == nil || r.Update.Phase != UpdateWaiting {
		t.Fatalf("after the hub's restart: %+v", r.Update)
	}

	// Idle: told once, and updating.
	busy = 0
	th.UpdateIdle(context.Background(), work)
	eventually(t, "the runner to be told", func() bool { return len(ur.calls()) == 1 })
	if got := ur.calls(); got[0] != "1.3.0" {
		t.Errorf("asked for %v, want 1.3.0", got)
	}
	if u := th.runner(t, creds.ID).Update; u == nil || u.Phase != UpdateUpdating {
		t.Fatalf("update %+v, want updating", u)
	}
	th.UpdateIdle(context.Background(), work)
	if len(ur.calls()) != 1 {
		t.Error("told twice")
	}

	// It restarts on the release: done.
	stop()
	th.connectAs(t, creds, "1.3.0", ur.handler())
	if r := th.runner(t, creds.ID); r.Update != nil || r.UpdateError != "" {
		t.Fatalf("after updating: %+v %q", r.Update, r.UpdateError)
	}
}

// A failed update (the runner can't download, check or install it, or it
// comes back on the old version) leaves maintenance mode with the error; a
// runner busy after all is asked again later.
func TestFailedUpdateLeavesMaintenanceWithTheError(t *testing.T) {
	t.Parallel()

	th := newTestHub(t)
	th.Release = "1.3.0"
	creds := th.register(t, "work")
	ur := &updatingRunner{status: http.StatusConflict}
	stop := th.connectAs(t, creds, "1.2.0", ur.handler())
	idle := func(string) int { return 0 }

	if err := th.StartUpdate(creds.ID); err != nil {
		t.Fatal(err)
	}
	th.UpdateIdle(context.Background(), idle)
	eventually(t, "back to waiting", func() bool {
		u := th.runner(t, creds.ID).Update
		return len(ur.calls()) == 1 && u != nil && u.Phase == UpdateWaiting
	})

	ur.mu.Lock()
	ur.status = http.StatusInternalServerError
	ur.mu.Unlock()
	th.UpdateIdle(context.Background(), idle)
	eventually(t, "the update to fail", func() bool { return th.runner(t, creds.ID).Update == nil })
	if r := th.runner(t, creds.ID); r.UpdateError == "" || !strings.Contains(r.UpdateError, "checksum mismatch") {
		t.Fatalf("update error %q", r.UpdateError)
	}

	// Told to update, it comes back on the old version (put back).
	ur.mu.Lock()
	ur.status = http.StatusOK
	ur.mu.Unlock()
	if err := th.StartUpdate(creds.ID); err != nil {
		t.Fatal(err)
	}
	if th.runner(t, creds.ID).UpdateError != "" {
		t.Error("a new update kept the last one's error")
	}
	th.UpdateIdle(context.Background(), idle)
	eventually(t, "updating", func() bool { return len(ur.calls()) == 3 })
	stop()
	stop = th.connectAs(t, creds, "1.2.0", ur.handler())
	if r := th.runner(t, creds.ID); r.Update != nil || !strings.Contains(r.UpdateError, "came back running 1.2.0") {
		t.Fatalf("back on the old version: %+v %q", r.Update, r.UpdateError)
	}

	// A runner without the update route (an older Dash) can't.
	if err := th.StartUpdate(creds.ID); err != nil {
		t.Fatal(err)
	}
	stop()
	th.hubCallsFail(t, creds, http.StatusForbidden)
	if r := th.runner(t, creds.ID); r.Update != nil || !strings.Contains(r.UpdateError, "too old") {
		t.Fatalf("older runner: %+v %q", r.Update, r.UpdateError)
	}
}

// hubCallsFail connects creds' runner with a handler answering status to
// everything, and tells it to update.
func (th testHub) hubCallsFail(t *testing.T, creds Credentials, status int) {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpjson.Error(w, status, "a runner doesn't offer this to the hub")
	})
	stop := th.connectAs(t, creds, "1.2.0", h)
	defer stop()
	th.UpdateIdle(context.Background(), func(string) int { return 0 })
	eventually(t, "the update to end", func() bool { return th.runner(t, creds.ID).Update == nil })
}

// Auto-update starts the update of a runner on another version when it
// connects or when it is turned on; cancelling it holds auto-update off
// for that release. A hub built from source updates nobody.
func TestAutoUpdateAndCancel(t *testing.T) {
	t.Parallel()

	th := newTestHub(t)
	th.Release = "1.3.0"
	creds := th.register(t, "work")
	ur := &updatingRunner{}
	stop := th.connectAs(t, creds, "1.2.0", ur.handler())
	if th.runner(t, creds.ID).Update != nil {
		t.Fatal("updating without auto-update")
	}
	if err := th.SetAutoUpdate(creds.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := th.runner(t, creds.ID); !r.AutoUpdate || r.Update == nil || !r.Update.Auto {
		t.Fatalf("auto-update on: %+v", r.Update)
	}
	if err := th.CancelUpdate(creds.ID); err != nil {
		t.Fatal(err)
	}
	if err := th.CancelUpdate(creds.ID); !errors.Is(err, ErrNoUpdate) {
		t.Errorf("cancelling nothing: %v", err)
	}
	stop()
	stop = th.connectAs(t, creds, "1.2.0", ur.handler())
	if th.runner(t, creds.ID).Update != nil {
		t.Fatal("auto-update started again the update that was cancelled")
	}

	// A new release: auto-update starts it when the runner connects.
	th.mu.Lock()
	th.Release = "1.4.0"
	th.mu.Unlock()
	stop()
	stop = th.connectAs(t, creds, "1.2.0", ur.handler())
	if u := th.runner(t, creds.ID).Update; u == nil || u.Target != "1.4.0" {
		t.Fatalf("connected with auto-update: %+v", u)
	}
	// Once told to update, it can't be cancelled.
	th.UpdateIdle(context.Background(), func(string) int { return 0 })
	eventually(t, "the runner to be told", func() bool { return len(ur.calls()) == 1 })
	if err := th.CancelUpdate(creds.ID); !errors.Is(err, ErrUpdating) {
		t.Errorf("cancelling an update under way: %v", err)
	}
	stop()

	src := newTestHub(t)
	c2 := src.register(t, "other")
	if err := src.StartUpdate(c2.ID); !errors.Is(err, ErrNoRelease) {
		t.Errorf("update from a hub built from source: %v", err)
	}
	if err := src.SetAutoUpdate(c2.ID, true); err != nil || src.runner(t, c2.ID).Update != nil {
		t.Errorf("auto-update from a hub built from source: %v", err)
	}
}
