package gitops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func fakeGh(t *testing.T, script string) *Git {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Git{GhBin: bin}
}

func TestCheckEvents(t *testing.T) {
	g := fakeGh(t, `echo '[{"name":"test","event":"pull_request"},{"name":"CLAAssistant","event":"pull_request_target"},{"name":"lint","event":"pull_request_target"},{"name":"lint","event":"pull_request"}]'
`)
	ev := g.CheckEvents(context.Background(), "p", t.TempDir(), 4)
	if ev["test"] != "pull_request" || !RunsFromBase(ev["CLAAssistant"]) || ev["lint"] != "pull_request" {
		t.Fatalf("events = %v", ev)
	}
}
