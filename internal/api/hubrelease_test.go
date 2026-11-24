package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/ci"
	"github.com/klahr/dash/internal/dispatch"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/tunnel"
)

// A release a runner tags for a hub project is followed by the hub's own
// CI watcher (which the hub's UI reads): it shows building right after the
// forwarded POST /tags, is saved, and ends as the runner's gh reports the
// tag's runs. GET /ci/releases lists it. Asked to deploy too, the hub
// queues the deploy itself: the runner is told only to tag.
func TestHubFollowsReleaseTaggedOnRunner(t *testing.T) {
	parallel(t)

	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	d := &dispatch.Dispatcher{Store: raw(hub), Fleet: rh, Grace: 50 * time.Millisecond}
	rh.OnState, rh.OnRevoke, rh.OnReport = d.State, d.Revoked, d.Report
	hub.Dispatch = d
	hub.CI = &ci.Watcher{Store: raw(hub), Git: RunnerGitHub{S: hub}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(hub.Routes())
	defer srv.Close()

	owner := testOwner(t, hub)
	code, _, err := rh.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := tunnel.Register(ctx, srv.URL, code, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := raw(hub).CreateProject(model.Project{Name: "shared", OrgID: owner.Org, RemoteURL: "https://github.com/o/r.git",
		DefaultBranch: "main", RunsOn: []string{"any"}})
	if err != nil {
		t.Fatal(err)
	}
	p = withEnvs(t, hub, p, model.Environment{Name: "staging", DeployCommand: "true"})

	// The runner: links the project as "rp", tags 1.4.0, and answers gh
	// calls with the tag's run, as it goes.
	const sha = "cccccccccccccccccccccccccccccccccccccccc"
	var mu sync.Mutex
	tagRun := gitops.Run{ID: 41, Workflow: "Release", Status: "in_progress", SHA: sha, Branch: "1.4.0", Event: "push",
		URL: "https://github.com/o/r/actions/runs/41", CreatedAt: time.Now()}
	runner := http.NewServeMux()
	runner.HandleFunc("PUT /api/hub/projects/{hpid}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, dispatch.Linked{ProjectID: "rp", DefaultBranch: "main"})
	})
	tagged := 0
	var tagBody map[string]any
	runner.HandleFunc("POST /api/projects/rp/tags", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tagged++
		_ = json.NewDecoder(r.Body).Decode(&tagBody)
		mu.Unlock()
		writeJSON(w, http.StatusCreated, mcptools.Release{Tag: "1.4.0", SHA: sha, Build: ci.ReleaseBuilding})
	})
	runner.HandleFunc("POST /api/hub/github/{hpid}", func(w http.ResponseWriter, r *http.Request) {
		var req dispatch.GitHubCall
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case req.Op == dispatch.GitHubRuns && req.Ref == "1.4.0", req.Op == dispatch.GitHubPushRuns:
			writeJSON(w, http.StatusOK, dispatch.GitHubAnswer{Runs: []gitops.Run{tagRun}})
		case req.Op == dispatch.GitHubRuns:
			writeJSON(w, http.StatusOK, dispatch.GitHubAnswer{Runs: []gitops.Run{}})
		default:
			writeError(w, http.StatusNotFound, "no")
		}
	})
	client := &tunnel.Client{Creds: creds, Handler: runner, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	w := httptest.NewRecorder()
	hub.mux().ServeHTTP(w, asOwner(t, hub, httptest.NewRequest("POST", "/api/projects/"+p.ID+"/tags", strings.NewReader(`{"bump":"minor","deploy":"staging"}`))))
	if w.Code != http.StatusCreated || tagged != 1 {
		t.Fatalf("tag on the runner: %d %s", w.Code, w.Body)
	}
	mu.Lock()
	if _, ok := tagBody["deploy"]; ok || tagBody["bump"] != "minor" {
		t.Errorf("the runner was told to deploy: %v", tagBody)
	}
	mu.Unlock()
	var tr taggedRelease
	if err := json.Unmarshal(w.Body.Bytes(), &tr); err != nil || tr.Tag != "1.4.0" || tr.Deploy == nil ||
		tr.Deploy.Environment != "staging" || tr.DeployError != "" {
		t.Fatalf("the hub's deploy of the tag: %v %s", err, w.Body)
	}
	if ds := raw(hub).ListDeploys(p.ID, "staging"); len(ds) != 1 || ds[0].Tag != "1.4.0" || ds[0].ID != tr.Deploy.ID {
		t.Fatalf("staging deploys on the hub: %+v", ds)
	}
	rel := hub.CI.Status(p.ID).Release
	if rel == nil || rel.Tag != "1.4.0" || rel.SHA != sha || rel.State != ci.ReleaseBuilding {
		t.Fatalf("the hub doesn't follow the runner's tag: %+v", rel)
	}
	if b, err := raw(hub).ReleaseBuild(p.ID, "1.4.0"); err != nil || b.State != ci.ReleaseBuilding {
		t.Fatalf("not saved on the hub: %v %+v", err, b)
	}

	// The tag's run passes: the hub's next check says published.
	mu.Lock()
	tagRun.Status, tagRun.Conclusion = "completed", "success"
	mu.Unlock()
	st, _ := hub.CI.Refresh(ctx, p.ID)
	if st.Release == nil || st.Release.State != ci.ReleasePublished || len(st.Release.Runs) != 1 || st.Release.Runs[0].ID != 41 {
		t.Fatalf("after the run passed: %+v", st.Release)
	}

	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		hub.mux().ServeHTTP(w, asOwner(t, hub, httptest.NewRequest("GET", "/api/projects/"+p.ID+path, nil)))
		return w
	}
	var list []model.ReleaseBuild
	if w := get("/ci/releases"); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil ||
		len(list) != 1 || list[0].Tag != "1.4.0" || list[0].State != ci.ReleasePublished {
		t.Fatalf("GET /ci/releases: %d %s", w.Code, w.Body)
	}
	var one model.ReleaseBuild
	if w := get("/ci/releases?tag=1.4.0"); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &one) != nil || one.State != ci.ReleasePublished {
		t.Fatalf("GET /ci/releases?tag=1.4.0: %d %s", w.Code, w.Body)
	}
	if w := get("/ci/releases?tag=9.9.9"); w.Code != http.StatusNotFound {
		t.Fatalf("an unknown tag: %d %s", w.Code, w.Body)
	}
}
