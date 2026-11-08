package dispatch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/scan"
	"github.com/klahr/dash/internal/tunnel"
)

// ScanPath is a runner's websocket that runs a hub's scan (scan.Request in,
// scan.Message out).
const ScanPath = "/ws/hub/scan"

// wsFleet is a Fleet that also opens websockets to runners (a *tunnel.Hub).
type wsFleet interface {
	DialWS(ctx context.Context, id, path string) (*websocket.Conn, error)
}

// Scan runs a scan's Claude work on one of p's runners online, which
// checks the commit out in its clone of p (implementing scan.Remote). A
// *scan.WaitError says none can take it now; p not linked to any runner
// is an error: it never will.
func (d *Dispatcher) Scan(ctx context.Context, p model.Project, job scan.Job, h scan.Hooks) (scan.Outcome, error) {
	fl, ok := d.Fleet.(wsFleet)
	if !ok {
		return scan.Outcome{}, errors.New("this Dash can't send scans to runners")
	}
	r, why, linked := d.scanRunner(p)
	if !linked {
		return scan.Outcome{}, fmt.Errorf("no runner works on %s: this hub runs Claude only on runners; link one to it in Project settings → Runners", p.Name)
	}
	if r == nil {
		return scan.Outcome{}, &scan.WaitError{Reason: why}
	}
	if err := d.link(ctx, *r, p); err != nil {
		return scan.Outcome{}, &scan.WaitError{Reason: "waiting for a runner: " + err.Error()}
	}
	conn, err := fl.DialWS(ctx, r.ID, ScanPath)
	if err != nil {
		return scan.Outcome{}, &scan.WaitError{Reason: "waiting for a runner: " + err.Error()}
	}
	defer conn.Close()
	// Canceling the run (the user, Dash shutting down) closes the socket,
	// which stops Claude on the runner.
	defer context.AfterFunc(ctx, func() { conn.Close() })()
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if err := conn.WriteJSON(scan.Request{Project: p.ID, Job: job}); err != nil {
		return scan.Outcome{}, &scan.WaitError{Reason: "waiting for a runner: " + r.Name + ": " + err.Error()}
	}
	if h.Log != nil {
		h.Log("→ on runner " + r.Name)
	}
	for {
		var m scan.Message
		if err := conn.ReadJSON(&m); err != nil {
			if ctx.Err() != nil {
				return scan.Outcome{}, ctx.Err()
			}
			return scan.Outcome{}, fmt.Errorf("lost the connection to %s", r.Name)
		}
		switch {
		case m.Error != "":
			out := scan.Outcome{}
			if m.Done != nil {
				out = *m.Done
			}
			return out, fmt.Errorf("%s: %s", r.Name, m.Error)
		case m.Done != nil:
			return *m.Done, nil
		case m.Target != nil:
			if h.Target != nil {
				h.Target(*m.Target)
			}
		case m.Event != nil:
			if h.Event != nil {
				h.Event(*m.Event)
			}
		case m.Log != "":
			if h.Log != nil {
				h.Log(m.Log)
			}
		}
	}
}

// scanRunner picks the runner for a scan of p: the least busy of p's
// runners that are online and not paused. linked is false when p has none
// at all.
func (d *Dispatcher) scanRunner(p model.Project) (r *tunnel.Runner, why string, linked bool) {
	var cands []tunnel.Runner
	var reasons []string
	for _, id := range p.Runners {
		x, ok := d.Fleet.Get(id)
		if !ok || x.OrgID() != p.OrgID {
			continue
		}
		linked = true
		switch {
		case !x.Online:
			reasons = append(reasons, x.Name+" is offline")
		case x.Paused:
			reasons = append(reasons, x.Name+" is paused")
		default:
			cands = append(cands, x)
		}
	}
	if len(cands) == 0 {
		return nil, waitReason(reasons), linked
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if d.Busy(c.ID) < d.Busy(best.ID) {
			best = c
		}
	}
	return &best, "", true
}

// waitReason says why no runner can take work now, from each runner's
// reason: "no runner online" when they are all offline.
func waitReason(reasons []string) string {
	offline := len(reasons) > 0
	for _, r := range reasons {
		offline = offline && strings.HasSuffix(r, " is offline")
	}
	if offline {
		return NoRunnerOnline + ": " + strings.Join(reasons, "; ")
	}
	return "waiting for a runner: " + strings.Join(reasons, "; ")
}

// NoRunnerOnline starts the waiting reason of work none of whose runners
// is online.
const NoRunnerOnline = "no runner online"

// link makes sure runner r has a clone of p, with the hub's settings.
func (d *Dispatcher) link(ctx context.Context, r tunnel.Runner, p model.Project) error {
	link := Link{Name: p.Name, RemoteURL: p.RemoteURL, DefaultBranch: p.DefaultBranch, WorkBranch: p.WorkBranch, OrgID: p.OrgID,
		MergeMode: p.MergeMode, MergeModeSet: p.MergeModeSet, MergeGreenPRs: p.MergeGreenPRs, TaskInstructions: p.TaskInstructions}
	return d.Fleet.Call(ctx, r.ID, http.MethodPut, "/api/hub/projects/"+url.PathEscape(p.ID), link, nil)
}
