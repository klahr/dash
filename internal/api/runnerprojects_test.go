package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// A member's runner's own projects: its owner adds one with a name and a
// test command, anyone in the org reads its settings on the hub, and only
// the owner changes them. A shared project's clone isn't one of them.
func TestRunnerOwnProjectSettingsOnTheHub(t *testing.T) {
	parallel(t)

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	srv := httptest.NewServer(hub.Routes())
	t.Cleanup(srv.Close)

	admin := testOwner(t, hub)
	devID := member(t, hub, admin.Org, "dev", store.RoleMember)
	member(t, hub, admin.Org, "bea", store.RoleMember)
	dev := loginAs(t, hub.Routes(), "dev", "long-enough-dev")
	bea := loginAs(t, hub.Routes(), "bea", "long-enough-bea")

	code, _, err := rh.NewCode(model.Owner{User: devID, Org: admin.Org})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	creds, err := tunnel.Register(ctx, srv.URL, code, "team-laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	runner, h, _ := runnerSide(t, creds.OrgID)
	runner.HubUser = creds.UserID
	if err := runner.Git.InitRepo(ctx, "app", filepath.Join(runner.ProjectRoot, "app"), "main"); err != nil {
		t.Fatal(err)
	}
	if err := runner.Git.InitRepo(ctx, "lib", filepath.Join(runner.ProjectRoot, "lib"), "main"); err != nil {
		t.Fatal(err)
	}
	clone, err := raw(runner).CreateProject(model.Project{Name: "shared", Path: filepath.Join(runner.ProjectRoot, "lib"), DefaultBranch: "main", HubProjectID: "hubproject"})
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	client := &tunnel.Client{Creds: creds, Handler: h, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx); close(stopped) }()
	t.Cleanup(func() { cancel(); <-stopped })
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	base := "/api/runners/" + creds.ID
	// A name and a test command go with one repository only.
	if w := send(dev, "POST", base+"/repos", `{"paths":["app","lib"],"name":"App"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a name for two repositories: %d %s", w.Code, w.Body)
	}
	w := send(dev, "POST", base+"/repos", `{"paths":["app"],"name":"App","test_command":"make test"}`)
	var added AddedRepos
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &added) != nil || len(added.Added) != 1 || added.SettingsError != "" {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	p := added.Added[0]
	if p.Name != "App" || p.TestCommand != "make test" {
		t.Errorf("added %+v", p)
	}

	// Another member reads its settings, but can't change them.
	w = send(bea, "GET", base+"/projects/"+p.ID, "")
	var got model.Project
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.ID != p.ID || got.TestCommand != "make test" {
		t.Fatalf("another member reading: %d %s", w.Code, w.Body)
	}
	if w := send(bea, "PATCH", base+"/projects/"+p.ID, `{"test_command":"rm -rf ~"}`); w.Code != http.StatusForbidden {
		t.Errorf("another member changing: %d %s", w.Code, w.Body)
	}
	// The owner changes them.
	if w := send(dev, "PATCH", base+"/projects/"+p.ID, `{"test_command":"go test ./..."}`); w.Code != http.StatusOK {
		t.Fatalf("owner changing: %d %s", w.Code, w.Body)
	}
	if now, _ := raw(runner).GetProject(p.ID); now.TestCommand != "go test ./..." || now.Name != "App" {
		t.Errorf("on the runner: %+v", now)
	}

	// A shared project's clone, and paths that aren't a project id.
	for _, pid := range []string{clone.ID, "%2E%2E", "a%2Fb", "nope"} {
		if w := send(dev, "GET", base+"/projects/"+pid, ""); w.Code != http.StatusNotFound {
			t.Errorf("reading %s: %d %s", pid, w.Code, w.Body)
		}
		if w := send(dev, "PATCH", base+"/projects/"+pid, `{"name":"x"}`); w.Code != http.StatusNotFound {
			t.Errorf("changing %s: %d %s", pid, w.Code, w.Body)
		}
	}
	if now, _ := raw(runner).GetProject(clone.ID); now.Name != "shared" {
		t.Errorf("the clone was renamed: %+v", now)
	}
}
