package dispatch

import (
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// An environment's deploys are held on a runner without every label it
// deploys on (model.Environment.RunsOn), saying which it lacks, and not on
// a runner with them; an environment with no labels deploys on any runner
// of the project. Each environment is held on its own.
func TestDeployHeldOnRunnersWithoutItsLabels(t *testing.T) {
	t.Parallel()
	run := bobsRunner(1, keyAuth, false)
	run.OS, run.Arch = "linux", "amd64"
	p := model.Project{Name: "web", Environments: []model.Environment{
		{Name: "production", DeployCommand: "./deploy.sh\n./notify.sh", RunsOn: []string{"gpu", "linux"}},
		{Name: "staging", DeployCommand: "./stage.sh"},
	}}

	held := HeldCommands(p, run)
	if why := held[model.HeldEnvKind("production")]; len(held) != 1 || !strings.Contains(why, "deploys only on runners with the labels gpu, linux") ||
		!strings.Contains(why, "bob-laptop lacks gpu") {
		t.Fatalf("held: %v", held)
	}
	run.Labels = []string{"GPU"}
	if held := HeldCommands(p, run); len(held) != 0 {
		t.Errorf("held on a runner with the labels: %v", held)
	}
	run.Labels = nil
	p.Environments[0].RunsOn = nil
	if held := HeldCommands(p, run); len(held) != 0 {
		t.Errorf("held with no deploy labels: %v", held)
	}
}

// Changing an environment changes the project's deploy command as a
// whole: one set by a member who may not set it for this runner holds
// every environment.
func TestDeployCommandCoversEveryEnvironment(t *testing.T) {
	t.Parallel()
	p := model.Project{Environments: []model.Environment{{Name: "production", DeployCommand: "./d"}}}
	before := p.Command(model.CmdDeploy)
	p.Environments[0].RunsOn = []string{"prod"}
	if p.Command(model.CmdDeploy) == before {
		t.Error("changing an environment's labels didn't change the deploy command")
	}
	cp := p
	cp.SetCommand(model.CmdDeploy, "")
	if cp.Environments[0].DeployCommand != "" || p.Environments[0].DeployCommand != "./d" {
		t.Errorf("held: %+v; the original: %+v", cp.Environments, p.Environments)
	}
}
