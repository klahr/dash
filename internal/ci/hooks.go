package ci

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// HookPath is where GitHub delivers the webhooks Hooks registers, with
// ?org=ID (HookOrgParam) so Deliver knows whose secret signs a delivery.
const HookPath = "/api/github/hook"

// HookOrgParam is the query parameter of HookPath that names the webhook's
// org.
const HookOrgParam = "org"

// HookEvents are the events the webhooks send.
var HookEvents = []string{"workflow_run", "release"}

// Webhook states (HookStatus.State).
const (
	// HookPending: registered, no delivery yet (or not registered yet).
	HookPending = "pending"
	// HookActive: GitHub's deliveries arrive: builds are polled slowly.
	HookActive = "active"
	// HookUnreachable: registered, but GitHub's deliveries don't arrive.
	HookUnreachable = "unreachable"
	// HookNoPermission: GitHub refused to register it (403, 404): the gh
	// account has no admin rights on the repository, or its token can't
	// manage webhooks.
	HookNoPermission = "no_permission"
	// HookError: registering it failed otherwise (no runner online, say).
	HookError = "error"
)

// HookStatus is how a project's webhook is doing, for the dashboards.
type HookStatus struct {
	State        string    `json:"state"`
	Reason       string    `json:"reason,omitempty"`
	URL          string    `json:"url,omitempty"`
	LastDelivery time.Time `json:"last_delivery,omitzero"`
}

func sameHook(a, b *HookStatus) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.State == b.State && a.Reason == b.Reason
}

// HookAPI manages a repository's webhooks: gh in the project's clone (a
// *gitops.Git) or, on the hub, on a runner linked to the project.
type HookAPI interface {
	Hooks(ctx context.Context, projectID, dir string) ([]gitops.Hook, error)
	CreateHook(ctx context.Context, projectID, dir string, c gitops.HookConfig) (int64, error)
	UpdateHook(ctx context.Context, projectID, dir string, id int64, c gitops.HookConfig) error
	DeleteHook(ctx context.Context, projectID, dir string, id int64) error
	PingHook(ctx context.Context, projectID, dir string, id int64) error
}

// The pacing of webhooks (variables for tests).
var (
	// hookFresh: a webhook that delivered this recently works.
	hookFresh = 2 * time.Hour
	// hookPingAfter: a webhook quiet this long is pinged, which proves it
	// still works (GitHub sends nothing between builds).
	hookPingAfter = time.Hour
	// hookWaitFor: how long a registered or pinged webhook may take to
	// deliver before it counts as unreachable.
	hookWaitFor = 10 * time.Minute
	// hookRetryRefused: how long before trying again to register a
	// webhook GitHub refused (other failures are tried at every check).
	hookRetryRefused = 6 * time.Hour
	// hookCheckEvery: how often Hooks checks every repository's webhook.
	hookCheckEvery = 10 * time.Minute
	// hookFirstCheck: the wait before the first check, so runners can
	// connect after the hub starts.
	hookFirstCheck = time.Minute
)

// Hooks registers a webhook on each GitHub repository of the hub's
// projects, so GitHub tells the hub of workflow runs (Deliver) instead of
// the hub polling for them. Only a hub with a public URL GitHub can reach
// has one; without it, or while a webhook doesn't work, Dash polls as
// before. One webhook (and secret) per org and repository, saved in the
// store, whatever number of the org's projects use it. An org's webhook is
// registered only through its own projects' runners, and each registration
// makes a new secret: a runner is code its org runs and may change, so it
// never gets another org's secret, nor an older one of its own org.
type Hooks struct {
	Store *store.Store
	API   HookAPI
	// PublicURL is the hub's public origin (https://dash.example.com).
	PublicURL string
	Watcher   *Watcher
	Now       func() time.Time

	mu     sync.Mutex // one registration at a time
	kick   chan struct{}
	inline bool // tests: Deliver's follow-up runs before it returns
}

func (h *Hooks) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// URL is where GitHub delivers org's webhooks.
func (h *Hooks) URL(org int64) string {
	return h.legacyURL() + "?" + HookOrgParam + "=" + strconv.FormatInt(org, 10)
}

// legacyURL is where GitHub delivered before webhooks were per org. No org
// registers on it anymore: a webhook on it is the repository's earlier one,
// which the first org to register takes over rather than add a second.
func (h *Hooks) legacyURL() string { return strings.TrimRight(h.PublicURL, "/") + HookPath }

// hookKey names a webhook: an org's, of a repository.
type hookKey struct {
	org  int64
	repo string
}

// hookRepo is project p's GitHub repository, OWNER/REPO in lower case, or "".
func hookRepo(p model.Project) string {
	return strings.ToLower(gitops.GitHubOwnerRepo(p.RemoteURL))
}

