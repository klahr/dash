package gitops

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/testbin"
)

func fakeGh(t *testing.T, script string) *Git {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gh")
	testbin.Write(t, bin, "#!/bin/sh\n"+script)
	return &Git{GhBin: bin}
}

func TestCheckEvents(t *testing.T) {
	t.Parallel()

	g := fakeGh(t, `echo '[{"name":"test","event":"pull_request"},{"name":"CLAAssistant","event":"pull_request_target"},{"name":"lint","event":"pull_request_target"},{"name":"lint","event":"pull_request"}]'
`)
	ev := g.CheckEvents(context.Background(), "p", t.TempDir(), 4)
	if ev["test"] != "pull_request" || !RunsFromBase(ev["CLAAssistant"]) || ev["lint"] != "pull_request" {
		t.Fatalf("events = %v", ev)
	}
}
