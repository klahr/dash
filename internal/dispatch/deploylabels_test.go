package dispatch

import (
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

// A project's deploy command is held on a runner without every label it
// deploys on (model.Project.DeployRunsOn), saying which it lacks, and sent
// to a runner with them; with no deploy labels, any runner of it deploys.
func TestDeployHeldOnRunnersWithoutItsLabels(t *testing.T) {
	t.Parallel()
	run := bobsRunner(1, keyAuth, false)
	run.OS, run.Arch = "linux", "amd64"
	p := model.Project{Name: "web", DeployCommand: "./deploy.sh\n./notify.sh", DeployRunsOn: []string{"gpu", "linux"}}

	held := HeldCommands(p, run)
	if why := held[model.CmdDeploy]; len(held) != 1 || !strings.Contains(why, "deploys only on runners with the labels gpu, linux") ||
		!strings.Contains(why, "bob-laptop lacks gpu") {
		t.Fatalf("held: %v", held)
	}
	run.Labels = []string{"GPU"}
	if held := HeldCommands(p, run); len(held) != 0 {
		t.Errorf("held on a runner with the labels: %v", held)
	}
	run.Labels = nil
	p.DeployRunsOn = nil
	if held := HeldCommands(p, run); len(held) != 0 {
		t.Errorf("held with no deploy labels: %v", held)
	}
	p.DeployRunsOn, p.DeployCommand = []string{"gpu"}, ""
	if held := HeldCommands(p, run); len(held) != 0 {
		t.Errorf("no deploy command, but held: %v", held)
	}
}