// Status is how project p's webhook is doing, nil if p isn't on GitHub.
func (h *Hooks) Status(p model.Project) *HookStatus {
	repo := hookRepo(p)
	if repo == "" {
		return nil
	}
	rec, err := h.Store.GitHubHook(p.OrgID, repo)
	if err != nil {
		return &HookStatus{State: HookPending, URL: h.URL(p.OrgID),
			Reason: "Not registered yet: Dash registers it with the gh of a runner linked to the project. Until then it polls GitHub."}
	}
	return h.status(rec)
}

// status is how webhook rec is doing now.
func (h *Hooks) status(rec model.GitHubHook) *HookStatus {
	st := &HookStatus{State: rec.State, Reason: rec.Reason, URL: rec.URL, LastDelivery: rec.LastDelivery}
	if rec.HookID == 0 {
		if st.State == "" {
			st.State = HookPending
		}
		return st
	}
	now := h.now()
	since := rec.Registered
	if rec.LastPing.After(since) {
		since = rec.LastPing
	}
	answered := !rec.LastDelivery.IsZero() && !rec.LastDelivery.Before(since)
	fresh := !rec.LastDelivery.IsZero() && now.Sub(rec.LastDelivery) < hookFresh
	st.Reason = ""
	switch {
	case fresh && (answered || now.Sub(since) < hookWaitFor):
		st.State = HookActive
	case !answered && now.Sub(since) < hookWaitFor:
		st.State = HookPending
		st.Reason = "Waiting for GitHub's first delivery. Until then Dash polls GitHub."
	case answered:
		st.State = HookPending
		st.Reason = "No delivery lately: Dash pings the webhook to check it. Until then it polls GitHub."
	default:
		st.State = HookUnreachable
		st.Reason = "GitHub's deliveries to " + rec.URL + " don't arrive: check that this URL is reachable from the internet. Dash polls GitHub meanwhile."
	}
	return st
}

// Active reports whether project p's webhook works.
func (h *Hooks) Active(p model.Project) bool {
	st := h.Status(p)
	return st != nil && st.State == HookActive
}

// Run checks every repository's webhook now and then (and when kicked)
// until ctx ends.
func (h *Hooks) Run(ctx context.Context) {
	h.kicker()
	t := time.NewTimer(hookFirstCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.kick:
			// a burst of project changes: one check
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		case <-t.C:
			t.Reset(hookCheckEvery)
		}
		h.Check(ctx)
	}
}

func (h *Hooks) kicker() chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.kick == nil {
		h.kick = make(chan struct{}, 1)
	}
	return h.kick
}

// Kick asks Run to check the webhooks soon.
func (h *Hooks) Kick() {
	select {
	case h.kicker() <- struct{}{}:
	default:
	}
}

// Observe kicks a check when a project is added or changed (its remote,
// say). It is called with the store's lock held: it reads nothing.
func (h *Hooks) Observe(ev model.Event) {
	if ev.Type != model.EvProjectUpdated {
		return
	}
	if p, ok := ev.Data.(model.Project); ok && hookRepo(p) == "" {
		return
	}
	h.Kick()
}

// Check registers the webhooks repositories of projects lack, pings those
// gone quiet, and forgets those of repositories no project uses anymore.
// Each org's projects of a repository have a webhook of their own.
func (h *Hooks) Check(ctx context.Context) {
	byKey := map[hookKey][]model.Project{}
	for _, p := range h.Store.ListProjects() {
		if repo := hookRepo(p); repo != "" {
			k := hookKey{p.OrgID, repo}
			byKey[k] = append(byKey[k], p)
		}
	}
	if recs, err := h.Store.GitHubHooks(); err == nil {
		for _, rec := range recs {
			if _, used := byKey[hookKey{rec.OrgID, rec.Repo}]; !used {
				// Its project went another way than Remove: no runner of it
				// is left to delete it on GitHub.
				slog.Info("github webhook: no project uses the repository anymore", "org", rec.OrgID, "repo", rec.Repo, "hook", rec.HookID)
				_ = h.Store.DeleteGitHubHook(rec.OrgID, rec.Repo)
			}
		}
	}
	keys := make([]hookKey, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b hookKey) int {
		if a.org != b.org {
			return cmp.Compare(a.org, b.org)
		}
		return strings.Compare(a.repo, b.repo)
	})
	for _, k := range keys {
		if ctx.Err() != nil {
			return
		}
		h.checkRepo(ctx, k, byKey[k])
	}
}

