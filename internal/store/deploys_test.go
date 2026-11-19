package store

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// 0035_deploys.sql turns a project's single deploy command (and the runner
// labels it ran on) into its environment "production", leaves a project
// with environments, or none, as it is, and drops the old fields.
func TestDeployMigrationMakesProductionEnvironment(t *testing.T) {
	t.Parallel()
	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	i := -1
	for k, m := range ms {
		if m.name == "0035_deploys.sql" {
			i = k
		}
	}
	if i < 0 {
		t.Fatal("no 0035_deploys.sql")
	}
	path := filepath.Join(t.TempDir(), "dash.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms[:i] {
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = " + strconv.Itoa(i)); err != nil {
		t.Fatal(err)
	}
	// Written as an older Dash wrote them: raw JSON with the old fields.
	rows := map[string]string{
		"web":   `{"id":"web","name":"web","deploy_command":"set -e\n./deploy.sh","deploy_runs_on":["prod","linux"]}`,
		"cli":   `{"id":"cli","name":"cli","deploy_command":"make ship"}`,
		"none":  `{"id":"none","name":"none","deploy_runs_on":["gpu"]}`,
		"newer": `{"id":"newer","name":"newer","deploy_command":"old","environments":[{"name":"staging","deploy_command":"./stage.sh"}]}`,
	}
	for id, data := range rows {
		if _, err := db.Exec("INSERT INTO projects (id, data) VALUES (?, ?)", id, data); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	get := func(id string) model.Project {
		t.Helper()
		p, err := s.GetProject(id)
		if err != nil {
			t.Fatal(err)
		}
		if p.DeployCommand != "" || p.DeployRunsOn != nil {
			t.Errorf("%s keeps the old fields: %q %v", id, p.DeployCommand, p.DeployRunsOn)
		}
		return p
	}
	web := get("web")
	if len(web.Environments) != 1 {
		t.Fatalf("web: %+v", web.Environments)
	}
	if e := web.Environments[0]; e.Name != "production" || e.DeployCommand != "set -e\n./deploy.sh" || strings.Join(e.RunsOn, ",") != "prod,linux" {
		t.Errorf("web's environment: %+v", e)
	}
	if cli := get("cli"); len(cli.Environments) != 1 || cli.Environments[0].DeployCommand != "make ship" || len(cli.Environments[0].RunsOn) != 0 {
		t.Errorf("cli: %+v", cli.Environments)
	}
	if none := get("none"); len(none.Environments) != 0 {
		t.Errorf("a project with no deploy command got environments: %+v", none.Environments)
	}
	if newer := get("newer"); len(newer.Environments) != 1 || newer.Environments[0].Name != "staging" {
		t.Errorf("a project with environments: %+v", newer.Environments)
	}
	// The rows themselves no longer have the old fields.
	var raw string
	if err := s.db.QueryRow("SELECT data FROM projects WHERE id = 'web'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal([]byte(raw), &m)
	if _, ok := m["deploy_command"]; ok {
		t.Errorf("the row keeps deploy_command: %s", raw)
	}
}

// A project with a deploy command that the SQL migration didn't see (a
// state.json imported after it ran) is moved over when the store opens.
func TestMigrateDeployOnOpen(t *testing.T) {
	t.Parallel()
	p := model.Project{DeployCommand: "./ship", DeployRunsOn: []string{"prod"}}
	if !p.MigrateDeploy() || len(p.Environments) != 1 || p.Environments[0].Name != model.DefaultEnvironment ||
		p.Environments[0].RunsOn[0] != "prod" || p.DeployCommand != "" || p.DeployRunsOn != nil {
		t.Fatalf("migrated: %+v", p)
	}
	if p.MigrateDeploy() {
		t.Error("migrated twice")
	}
}

// Deploys are queued, move on, keep their logs apart from the list, and
// go with their project.
func TestDeployRecords(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "dash.db")
	s := openTest(t, path)
	p, err := s.CreateProject(model.Project{Name: "web", Environments: []model.Environment{{Name: "production", DeployCommand: "./d"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDeploy(model.Deploy{ProjectID: p.ID, Environment: "staging"}); err == nil {
		t.Error("a deploy to an environment the project hasn't")
	}
	d, err := s.CreateDeploy(model.Deploy{ProjectID: p.ID, Environment: "production", Trigger: model.DeployByUser})
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != model.DeployQueued || len(d.Phases) != 1 {
		t.Fatalf("new deploy: %+v", d)
	}
	if _, err := s.UpdateDeploy(d.ID, func(x *model.Deploy) error {
		x.Commit = "abc"
		x.SetStatus(model.DeployDeploying, x.CreatedAt)
		x.SetStatus(model.DeployLive, x.CreatedAt)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDeployLog(d.ID, "hello\n"); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, s, path)
	got, err := s.GetDeploy(d.ID)
	if err != nil || got.Log != "hello\n" || got.Status != model.DeployLive || got.StartedAt == nil || got.FinishedAt == nil {
		t.Fatalf("after reopening: %+v %v", got, err)
	}
	if list := s.ListDeploys(p.ID, ""); len(list) != 1 || list[0].Log != "" {
		t.Errorf("list: %+v", list)
	}
	if live, ok := s.LiveDeploy(p.ID, "production"); !ok || live.ID != d.ID {
		t.Errorf("live: %+v %v", live, ok)
	}
	if _, err := s.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDeploy(d.ID); err == nil {
		t.Error("a deleted project's deploy is still there")
	}
}

// Only the newest KeepDeploys finished deploys of an environment are kept,
// and the live one.
func TestOldDeploysGo(t *testing.T) {
	t.Parallel()
	s := openTest(t, filepath.Join(t.TempDir(), "dash.db"))
	p, err := s.CreateProject(model.Project{Name: "web", Environments: []model.Environment{{Name: "production", DeployCommand: "./d"}}})
	if err != nil {
		t.Fatal(err)
	}
	var first string
	for n := 0; n < KeepDeploys+3; n++ {
		d, err := s.CreateDeploy(model.Deploy{ProjectID: p.ID, Environment: "production"})
		if err != nil {
			t.Fatal(err)
		}
		st := model.DeployFailed
		if n == 0 {
			first, st = d.ID, model.DeployLive
		}
		if _, err := s.UpdateDeploy(d.ID, func(x *model.Deploy) error { x.SetStatus(st, x.CreatedAt); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	list := s.ListDeploys(p.ID, "production")
	if len(list) != KeepDeploys+2 { // the newest one isn't finished when the next is queued; the live one stays
		t.Errorf("%d deploys kept", len(list))
	}
	if _, err := s.GetDeploy(first); err != nil {
		t.Errorf("the live deploy went: %v", err)
	}
}
