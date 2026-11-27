package dispatch

import (
	"testing"

	"github.com/klahr/dash/internal/doctor"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tunnel"
)

// A runner whose doctor found a tool of the project's commands missing
// comes after one that has them, however idle it is; one that hasn't
// reported its checks counts as having them, and the runner a task
// continues on keeps it.
func TestPreferRunnerWithProjectTools(t *testing.T) {
	missing := &doctor.Report{Checks: []doctor.Check{{Group: doctor.GroupProject, Name: "go", Project: "hp1", Status: doctor.Warning}}}
	lacking := tunnel.Runner{ID: "a", Doctor: missing}
	busy := tunnel.Runner{ID: "b"}
	limit := func(tunnel.Runner) int { return 4 }
	load := map[string]int{"a": 0, "b": 3}
	level := map[string]int{"a": 0, "b": 0}
	task := model.Todo{ProjectID: "hp1"}

	if r := prefer([]tunnel.Runner{lacking, busy}, task, nil, load, limit, level); r.ID != "b" {
		t.Errorf("picked %s, want b: a lacks go", r.ID)
	}
	other := model.Todo{ProjectID: "hp2"}
	if r := prefer([]tunnel.Runner{lacking, busy}, other, nil, load, limit, level); r.ID != "a" {
		t.Errorf("picked %s for another project, want the idle a", r.ID)
	}
	task.Runner = "a"
	if r := prefer([]tunnel.Runner{lacking, busy}, task, nil, load, limit, level); r.ID != "a" {
		t.Errorf("picked %s, want a: the task continues there", r.ID)
	}
}
