package dispatch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
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
// *scan.WaitError says none can take it now; p on labels no runner has is
// an error: it never will.
func (d *Dispatcher) Scan(ctx context.Context, p model.Project, job scan.Job, h scan.Hooks) (scan.Outcome, error) {
	fl, ok := d.Fleet.(wsFleet)
	if !ok {
		return scan.Outcome{}, errors.New("this Dash can't send scans to runners")
	}
	r, why, linked := d.scanRunner(p, []int64{job.For}, job.Account)
	if !linked {
		return scan.Outcome{}, fmt.Errorf("no runner works on %s: this hub runs Claude only on runners; give one the labels it runs on, or change them in Project settings → Runners", p.Name)
	}
	if r == nil {
		return scan.Outcome{}, &scan.WaitError{Reason: why}
	}
	// The link has its own timeout: the scan's context lasts as long as the
	// scan. A first link, which clones, goes on on the runner after this
	// gives up; the scan's next try finds it linked.
	lctx, cancel := context.WithTimeout(ctx, linkTimeout)
	err := d.link(lctx, *r, p)
	cancel()
	if err != nil {
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
// at all. A scan runs on Claude Code whatever agent p's tasks run on, so it
// needs a runner with Claude Code's label (model.Project.OnDefaultAgent).
// account, if set, takes only runners logged in to that Claude account.
func (d *Dispatcher) scanRunner(p model.Project, users []int64, account string) (r *tunnel.Runner, why string, linked bool) {
	p = p.OnDefaultAgent()
	var cands []tunnel.Runner
	var reasons, notFor []string
	for _, x := range tunnel.Serving(d.Fleet.List(), p) {
		linked = true
		if ok, why := x.WorksFor(users); !ok {
			notFor = append(notFor, why)
			continue
		}
		if account != "" && x.Account() != account {
			reasons = append(reasons, x.Name+" is on another Claude account")
			continue
		}
		switch {
		case !x.Online:
			reasons = append(reasons, x.Name+" is offline")
		case x.Update != nil:
			reasons = append(reasons, x.Name+" is updating")
		case x.Paused:
			reasons = append(reasons, x.Name+" is paused")
		default:
			cands = append(cands, x)
		}
	}
	if len(cands) == 0 {
		if len(reasons) == 0 && len(notFor) > 0 {
			return nil, d.noRunnerFor(p, users, notFor), linked
		}
		return nil, waitReason(append(reasons, notFor...)), linked
	}
	load := d.loads()
	best := cands[0]
	for _, c := range cands[1:] {
		if load[c.ID] < load[best.ID] {
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

// linkTimeout bounds the link a scan sends before it starts.
const linkTimeout = 30 * time.Second

// link makes sure runner r has a clone of p, with the hub's settings.
func (d *Dispatcher) link(ctx context.Context, r tunnel.Runner, p model.Project) error {
	_, err := d.Link(ctx, r.ID, p)
	return err
}

// Link makes sure runner id has a clone of p, with the hub's settings, and
// returns the runner's project for it: the hub forwards p's git, files and
// commands there. The runner clones p's remote the first time. A link the
// hub sent the runner lately, unchanged, isn't sent again (see linkCache).
func (d *Dispatcher) Link(ctx context.Context, id string, p model.Project) (Linked, error) {
	link := Link{Name: p.Name, RemoteURL: p.RemoteURL, CloneURL: p.CloneURL, DefaultBranch: p.DefaultBranch, WorkBranch: p.WorkBranch, OrgID: p.OrgID,
		MergeMode: p.MergeMode, MergeModeSet: p.MergeModeSet, TaskInstructions: p.TaskInstructions, Agent: p.Agent,
		AutoApprove: &p.AutoApprove, CloseNoChanges: &p.CloseNoChanges, MaxAutoFixes: &p.MaxAutoFixes,
		SetupCommand: p.SetupCommand, TestCommand: p.TestCommand, RunCommand: p.RunCommand,
		Environments: model.CloneEnvironments(p.Environments), PreviewKind: p.PreviewKind, PreviewCommand: p.PreviewCommand}
	if d.Store != nil {
		if o, err := d.Store.Accounts().Org(p.OrgID); err == nil {
			link.OrgFolder = o.ProjectFolder()
		}
	}
	if r, ok := d.Fleet.Get(id); ok {
		link.Held = HeldCommands(p, r)
		for kind := range link.Held {
			switch kind {
			case model.CmdRun:
				link.RunCommand = ""
			case model.CmdTest:
				link.TestCommand = ""
			case model.CmdDeploy:
				for i := range link.Environments {
					link.Environments[i].ClearCommands()
				}
			case model.CmdSetup:
				link.SetupCommand = ""
			case model.CmdPreview:
				link.PreviewCommand = ""
			default:
				for i, e := range link.Environments {
					if kind == model.HeldEnvKind(e.Name) {
						link.Environments[i].ClearCommands()
					}
				}
			}
		}
	}
	sum := linkSum(link)
	if out, ok := d.links.get(id, p.ID, sum, time.Now()); ok {
		return out, nil
	}
	var out Linked
	if err := d.Fleet.Call(ctx, id, http.MethodPut, "/api/hub/projects/"+url.PathEscape(p.ID), link, &out); err != nil {
		d.links.forget(id, p.ID)
		return out, err
	}
	if out.ProjectID != "" {
		d.links.put(id, p.ID, sum, out, time.Now())
	}
	return out, nil
}

// DeployElsewhere is why runner r doesn't run p's deploys to e: it lacks
// labels e deploys on (model.Environment.RunsOn).
func DeployElsewhere(e model.Environment, r tunnel.Runner) string {
	return fmt.Sprintf("the %s environment deploys only on runners with the labels %s, and runner %s lacks %s (Project settings → Deploy)",
		e.Name, strings.Join(e.RunsOn, ", "), r.Name, strings.Join(model.MissingLabels(r.AllLabels(), e.RunsOn), ", "))
}

// HeldCommands are p's commands runner r must not run, by kind, each with
// why (nil: none): set by a member who is neither an org admin nor r's
// owner, on a runner that doesn't take other members' work, or an
// environment's deploys on a runner without the labels it deploys on
// (model.HeldEnvKind, DeployElsewhere). Commands run as r's owner, on
// their computer, and could run claude on their login.
func HeldCommands(p model.Project, r tunnel.Runner) map[string]string {
	shared, _ := r.TakesOthers()
	var held map[string]string
	for _, kind := range model.CommandKinds {
		if p.Command(kind) == "" || p.CommandApproved(kind, r.OwnerID(), shared) {
			continue
		}
		by := p.CommandsBy[kind]
		who := by.Name
		if who == "" {
			who = fmt.Sprintf("user %d", by.User)
		}
		owner := "its owner"
		if r.Owner != nil && r.Owner.Username != "" {
			owner = r.Owner.Username
		}
		if held == nil {
			held = map[string]string{}
		}
		held[kind] = fmt.Sprintf("the project's %s command was last changed by %s, who is neither an org admin nor the owner of runner %s, "+
			"so it doesn't run there: an org admin or %s must approve it (Approve commands in Project settings → General)", kind, who, r.Name, owner)
	}
	if _, ok := held[model.CmdDeploy]; !ok {
		for _, e := range p.Environments {
			if e.HasCommands() && !e.DeploysOn(r.AllLabels()) {
				if held == nil {
					held = map[string]string{}
				}
				held[model.HeldEnvKind(e.Name)] = DeployElsewhere(e, r)
			}
		}
	}
	return held
}

// ScanSpare lists the Claude accounts of p's runners that could take a
// scan for the hub's user by now (online, not paused or updating), with
// each account's freshest usage and why it may not start work, if it may
// not (scan.Scheduler.Spare). Runners that haven't said which account they
// are on are left out: an early run needs to know whose usage it spends.
func (d *Dispatcher) ScanSpare(p model.Project, by int64) []scan.Spare {
	if d.Pacer == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []scan.Spare
	for _, x := range tunnel.Serving(d.Fleet.List(), p.OnDefaultAgent()) {
		acct := x.Account()
		if ok, _ := x.WorksFor([]int64{by}); !ok || !active(x) || acct == "" || seen[acct] {
			continue
		}
		seen[acct] = true
		g := d.Pacer.Account(acct)
		if g == nil {
			continue
		}
		sp := scan.Spare{Account: acct, Hold: g.Stop}
		if g.Usage != nil {
			sp.Usage = *g.Usage
		}
		if sp.Hold == "" && g.Pace.Waiting() {
			sp.Hold = g.Pace.Reason()
		}
		out = append(out, sp)
	}
	slices.SortFunc(out, func(a, b scan.Spare) int { return strings.Compare(a.Account, b.Account) })
	return out
}
