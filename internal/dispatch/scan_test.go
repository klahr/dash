package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/scan"
)

// wsFakeFleet is a fakeFleet that can't open websockets either.
type wsFakeFleet struct{ *fakeFleet }

func (wsFakeFleet) DialWS(context.Context, string, string) (*websocket.Conn, error) {
	return nil, errors.New("no websockets here")
}

// A scan of a project without runners fails at once; with its runners all
// offline it waits, saying no runner is online; a paused one is no
// different.
func TestScanPicksALinkedRunnerOrWaits(t *testing.T) {
	fx := newFixture(t)
	fx.d.Fleet = wsFakeFleet{fx.fleet}
	ctx := context.Background()
	var w *scan.WaitError

	_, err := fx.d.Scan(ctx, fx.project(t, fx.orgA), scan.Job{}, scan.Hooks{})
	if err == nil || errors.As(err, &w) || !strings.Contains(err.Error(), "link one") {
		t.Fatalf("unlinked: %v, want an error saying to link a runner", err)
	}

	fx.fleet.set(runnerOf("laptop", fx.orgA, false))
	p := fx.project(t, fx.orgA, "laptop")
	_, err = fx.d.Scan(ctx, p, scan.Job{}, scan.Hooks{})
	if !errors.As(err, &w) || w.Reason != NoRunnerOnline+": laptop is offline" {
		t.Fatalf("offline: %v", err)
	}

	paused := runnerOf("laptop", fx.orgA, true)
	paused.Paused = true
	fx.fleet.set(paused)
	if _, err = fx.d.Scan(ctx, p, scan.Job{}, scan.Hooks{}); !errors.As(err, &w) || !strings.Contains(w.Reason, "laptop is paused") {
		t.Fatalf("paused: %v", err)
	}

	// Online: linked, then dialed (which fails here, so it waits).
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	if _, err = fx.d.Scan(ctx, p, scan.Job{}, scan.Hooks{}); !errors.As(err, &w) || !strings.Contains(w.Reason, "no websockets here") {
		t.Fatalf("online: %v", err)
	}
	if got := fx.fleet.callsTo("laptop"); len(got) != 1 || !strings.Contains(got[0], "PUT /api/hub/projects/"+p.ID) {
		t.Errorf("calls to laptop %q, want the link", got)
	}
}