// checkRepo makes sure k's repository has a working webhook for k's org,
// whose projects ps use it.
func (h *Hooks) checkRepo(ctx context.Context, k hookKey, ps []model.Project) {
	h.mu.Lock()
	defer h.mu.Unlock()
	repo := k.repo
	rec, err := h.Store.GitHubHook(k.org, repo)
	if err != nil {
		rec = model.GitHubHook{OrgID: k.org, Repo: repo}
	}
	now := h.now()
	if rec.HookID != 0 && rec.URL == h.URL(k.org) {
		last := rec.LastDelivery
		if rec.LastPing.After(last) {
			last = rec.LastPing
		}
		if now.Sub(last) < hookPingAfter {
			return
		}
		for _, p := range ps {
			if p.OrgID != rec.OrgID {
				continue // never through another org's runner
			}
			err = h.API.PingHook(ctx, p.ID, p.Path, rec.HookID)
			if err == nil {
				rec.LastPing = now
				h.put(rec)
				return
			}
			if gitops.GitHubHTTPStatus(err) == 404 {
				break // deleted on GitHub (or no rights anymore): register it again
			}
		}
		if gitops.GitHubHTTPStatus(err) != 404 {
			slog.Debug("github webhook: ping", "repo", repo, "err", err)
			return
		}
		slog.Info("github webhook: gone from GitHub, registering it again", "repo", repo, "hook", rec.HookID)
		rec.HookID = 0
	}
	if rec.HookID == 0 && rec.State == HookNoPermission && now.Sub(rec.Checked) < hookRetryRefused {
		return
	}
	h.register(ctx, rec, ps)
}

// newHookSecret makes a webhook secret.
func newHookSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// register registers (or updates) rec's webhook with the gh of the first
// of ps, rec's org's projects, that can, and saves the outcome. Each
// registration has a new secret: the one before it never goes to a runner.
// Deliveries GitHub signs with the new secret before it is saved are
// refused, so register pings the webhook once it is.
func (h *Hooks) register(ctx context.Context, rec model.GitHubHook, ps []model.Project) {
	rec.Secret = newHookSecret()
	prevID := rec.HookID
	rec.URL, rec.Checked = h.URL(rec.OrgID), h.now()
	cfg := gitops.HookConfig{URL: rec.URL, Secret: rec.Secret, Events: HookEvents}
	var err error
	for _, p := range ps {
		if p.OrgID != rec.OrgID {
			continue // never through another org's runner
		}
		var id int64
		if id, err = h.registerWith(ctx, p, prevID, cfg); err == nil {
			rec.HookID, rec.State, rec.Reason, rec.Registered, rec.LastPing = id, HookPending, "", h.now(), time.Time{}
			h.put(rec)
			slog.Info("github webhook registered", "repo", rec.Repo, "hook", id, "url", rec.URL)
			h.notify(rec.OrgID, rec.Repo)
			// Ping it now that Deliver knows the new secret: GitHub's own ping
			// of a new webhook (and an updated one's deliveries) may have come
			// before it was saved, and been refused. Its answer counts from
			// rec.Registered, so LastPing stays as it is.
			if err := h.API.PingHook(ctx, p.ID, p.Path, id); err != nil {
				slog.Debug("github webhook: ping", "repo", rec.Repo, "err", err)
			}
			return
		}
	}
	rec.HookID = 0
	if code := gitops.GitHubHTTPStatus(err); code == 403 || code == 404 {
		rec.State = HookNoPermission
		rec.Reason = fmt.Sprintf("GitHub refused it (HTTP %d): adding a webhook to %s needs admin rights on the repository, "+
			"and a gh login whose token may manage webhooks (admin:repo_hook). Dash polls GitHub instead.", code, rec.Repo)
	} else {
		rec.State = HookError
		msg := "no project of the repository to register it with"
		if err != nil {
			msg = briefErr(err)
		}
		rec.Reason = "Couldn't register it: " + msg + ". Dash polls GitHub instead, and tries again in a few minutes."
	}
	h.put(rec)
	slog.Info("github webhook not registered: polling", "repo", rec.Repo, "state", rec.State, "err", err)
	h.notify(rec.OrgID, rec.Repo)
}

// registerWith registers cfg with project p's gh: it updates the
// repository's webhook that already delivers to cfg.URL (or is prevID, or
// delivers to the URL of before webhooks were per org) instead of adding a
// second one. It doesn't ping it: register does, once the new secret is
// saved.
func (h *Hooks) registerWith(ctx context.Context, p model.Project, prevID int64, cfg gitops.HookConfig) (int64, error) {
	hooks, err := h.API.Hooks(ctx, p.ID, p.Path)
	if err != nil {
		return 0, err
	}
	for _, x := range hooks {
		if x.URL == cfg.URL || x.URL == h.legacyURL() || (prevID != 0 && x.ID == prevID) {
			if err := h.API.UpdateHook(ctx, p.ID, p.Path, x.ID, cfg); err != nil {
				return 0, err
			}
			return x.ID, nil
		}
	}
	return h.API.CreateHook(ctx, p.ID, p.Path, cfg)
}

