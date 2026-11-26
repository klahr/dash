package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
)

// Project settings → Tests' time limit is kept between 0 (the default) and
// model.MaxTestTimeoutMinutes.
func TestTestTimeoutSettingIsClamped(t *testing.T) {
	parallel(t)

	s, _, p := boardServer(t)
	s.Git = gitops.New(nil)
	h := loggedIn(t, s.Routes())
	for _, c := range []struct{ send, want int }{
		{90, 90},
		{5000, model.MaxTestTimeoutMinutes},
		{-3, 0},
		{model.MaxTestTimeoutMinutes, model.MaxTestTimeoutMinutes},
	} {
		body := fmt.Sprintf(`{"test_timeout_minutes":%d}`, c.send)
		if w := do(t, h, "PATCH", "/api/projects/"+p.ID, body); w.Code != http.StatusOK {
			t.Fatalf("PATCH %s: %d %s", body, w.Code, w.Body)
		}
		if got, err := raw(s).GetProject(p.ID); err != nil || got.TestTimeoutMinutes != c.want {
			t.Errorf("sent %d: stored %d, want %d (%v)", c.send, got.TestTimeoutMinutes, c.want, err)
		}
	}
	// Other settings leave it as it is.
	do(t, h, "PATCH", "/api/projects/"+p.ID, `{"test_timeout_minutes":45}`)
	do(t, h, "PATCH", "/api/projects/"+p.ID, `{"name":"renamed"}`)
	if got, _ := raw(s).GetProject(p.ID); got.TestTimeoutMinutes != 45 {
		t.Errorf("a rename changed the limit to %d", got.TestTimeoutMinutes)
	}
}

// A runner's copy of a hub project takes the hub's test time limit,
// clamped the same way, and keeps its own when an older hub sends none.
func TestHubLinkCarriesTestTimeout(t *testing.T) {
	parallel(t)

	s, h, remote := runnerSide(t, 7)
	link := func(minutes *int) model.Project {
		t.Helper()
		b, _ := json.Marshal(dispatch.Link{Name: "Shared App", RemoteURL: remote, DefaultBranch: "main", OrgID: 7,
			TestTimeoutMinutes: minutes})
		w := send(h, "PUT", "/api/hub/projects/hub-p1", string(b))
		var linked dispatch.Linked
		if w.Code/100 != 2 || json.Unmarshal(w.Body.Bytes(), &linked) != nil {
			t.Fatalf("link: %d %s", w.Code, w.Body)
		}
		p, err := raw(s).GetProject(linked.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	ninety, huge, negative := 90, 5000, -3
	if p := link(&ninety); p.TestTimeoutMinutes != 90 {
		t.Errorf("created from the link: %d minutes, want 90", p.TestTimeoutMinutes)
	}
	if p := link(&huge); p.TestTimeoutMinutes != model.MaxTestTimeoutMinutes {
		t.Errorf("too long a limit from the hub: %d minutes, want %d", p.TestTimeoutMinutes, model.MaxTestTimeoutMinutes)
	}
	if p := link(&negative); p.TestTimeoutMinutes != 0 {
		t.Errorf("negative limit from the hub: %d minutes, want 0", p.TestTimeoutMinutes)
	}
	link(&ninety)
	if p := link(nil); p.TestTimeoutMinutes != 90 {
		t.Errorf("link from an older hub: %d minutes, want the runner's 90", p.TestTimeoutMinutes)
	}
}
