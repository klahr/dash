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

func TestPRCheckInfo(t *testing.T) {
	g := fakeGh(t, `case "$*" in
*--required*) echo '[{"name":"test","event":"pull_request"}]' ;;
*) echo '[{"name":"test","event":"pull_request"},{"name":"CLAAssistant","event":"pull_request_target"},{"name":"lint","event":"pull_request"}]' ;;
esac
`)
	info, anyRequired, err := g.PRCheckInfo(context.Background(), "p", t.TempDir(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if !anyRequired || !info["test"].Required || info["lint"].Required || info["CLAAssistant"].Event != "pull_request_target" {
		t.Fatalf("info = %+v, anyRequired = %v", info, anyRequired)
	}
}

func TestPRCheckInfoNoneRequired(t *testing.T) {
	g := fakeGh(t, `case "$*" in
*--required*) echo "no required checks reported on the 'x' branch" >&2; exit 1 ;;
*) echo '[{"name":"test","event":"pull_request"}]' ;;
esac
`)
	info, anyRequired, err := g.PRCheckInfo(context.Background(), "p", t.TempDir(), 4)
	if err != nil || anyRequired || info["test"].Event != "pull_request" {
		t.Fatalf("info = %+v, anyRequired = %v, err = %v", info, anyRequired, err)
	}
}