func (h *Hooks) put(rec model.GitHubHook) {
	if err := h.Store.PutGitHubHook(rec); err != nil {
		slog.Warn("save github webhook", "repo", rec.Repo, "err", err)
	}
}

// notify shows org's projects of repo their webhook's new state.
func (h *Hooks) notify(org int64, repo string) {
	if h.Watcher == nil {
		return
	}
	for _, p := range h.Store.ListProjects() {
		if p.OrgID == org && hookRepo(p) == repo {
			h.Watcher.publish(p.ID)
		}
	}
}

// Remove deletes project p's webhook from GitHub and forgets it, unless
// another project of p's org uses the same repository. Call it before p is
// deleted: it needs p's runner.
func (h *Hooks) Remove(ctx context.Context, p model.Project) {
	repo := hookRepo(p)
	if repo == "" {
		return
	}
	for _, o := range h.Store.ListProjects() {
		if o.ID != p.ID && o.OrgID == p.OrgID && hookRepo(o) == repo {
			return // still theirs
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	rec, err := h.Store.GitHubHook(p.OrgID, repo)
	if err != nil {
		return
	}
	if rec.HookID != 0 {
		if err := h.API.DeleteHook(ctx, p.ID, p.Path, rec.HookID); err != nil && gitops.GitHubHTTPStatus(err) != 404 {
			slog.Warn("github webhook: couldn't delete it: remove it in the repository's settings (Settings → Webhooks)",
				"repo", repo, "hook", rec.HookID, "err", err)
		} else {
			slog.Info("github webhook deleted", "repo", repo, "hook", rec.HookID)
		}
	}
	_ = h.Store.DeleteGitHubHook(p.OrgID, repo)
}

// ErrHookRefused is a delivery Dash doesn't take: of a repository the org
// has no webhook for, or not signed with its secret.
var ErrHookRefused = errors.New("delivery refused")

// ValidSignature reports whether sig, an X-Hub-Signature-256 header
// ("sha256=" and the hex HMAC-SHA256 of body), is body's with secret.
func ValidSignature(secret, sig string, body []byte) bool {
	hexSum, ok := strings.CutPrefix(sig, "sha256=")
	if !ok || secret == "" {
		return false
	}
	got, err := hex.DecodeString(hexSum)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// delivery is what Deliver reads of a webhook's payload: no state, only
// what names the run (or release) to read again from GitHub.
type delivery struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	WorkflowRun struct {
		ID         int64  `json:"id"`
		HeadBranch string `json:"head_branch"`
	} `json:"workflow_run"`
	Release struct {
		TagName string `json:"tag_name"`
	} `json:"release"`
}

// Deliver takes a delivery to org's webhook (HookOrgParam) of event
// (X-GitHub-Event) signed sig (X-Hub-Signature-256): ErrHookRefused unless
// the secret of org's webhook of the repository signed it. A ping only
// shows the webhook works; a workflow_run has the run read again from
// GitHub and org's projects' builds updated (Watcher.HookRun) after Deliver
// returns; a published release has its build checked. Other events are
// taken and ignored. Other orgs' projects of the repository are left alone:
// their own webhooks tell them.
func (h *Hooks) Deliver(org int64, event, sig string, body []byte) error {
	var d delivery
	if err := json.Unmarshal(body, &d); err != nil || d.Repository.FullName == "" {
		return ErrHookRefused
	}
	repo := strings.ToLower(d.Repository.FullName)
	rec, err := h.Store.GitHubHook(org, repo)
	if err != nil || !ValidSignature(rec.Secret, sig, body) {
		return ErrHookRefused
	}
	was := h.status(rec).State
	rec.LastDelivery = h.now()
	if err := h.Store.GitHubHookDelivered(org, repo, rec.LastDelivery); err != nil {
		slog.Debug("github webhook: note delivery", "repo", repo, "err", err)
	}
	if h.status(rec).State != was {
		h.notify(org, repo)
	}
	if h.Watcher == nil {
		return nil
	}
	switch event {
	case "workflow_run":
		if id, branch := d.WorkflowRun.ID, d.WorkflowRun.HeadBranch; id != 0 {
			h.later(func(ctx context.Context) { h.Watcher.HookRun(ctx, org, repo, id, branch) })
		}
	case "release":
		if tag := d.Release.TagName; d.Action == "published" && tag != "" {
			h.later(func(context.Context) { h.Watcher.HookRelease(org, repo, tag) })
		}
	}
	return nil
}

// later runs f after Deliver answers GitHub, which waits 10 seconds at most.
func (h *Hooks) later(f func(ctx context.Context)) {
	if h.inline {
		f(context.Background())
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		f(ctx)
	}()
}
