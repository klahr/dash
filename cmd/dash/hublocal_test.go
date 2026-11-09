package main

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// New project → Local only on the hub: the runner's owner gets a new
// repository on the runner (git init and an initial commit), with no
// remote and no GitHub, as the runner's own project. A second one by the
// same name is refused rather than taking over the folder, and deleting the
// project deletes its folder on the runner too.
func TestHubLocalOnlyProject(t *testing.T) {
	h := startRunnerHub(t)
	api := h.api
	root := h.runnerArgs[slices.Index(h.runnerArgs, "-projects")+1]

	var p struct {
		ID, Name, Path string
		RemoteURL      string `json:"remote_url"`
		LocalOnly      bool   `json:"local_only"`
	}
	api.must("POST", "/api/runners/"+h.runnerID+"/projects", map[string]any{"name": "Scratch pad"}, http.StatusCreated, &p)
	if !p.LocalOnly || p.RemoteURL != "" || p.Name != "Scratch pad" || !strings.HasPrefix(p.Path, root+string(filepath.Separator)) {
		t.Fatalf("local-only project: %+v (projects folder %s)", p, root)
	}
	if log := run(t, p.Path, h.gitEnv, "git", "log", "--format=%s"); strings.TrimSpace(log) != "Initial commit (Dash)" {
		t.Errorf("its history: %q", log)
	}
	if remotes := run(t, p.Path, h.gitEnv, "git", "remote"); strings.TrimSpace(remotes) != "" {
		t.Errorf("it has remotes: %q", remotes)
	}
	// It is the runner's, not the hub's.
	var hubs []struct{ ID string }
	api.must("GET", "/api/projects", nil, http.StatusOK, &hubs)
	if slices.ContainsFunc(hubs, func(x struct{ ID string }) bool { return x.ID == p.ID }) {
		t.Error("the local-only project is on the hub")
	}
	var mine []struct{ ID string }
	api.must("GET", "/peer/laptop/api/projects", nil, http.StatusOK, &mine)
	if !slices.ContainsFunc(mine, func(x struct{ ID string }) bool { return x.ID == p.ID }) {
		t.Error("the runner doesn't list the local-only project")
	}

	if code, body := api.try("POST", "/api/runners/"+h.runnerID+"/projects", map[string]any{"name": "scratch-pad"}); code != http.StatusBadRequest ||
		!strings.Contains(body, "already exists") {
		t.Errorf("a second project in the same folder: %d %s", code, body)
	}
	if code, body := api.try("POST", "/api/runners/"+h.runnerID+"/projects", map[string]any{"name": " "}); code != http.StatusBadRequest {
		t.Errorf("no name: %d %s", code, body)
	}

	// Deleting it (without asking for the folder) deletes the folder.
	api.must("DELETE", "/peer/laptop/api/projects/"+p.ID, map[string]any{"password": h.password}, http.StatusNoContent, nil)
	if _, err := os.Stat(p.Path); !os.IsNotExist(err) {
		t.Errorf("its folder is still there: %v", err)
	}
}
