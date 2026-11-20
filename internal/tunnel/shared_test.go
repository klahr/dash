package tunnel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/store/storetest"
)

// sharedHub is a hub with one registered runner, which reports how Claude
// signs in on it with report.
func sharedHub(t *testing.T) (h *Hub, id string, report func(auth map[string]any)) {
	t.Helper()
	acc, owner := storetest.Accounts(t)
	h = &Hub{Accounts: acc}
	code, _, _ := h.NewCode(owner)
	id, token, _, err := h.register(code, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(h.ServeReport))
	t.Cleanup(srv.Close)
	creds := Credentials{Server: srv.URL, Token: token}
	return h, id, func(auth map[string]any) {
		t.Helper()
		claude := map[string]any{"version": "2.1.326", "checked_at": "2026-11-18T10:00:00Z"}
		if auth != nil {
			claude["auth"] = auth
		}
		if err := Report(context.Background(), creds, map[string]any{"parallel": 1, "claude": claude}); err != nil {
			t.Fatal(err)
		}
	}
}

var (
	apiKey    = map[string]any{"method": claudecli.AuthAPIKey, "key_source": claudecli.KeyEnv}
	maxLogin  = map[string]any{"method": claudecli.AuthSubscription, "subscription": "max"}
	keyAndPro = map[string]any{"method": claudecli.AuthAPIKey, "key_source": claudecli.KeyEnv, "login": "pro"}
)

// Take other members' tasks can be turned on only for a runner that runs
// Claude with an API key and no subscription login; once on, it has effect
// only while that stays so.
func TestOnlyAnAPIKeyRunnerCanBeShared(t *testing.T) {
	t.Parallel()
	h, id, report := sharedHub(t)

	if err := h.SetShared(id, true); !errors.Is(err, ErrNotAPIKey) {
		t.Fatalf("a runner that hasn't said how it signs in: %v", err)
	}
	report(maxLogin)
	if err := h.SetShared(id, true); !errors.Is(err, ErrNotAPIKey) || !strings.Contains(err.Error(), "Claude Max subscription") {
		t.Fatalf("a Max subscription: %v", err)
	}
	report(keyAndPro)
	if err := h.SetShared(id, true); !errors.Is(err, ErrNotAPIKey) || !strings.Contains(err.Error(), "Pro subscription login") {
		t.Fatalf("a key with a Pro login behind it: %v", err)
	}
	report(apiKey)
	if err := h.SetShared(id, true); err != nil {
		t.Fatal(err)
	}
	r, _ := h.Get(id)
	if ok, _ := r.TakesOthers(); !ok || !r.Shared {
		t.Fatalf("shared API-key runner: %+v", r)
	}
	other := r.OwnerID() + 100
	if ok, why := r.WorksFor([]int64{other}); !ok {
		t.Errorf("a shared runner refused another member's work: %s", why)
	}

	// It switches to a subscription: it stops taking others' work at once,
	// and says why.
	report(maxLogin)
	r, _ = h.Get(id)
	if ok, why := r.WorksFor([]int64{other}); ok || !strings.Contains(why, "takes other members' tasks, but it uses a Claude Max subscription") {
		t.Errorf("after switching to Max: %v %q", ok, why)
	}
	if ok, _ := r.WorksFor([]int64{r.OwnerID()}); !ok {
		t.Error("its owner's work stopped too")
	}
	// Turning it off is always allowed.
	if err := h.SetShared(id, false); err != nil {
		t.Fatal(err)
	}
}

// A runner only does its owner's work: anyone else's, Dash's own (0) or a
// deleted account's, it refuses, saying whose runner it is.
func TestRunnerWorksOnlyForItsOwner(t *testing.T) {
	r := Runner{Name: "laptop", Owner: &store.RunnerOwner{UserID: 7, Username: "bob"}}
	if ok, _ := r.WorksFor([]int64{7}); !ok {
		t.Error("its owner's task refused")
	}
	for _, users := range [][]int64{{8}, {7, 8}, {0}, {-1}, nil} {
		if ok, why := r.WorksFor(users); ok || why != "laptop is bob's runner and only takes bob's tasks" {
			t.Errorf("WorksFor(%v) = %v, %q", users, ok, why)
		}
	}
	if ok, _ := (Runner{Name: "orphan"}).WorksFor([]int64{0}); ok {
		t.Error("a runner of nobody's took work that is no one's")
	}
}

// Runners from before are settled once they say how Claude signs in: an
// API-key runner that other members' tasks went to stays shared; one with a
// subscription takes only its owner's tasks; one that doesn't say waits.
func TestMigrateShared(t *testing.T) {
	t.Parallel()
	others := func(Runner) bool { return true }

	h, id, report := sharedHub(t)
	h.runners[0].SharedSet = false // registered before
	if settled, _, _ := h.MigrateShared(id, others); settled {
		t.Fatal("settled before it said how Claude signs in")
	}
	report(nil) // an older runner: it doesn't say
	if settled, _, _ := h.MigrateShared(id, others); settled || len(h.Unsettled()) != 1 {
		t.Fatal("an older runner was settled")
	}
	report(apiKey)
	if settled, on, err := h.MigrateShared(id, others); !settled || !on || err != nil {
		t.Fatalf("API-key runner: settled %v on %v %v", settled, on, err)
	}
	if r, _ := h.Get(id); !r.Shared || len(h.Unsettled()) != 0 {
		t.Fatalf("not shared: %+v", r)
	}
	if settled, _, _ := h.MigrateShared(id, others); settled {
		t.Error("settled twice")
	}

	h, id, report = sharedHub(t)
	h.runners[0].SharedSet = false
	report(maxLogin)
	if settled, on, _ := h.MigrateShared(id, others); !settled || on {
		t.Fatalf("subscription runner: settled %v on %v", settled, on)
	}
	if r, _ := h.Get(id); r.Shared {
		t.Error("a subscription runner was made shared")
	}

	// An API-key runner no other member's tasks went to stays its owner's.
	h, id, report = sharedHub(t)
	h.runners[0].SharedSet = false
	report(apiKey)
	if settled, on, _ := h.MigrateShared(id, func(Runner) bool { return false }); !settled || on {
		t.Fatalf("API-key runner of its owner alone: settled %v on %v", settled, on)
	}
}

// CanShare names the runner once, and says to update it only when it runs
// another version than the hub.
func TestCanShareSaysWhy(t *testing.T) {
	t.Parallel()
	r := Runner{Name: "team-laptop", Version: "1.2.0", Reported: &Reported{Claude: &claudecli.Status{}}}
	got := r.CanShare("1.2.0")
	if got != "team-laptop can't take other members' tasks: `claude auth status` gave no answer Dash understands" {
		t.Errorf("same version: %q", got)
	}
	if got := r.CanShare("1.3.0"); !strings.Contains(got, "it runs Dash 1.2.0 and the hub 1.3.0: update it") {
		t.Errorf("older runner: %q", got)
	}
	if got := (Runner{Name: "box"}).CanShare(""); got != "box can't take other members' tasks: it hasn't said how Claude is signed in on it yet" {
		t.Errorf("nothing reported: %q", got)
	}
}
