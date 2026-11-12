package runnerctl

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/notes"
)

// dash runner notes shows this runner's fingerprint; approve and revoke
// write what the running runner reads.
func TestNotesCommand(t *testing.T) {
	data := t.TempDir()
	var out, errOut bytes.Buffer
	c := &CLI{Home: t.TempDir(), Out: &out, Err: &errOut}
	run := func(args ...string) int {
		out.Reset()
		errOut.Reset()
		return c.Main(context.Background(), "notes", append(args, "-data", data))
	}
	if code := run(); code != 0 {
		t.Fatalf("show: %d %s", code, errOut.String())
	}
	id, err := notes.ReadIdentity(notes.IdentityPath(data))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), id.Fingerprint()) {
		t.Fatalf("show doesn't print the fingerprint:\n%s", out.String())
	}

	peer, _ := notes.NewIdentity()
	fp := peer.Fingerprint()
	if code := run("approve", strings.ToLower(strings.ReplaceAll(fp, "-", ""))); code != 0 {
		t.Fatalf("approve: %s", errOut.String())
	}
	a, _ := notes.ReadApprovals(filepath.Join(data, "shared"))
	if _, ok := a.Approved[fp]; !ok {
		t.Fatalf("not approved: %+v", a)
	}
	if code := run(); code != 0 || !strings.Contains(out.String(), fp) {
		t.Fatalf("show after approve:\n%s", out.String())
	}
	if code := run("approve", id.Fingerprint()); code == 0 || !strings.Contains(errOut.String(), "own fingerprint") {
		t.Fatalf("approving itself: %s", errOut.String())
	}
	if code := run("approve", "nonsense"); code == 0 {
		t.Fatal("approved a bad fingerprint")
	}
	if code := run("revoke", fp); code != 0 {
		t.Fatalf("revoke: %s", errOut.String())
	}
	a, _ = notes.ReadApprovals(filepath.Join(data, "shared"))
	if _, ok := a.Approved[fp]; ok || a.Revoked[fp].IsZero() || a.Revoked[fp].After(time.Now()) {
		t.Fatalf("not revoked: %+v", a)
	}
}
