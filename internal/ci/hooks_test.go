package ci

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// fakeGH is a GitHub and HookAPI in memory: runs by id, and the
// repository's webhooks.
type fakeGH struct {
	mu      sync.Mutex
	runs    map[int64]gitops.Run
	fetched []int64 // RunByID calls
	hooks   []gitops.Hook
	secrets map[int64]string
	nextID  int64
	pings   int
	deleted []int64
	hookErr error // every hook call fails with it
	// onPing, if set, is GitHub answering a ping of webhook id (called
	// without mu held).
	onPing func(id int64, secret string)
	// sent are the secrets each project's runner was given (Create, Update).
	sent map[string][]string
	// listed, if set, is what Runs answers: a poll of the tag's runs.
	listed []gitops.Run
}

func newFakeGH() *fakeGH {
	return &fakeGH{runs: map[int64]gitops.Run{}, secrets: map[int64]string{}, nextID: 100}
}

func (f *fakeGH) Runs(context.Context, string, string, string, int) ([]gitops.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listed == nil {
		return nil, errors.New("not in this test")
	}
	return append([]gitops.Run(nil), f.listed...), nil
}
func (f *fakeGH) PushRuns(context.Context, string, string, int) ([]gitops.Run, error) {
	return nil, errors.New("not in this test")
}
func (f *fakeGH) RunsForCommit(context.Context, string, string, string, int) ([]gitops.Run, error) {
	return nil, errors.New("not in this test")
}
func (f *fakeGH) ReleaseURL(context.Context, string, string, string) (string, error) {
	return "https://github.com/o/r/releases/tag/v1.0.0", nil
}
func (f *fakeGH) RerunRun(context.Context, string, string, int64, bool) error { return nil }
func (f *fakeGH) RunFailedLog(context.Context, string, string, int64, int) string {
	return ""
}
func (f *fakeGH) RunByID(_ context.Context, _, _ string, id int64) (gitops.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched = append(f.fetched, id)
	r, ok := f.runs[id]
	if !ok {
		return gitops.Run{}, errors.New("gh: Not Found (HTTP 404)")
	}
	return r, nil
}

func (f *fakeGH) Hooks(context.Context, string, string) ([]gitops.Hook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gitops.Hook(nil), f.hooks...), f.hookErr
}
func (f *fakeGH) note(projectID, secret string) {
	if f.sent == nil {
		f.sent = map[string][]string{}
	}
	f.sent[projectID] = append(f.sent[projectID], secret)
}

func (f *fakeGH) CreateHook(_ context.Context, projectID, _ string, c gitops.HookConfig) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hookErr != nil {
		return 0, f.hookErr
	}
	f.note(projectID, c.Secret)
	f.nextID++
	f.hooks = append(f.hooks, gitops.Hook{ID: f.nextID, URL: c.URL, Events: c.Events, Active: true})
	f.secrets[f.nextID] = c.Secret
	return f.nextID, nil
}
func (f *fakeGH) UpdateHook(_ context.Context, projectID, _ string, id int64, c gitops.HookConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hookErr != nil {
		return f.hookErr
	}
	f.note(projectID, c.Secret)
	f.secrets[id] = c.Secret
	return nil
}
func (f *fakeGH) DeleteHook(_ context.Context, _, _ string, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	for i, h := range f.hooks {
		if h.ID == id {
			f.hooks = append(f.hooks[:i], f.hooks[i+1:]...)
			break
		}
	}
	return f.hookErr
}
func (f *fakeGH) PingHook(_ context.Context, _, _ string, id int64) error {
	f.mu.Lock()
	f.pings++
	err, onPing, secret := f.hookErr, f.onPing, f.secrets[id]
	f.mu.Unlock()
	if err == nil && onPing != nil {
		onPing(id, secret)
	}
	return err
}

// newHookWatcher is a watcher of one GitHub project (o/r) with Hooks on,
// at https://dash.example.com, and a clock the test moves.
func newHookWatcher(t *testing.T) (*Watcher, *Hooks, *fakeGH, model.Project, *time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), nopPub{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p, err := st.CreateProject(model.Project{Name: "p", DefaultBranch: "main", RemoteURL: "git@github.com:O/r.git", TagFormat: "vX.Y.Z"})
	if err != nil {
		t.Fatal(err)
	}
	gh := newFakeGH()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	w := &Watcher{Store: st, Git: gh, Pub: nopPub{}}
	t.Cleanup(w.stop) // before st.Close (cleanups run last first)
	h := &Hooks{Store: st, API: gh, PublicURL: "https://dash.example.com/", Watcher: w, Now: clock, inline: true}
	w.Hooks = h
	return w, h, gh, p, &now
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func runPayload(repo string, id int64, branch string) []byte {
	b, _ := json.Marshal(map[string]any{
		"action":       "completed",
		"repository":   map[string]string{"full_name": repo},
		"workflow_run": map[string]any{"id": id, "head_branch": branch, "status": "completed", "conclusion": "success"},
	})
	return b
}

func secretOf(t *testing.T, h *Hooks, repo string) string {
	t.Helper()
	rec, err := h.Store.GitHubHook(0, repo)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Secret
}

func TestValidSignature(t *testing.T) {
	body := []byte(`{"zen":"Keep it logically awesome."}`)
	good := sign("s3cret", body)
	for _, c := range []struct {
		name, secret, sig string
		want              bool
	}{
		{"valid", "s3cret", good, true},
		{"invalid", "s3cret", "sha256=" + strings.Repeat("0", 64), false},
		{"not hex", "s3cret", "sha256=zz", false},
		{"sha1 only", "s3cret", "sha1=" + good[7:], false},
		{"missing", "s3cret", "", false},
		{"wrong secret", "other", good, false},
		{"no secret", "", sign("", body), false},
	} {
		if got := ValidSignature(c.secret, c.sig, body); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDeliverChecksSignature(t *testing.T) {
	_, h, _, _, _ := newHookWatcher(t)
	h.Check(context.Background())
	secret := secretOf(t, h, "o/r")
	ping := []byte(`{"zen":"hi","hook_id":101,"repository":{"full_name":"O/r"}}`)

	if err := h.Deliver(0, "ping", "", ping); !errors.Is(err, ErrHookRefused) {
		t.Fatalf("missing signature: %v", err)
	}
	if err := h.Deliver(0, "ping", sign("wrong", ping), ping); !errors.Is(err, ErrHookRefused) {
		t.Fatalf("wrong secret: %v", err)
	}
	if err := h.Deliver(0, "ping", sign(secret, ping)[:20], ping); !errors.Is(err, ErrHookRefused) {
		t.Fatalf("invalid signature: %v", err)
	}
	if rec, _ := h.Store.GitHubHook(0, "o/r"); !rec.LastDelivery.IsZero() {
		t.Fatal("a refused delivery counted")
	}
	if err := h.Deliver(0, "ping", sign(secret, ping), ping); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if rec, _ := h.Store.GitHubHook(0, "o/r"); rec.LastDelivery.IsZero() {
		t.Fatal("the ping didn't count")
	}
	// Unknown repository: nothing to verify it with.
	other := []byte(`{"repository":{"full_name":"x/y"}}`)
	if err := h.Deliver(0, "ping", sign(secret, other), other); !errors.Is(err, ErrHookRefused) {
		t.Fatalf("unknown repo: %v", err)
	}
	// Events Dash doesn't use are taken.
	push := []byte(`{"ref":"refs/heads/main","repository":{"full_name":"o/r"}}`)
	if err := h.Deliver(0, "push", sign(secret, push), push); err != nil {
		t.Fatalf("other event: %v", err)
	}
}

func TestSecretStaysOutOfJSON(t *testing.T) {
	w, h, _, p, _ := newHookWatcher(t)
	h.Check(context.Background())
	secret := secretOf(t, h, "o/r")
	if len(secret) < 32 {
		t.Fatalf("weak secret %q", secret)
	}
	rec, _ := h.Store.GitHubHook(0, "o/r")
	for _, v := range []any{rec, w.Status(p.ID), h.Status(p)} {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), secret) {
			t.Fatalf("secret in %s", b)
		}
	}
}

func TestHookRunMovesRelease(t *testing.T) {
	for _, end := range []struct{ conclusion, state string }{{"success", ReleasePublished}, {"failure", ReleaseFailed}} {
		t.Run(end.state, func(t *testing.T) {
			releaseEvery = time.Hour // nothing polls by itself
			w, h, gh, p, _ := newHookWatcher(t)
			h.Check(context.Background())
			secret := secretOf(t, h, "o/r")
			w.WatchRelease(p.ID, "v1.0.0", "ccc")
			deliver := func(id int64, branch string) {
				t.Helper()
				body := runPayload("o/r", id, branch)
				if err := h.Deliver(0, "workflow_run", sign(secret, body), body); err != nil {
					t.Fatal(err)
				}
			}
			gh.runs[31] = gitops.Run{ID: 31, Workflow: "Release", Status: "in_progress", SHA: "ccc", Branch: "v1.0.0", Event: "push"}
			deliver(31, "v1.0.0")
			rel := w.Status(p.ID).Release
			if rel.State != ReleaseBuilding || len(rel.Runs) != 1 || rel.Runs[0].Status != "in_progress" {
				t.Fatalf("building: %+v", rel)
			}
			// The payload says it passed; GitHub, read again, decides.
			gh.runs[31] = gitops.Run{ID: 31, Workflow: "Release", Status: "completed", Conclusion: end.conclusion, SHA: "ccc", Branch: "v1.0.0", Event: "push"}
			deliver(31, "v1.0.0")
			rel = w.Status(p.ID).Release
			if rel.State != end.state {
				t.Fatalf("want %s: %+v", end.state, rel)
			}
			saved, err := w.Store.ReleaseBuild(p.ID, "v1.0.0")
			if err != nil || saved.State != end.state {
				t.Fatalf("saved: %+v %v", saved, err)
			}
			if end.state == ReleasePublished && rel.URL == "" {
				t.Fatalf("no release page: %+v", rel)
			}
		})
	}
}

// A poll that answers after a delivery, with a list GitHub made before the
// run moved on, doesn't take the run back: neither while the release still
// builds nor once it is published.
func TestStalePollAfterHookRun(t *testing.T) {
	releaseEvery = time.Hour
	w, h, gh, p, _ := newHookWatcher(t)
	h.Check(context.Background())
	secret := secretOf(t, h, "o/r")
	w.WatchRelease(p.ID, "v1.0.0", "ccc")
	deliver := func(id int64) {
		t.Helper()
		body := runPayload("o/r", id, "v1.0.0")
		if err := h.Deliver(0, "workflow_run", sign(secret, body), body); err != nil {
			t.Fatal(err)
		}
	}
	tagged := func(id int64, workflow, status, conclusion string) gitops.Run {
		return gitops.Run{ID: id, Workflow: workflow, Status: status, Conclusion: conclusion, SHA: "ccc", Branch: "v1.0.0", Event: "push"}
	}
	gh.runs[31] = tagged(31, "Release", "completed", "success")
	gh.runs[32] = tagged(32, "Build", "in_progress", "")
	deliver(32)
	deliver(31)
	if rel := w.Status(p.ID).Release; rel.State != ReleaseBuilding || len(rel.Runs) != 2 {
		t.Fatalf("building: %+v", rel)
	}
	// The poll lists 31 as it was before it finished.
	gh.listed = []gitops.Run{tagged(31, "Release", "in_progress", ""), tagged(32, "Build", "in_progress", "")}
	if _, err := w.Refresh(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	rel := w.Status(p.ID).Release
	if rel.State != ReleaseBuilding || len(rel.Runs) != 2 {
		t.Fatalf("after a stale poll: %+v", rel)
	}
	for _, r := range rel.Runs {
		if r.ID == 31 && (r.Status != "completed" || r.Conclusion != "success") {
			t.Fatalf("run 31 went back: %+v", r)
		}
	}
	// 32 finishes: published. A stale poll again leaves it so.
	gh.runs[32] = tagged(32, "Build", "completed", "success")
	deliver(32)
	if rel := w.Status(p.ID).Release; rel.State != ReleasePublished {
		t.Fatalf("want published: %+v", rel)
	}
	before, err := w.Store.ReleaseBuild(p.ID, "v1.0.0")
	if err != nil || before.State != ReleasePublished {
		t.Fatalf("saved: %+v %v", before, err)
	}
	if _, err := w.Refresh(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if rel := w.Status(p.ID).Release; rel.State != ReleasePublished {
		t.Fatalf("a stale poll moved a published release: %+v", rel)
	}
	after, err := w.Store.ReleaseBuild(p.ID, "v1.0.0")
	if err != nil || after.State != ReleasePublished || !sameRuns(after.Runs, before.Runs) {
		t.Fatalf("saved changed: %+v, was %+v (%v)", after, before, err)
	}
}

func TestHookRunDoesNotTrustPayload(t *testing.T) {
	releaseEvery = time.Hour
	w, h, gh, p, _ := newHookWatcher(t)
	h.Check(context.Background())
	secret := secretOf(t, h, "o/r")
	w.WatchRelease(p.ID, "v1.0.0", "ccc")
	// The payload names the tag, but the run is a fork's pull request.
	gh.runs[40] = gitops.Run{ID: 40, Workflow: "Release", Status: "completed", Conclusion: "failure", SHA: "ccc", Branch: "v1.0.0", Event: "pull_request"}
	body := runPayload("o/r", 40, "v1.0.0")
	if err := h.Deliver(0, "workflow_run", sign(secret, body), body); err != nil {
		t.Fatal(err)
	}
	if rel := w.Status(p.ID).Release; rel.State != ReleaseBuilding || len(rel.Runs) != 0 {
		t.Fatalf("an untrusted run counted: %+v", rel)
	}
}

func TestHookRunIgnoresOthers(t *testing.T) {
	releaseEvery = time.Hour
	w, h, gh, p, _ := newHookWatcher(t)
	h.Check(context.Background())
	secret := secretOf(t, h, "o/r")
	w.WatchRelease(p.ID, "v1.0.0", "ccc")
	// A tag not in the project's format, and a feature branch: not read.
	for i, branch := range []string{"nightly", "feature/x"} {
		body := runPayload("o/r", int64(50+i), branch)
		if err := h.Deliver(0, "workflow_run", sign(secret, body), body); err != nil {
			t.Fatalf("%s: %v", branch, err)
		}
	}
	if len(gh.fetched) != 0 {
		t.Fatalf("read runs %v", gh.fetched)
	}
	// Another repository's run, straight to the watcher: no project, no error.
	w.HookRun(context.Background(), 0, "x/y", 60, "v1.0.0")
	if len(gh.fetched) != 0 {
		t.Fatalf("read runs %v", gh.fetched)
	}
	// A run GitHub says is of another tag than the payload: left alone.
	gh.runs[61] = gitops.Run{ID: 61, Workflow: "Release", Status: "completed", Conclusion: "success", SHA: "ccc", Branch: "v2.0.0", Event: "push"}
	w.HookRun(context.Background(), 0, "o/r", 61, "v1.0.0")
	if rel := w.Status(p.ID).Release; rel.State != ReleaseBuilding || len(rel.Runs) != 0 {
		t.Fatalf("another tag's run counted: %+v", rel)
	}
}

func TestHookRunFindsNewRelease(t *testing.T) {
	releaseEvery = time.Hour
	w, h, gh, p, _ := newHookWatcher(t)
	h.Check(context.Background())
	gh.runs[70] = gitops.Run{ID: 70, Workflow: "Release", Status: "queued", SHA: "ddd", Branch: "v2.0.0", Event: "push", CreatedAt: time.Now()}
	w.HookRun(context.Background(), 0, "o/r", 70, "v2.0.0")
	rel := w.Status(p.ID).Release
	if rel == nil || rel.Tag != "v2.0.0" || rel.SHA != "ddd" || rel.State != ReleaseBuilding || !rel.Found {
		t.Fatalf("found: %+v", rel)
	}
}

func TestHookRegistration(t *testing.T) {
	ctx := context.Background()
	_, h, gh, p, now := newHookWatcher(t)
	if st := h.Status(p); st.State != HookPending {
		t.Fatalf("before: %+v", st)
	}
	h.Check(ctx)
	if len(gh.hooks) != 1 || gh.hooks[0].URL != "https://dash.example.com/api/github/hook?org=0" {
		t.Fatalf("hooks: %+v", gh.hooks)
	}
	rec, _ := h.Store.GitHubHook(0, "o/r")
	if rec.HookID != gh.hooks[0].ID || gh.secrets[rec.HookID] != rec.Secret || rec.State != HookPending {
		t.Fatalf("saved: %+v", rec)
	}
	if h.Active(p) {
		t.Fatal("active before any delivery")
	}
	// A ping arrives: it works.
	ping := []byte(`{"repository":{"full_name":"o/r"}}`)
	if err := h.Deliver(0, "ping", sign(rec.Secret, ping), ping); err != nil {
		t.Fatal(err)
	}
	if !h.Active(p) {
		t.Fatalf("after the ping: %+v", h.Status(p))
	}
	// Checking again adds nothing (the one ping is registration's).
	h.Check(ctx)
	if len(gh.hooks) != 1 || gh.pings != 1 {
		t.Fatalf("again: %+v, %d pings", gh.hooks, gh.pings)
	}
	// Quiet for a while: pinged. Unanswered: not reachable.
	*now = now.Add(hookPingAfter + time.Minute)
	h.Check(ctx)
	if gh.pings != 2 {
		t.Fatalf("pings %d", gh.pings)
	}
	*now = now.Add(hookWaitFor + time.Minute)
	if st := h.Status(p); st.State != HookUnreachable || st.Reason == "" {
		t.Fatalf("unanswered ping: %+v", st)
	}

	// Lost its saved state (a restored backup): the webhook with the
	// same URL is updated, not doubled, with a new secret.
	_ = h.Store.DeleteGitHubHook(0, "o/r")
	h.Check(ctx)
	rec2, _ := h.Store.GitHubHook(0, "o/r")
	if len(gh.hooks) != 1 || rec2.HookID != rec.HookID || gh.secrets[rec2.HookID] != rec2.Secret {
		t.Fatalf("reused: %+v %+v", gh.hooks, rec2)
	}

	// Removing the project deletes it on GitHub, and forgets it.
	h.Remove(ctx, p)
	if len(gh.deleted) != 1 || gh.deleted[0] != rec.HookID || len(gh.hooks) != 0 {
		t.Fatalf("deleted %v, left %+v", gh.deleted, gh.hooks)
	}
	if _, err := h.Store.GitHubHook(0, "o/r"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("still saved: %v", err)
	}
}

// TestHookRegistrationPingCounts: the ping after registering (a new webhook
// or an updated one) is signed with the new secret, and Deliver knows it by
// then, so the webhook works at once.
func TestHookRegistrationPingCounts(t *testing.T) {
	ctx := context.Background()
	_, h, gh, p, _ := newHookWatcher(t)
	var refused int
	gh.onPing = func(_ int64, secret string) {
		ping := []byte(`{"repository":{"full_name":"o/r"}}`)
		if err := h.Deliver(0, "ping", sign(secret, ping), ping); err != nil {
			refused++
		}
	}
	h.Check(ctx)
	if gh.pings != 1 || refused != 0 || !h.Active(p) {
		t.Fatalf("new: %d pings, %d refused, %+v", gh.pings, refused, h.Status(p))
	}
	// Registered again (its saved state lost): updated, with a new secret,
	// and pinged.
	_ = h.Store.DeleteGitHubHook(0, "o/r")
	h.Check(ctx)
	if len(gh.hooks) != 1 || gh.pings != 2 || refused != 0 || !h.Active(p) {
		t.Fatalf("updated: %+v, %d pings, %d refused, %+v", gh.hooks, gh.pings, refused, h.Status(p))
	}
}

func TestHookKeptWhileAnotherProjectUsesIt(t *testing.T) {
	ctx := context.Background()
	_, h, gh, p, _ := newHookWatcher(t)
	q, err := h.Store.CreateProject(model.Project{Name: "q", DefaultBranch: "main", RemoteURL: "https://github.com/o/r.git"})
	if err != nil {
		t.Fatal(err)
	}
	h.Check(ctx)
	if len(gh.hooks) != 1 {
		t.Fatalf("one per repository: %+v", gh.hooks)
	}
	h.Remove(ctx, p)
	if len(gh.deleted) != 0 {
		t.Fatal("deleted a webhook q still uses")
	}
	if _, err := h.Store.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	h.Remove(ctx, q)
	if len(gh.deleted) != 1 {
		t.Fatal("not deleted with the last project")
	}
}

func TestHookRefusedFallsBackToPolling(t *testing.T) {
	for _, code := range []string{"403", "404"} {
		t.Run(code, func(t *testing.T) {
			ctx := context.Background()
			w, h, gh, p, now := newHookWatcher(t)
			gh.hookErr = &gitops.CmdError{Args: []string{"gh", "api"}, Output: "gh: Forbidden (HTTP " + code + ")", Err: errors.New("exit status 1")}
			h.Check(ctx)
			st := h.Status(p)
			if st.State != HookNoPermission || !strings.Contains(st.Reason, "admin") {
				t.Fatalf("refused: %+v", st)
			}
			if w.hookActive(p) {
				t.Fatal("active")
			}
			if ws := w.Status(p.ID); ws.Hook == nil || ws.Hook.State != HookNoPermission {
				t.Fatalf("status: %+v", ws.Hook)
			}
			// Not asked again at every check...
			gh.hookErr = nil
			h.Check(ctx)
			if len(gh.hooks) != 0 {
				t.Fatal("asked again at once")
			}
			// ...but after a while, when it may have been fixed.
			*now = now.Add(hookRetryRefused + time.Minute)
			h.Check(ctx)
			if len(gh.hooks) != 1 || h.Status(p).State != HookPending {
				t.Fatalf("retried: %+v %+v", gh.hooks, h.Status(p))
			}
		})
	}
}

func TestHookOtherFailureRetried(t *testing.T) {
	ctx := context.Background()
	_, h, gh, p, _ := newHookWatcher(t)
	gh.hookErr = errors.New("no runner online")
	h.Check(ctx)
	if st := h.Status(p); st.State != HookError || !strings.Contains(st.Reason, "no runner online") {
		t.Fatalf("error: %+v", st)
	}
	gh.hookErr = nil
	h.Check(ctx)
	if len(gh.hooks) != 1 {
		t.Fatal("not retried at the next check")
	}
}

func TestPollPaceFollowsHook(t *testing.T) {
	every, slow, noBuild := 15*time.Second, 3*time.Minute, 3*time.Minute
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	withRuns := Release{Pushed: now.Add(-time.Minute), Runs: []gitops.Run{{ID: 1}}}
	if d := releaseWait(false, withRuns, now, every, slow, noBuild); d != every {
		t.Fatalf("no hook: %v", d)
	}
	if d := releaseWait(true, withRuns, now, every, slow, noBuild); d != slow {
		t.Fatalf("hook: %v", d)
	}
	// No run yet: the no_build check still comes on time.
	fresh := Release{Pushed: now.Add(-time.Minute)}
	if d := releaseWait(true, fresh, now, every, slow, noBuild); d != 2*time.Minute+every {
		t.Fatalf("hook, no runs yet: %v", d)
	}
	late := Release{Pushed: now.Add(-10 * time.Minute)}
	if d := releaseWait(true, late, now, every, slow, noBuild); d != every {
		t.Fatalf("hook, past no_build: %v", d)
	}

	// The regular poll: a building release keeps a project active (every
	// Every) only while no webhook works.
	releaseEvery = time.Hour
	w, h, _, p, _ := newHookWatcher(t)
	w.WatchRelease(p.ID, "v1.0.0", "ccc")
	if !w.active(p) {
		t.Fatal("no hook: want active")
	}
	h.Check(context.Background())
	ping := []byte(`{"repository":{"full_name":"o/r"}}`)
	if err := h.Deliver(0, "ping", sign(secretOf(t, h, "o/r"), ping), ping); err != nil {
		t.Fatal(err)
	}
	if w.active(p) {
		t.Fatal("hook active: want idle")
	}
}

// A poll keeps the webhook's status in what it answers.
func TestRefreshKeepsHookStatus(t *testing.T) {
	w, h, _, p, _ := newHookWatcher(t)
	w.AnyRemote = true
	h.Check(context.Background())
	st, _ := w.Refresh(context.Background(), p.ID)
	if st.Hook == nil || st.Hook.State != HookPending {
		t.Fatalf("hook: %+v", st.Hook)
	}
}

// A release build saved as ended, here by a webhook delivery, tells
// OnReleaseEnded's callback at once; one still building doesn't.
func TestReleaseEndedCallback(t *testing.T) {
	releaseEvery = time.Hour // nothing polls by itself
	w, h, gh, p, _ := newHookWatcher(t)
	ended := make(chan string, 4)
	w.OnReleaseEnded(func(projectID, tag string) { ended <- projectID + " " + tag })
	h.Check(context.Background())
	secret := secretOf(t, h, "o/r")
	w.WatchRelease(p.ID, "v1.0.0", "ccc")
	deliver := func() {
		t.Helper()
		body := runPayload("o/r", 31, "v1.0.0")
		if err := h.Deliver(0, "workflow_run", sign(secret, body), body); err != nil {
			t.Fatal(err)
		}
	}
	gh.runs[31] = gitops.Run{ID: 31, Workflow: "Release", Status: "in_progress", SHA: "ccc", Branch: "v1.0.0", Event: "push"}
	deliver()
	select {
	case got := <-ended:
		t.Fatalf("told while building: %s", got)
	case <-time.After(50 * time.Millisecond):
	}
	gh.runs[31] = gitops.Run{ID: 31, Workflow: "Release", Status: "completed", Conclusion: "success", SHA: "ccc", Branch: "v1.0.0", Event: "push"}
	deliver()
	select {
	case got := <-ended:
		if got != p.ID+" v1.0.0" {
			t.Fatalf("told %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not told the build ended")
	}
}

// Two orgs with projects of the same repository each have their own webhook
// and secret: one org's runner never gets the other's secret, a delivery
// signed with one org's secret counts for its own projects only, and
// removing one org's project leaves the other's webhook be.
func TestHookPerOrg(t *testing.T) {
	ctx := context.Background()
	w, h, gh, p, _ := newHookWatcher(t)
	// Another org's project of the same repository, older or not: its runner
	// may be anything its org runs.
	q, err := h.Store.CreateProject(model.Project{Name: "q", DefaultBranch: "main", RemoteURL: "https://github.com/o/r.git", OrgID: p.OrgID + 7})
	if err != nil {
		t.Fatal(err)
	}
	h.Check(ctx)
	mine, err := h.Store.GitHubHook(p.OrgID, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := h.Store.GitHubHook(q.OrgID, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if mine.Secret == theirs.Secret || mine.URL == theirs.URL || len(gh.hooks) != 2 {
		t.Fatalf("shared: %+v %+v %+v", mine, theirs, gh.hooks)
	}
	if !strings.HasSuffix(theirs.URL, "?org="+strconv.FormatInt(q.OrgID, 10)) {
		t.Fatalf("url %q", theirs.URL)
	}
	for _, sec := range gh.sent[q.ID] {
		if sec == mine.Secret {
			t.Fatal("the other org's runner got this org's secret")
		}
	}
	// Re-registering makes a new secret, never resends the old one.
	_ = h.Store.PutGitHubHook(model.GitHubHook{OrgID: q.OrgID, Repo: "o/r", Secret: theirs.Secret})
	h.Check(ctx)
	again, _ := h.Store.GitHubHook(q.OrgID, "o/r")
	if again.Secret == theirs.Secret {
		t.Fatal("secret reused")
	}

	// Their secret signs nothing of this org's.
	releaseEvery = time.Hour
	w.WatchRelease(p.ID, "v1.0.0", "ccc")
	body := runPayload("o/r", 90, "v1.0.0")
	if err := h.Deliver(p.OrgID, "workflow_run", sign(again.Secret, body), body); !errors.Is(err, ErrHookRefused) {
		t.Fatalf("other org's secret: %v", err)
	}
	if err := h.Deliver(q.OrgID, "workflow_run", sign(again.Secret, body), body); err != nil {
		t.Fatal(err)
	}
	if h.Active(p) {
		t.Fatal("their delivery made this org's webhook active")
	}
	if len(gh.fetched) != 0 {
		t.Fatalf("read runs for this org's project %v", gh.fetched)
	}

	// Removing their project deletes their webhook only.
	h.Remove(ctx, q)
	if _, err := h.Store.GitHubHook(q.OrgID, "o/r"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("theirs kept: %v", err)
	}
	if rec, err := h.Store.GitHubHook(p.OrgID, "o/r"); err != nil || rec.HookID != mine.HookID {
		t.Fatalf("mine: %+v %v", rec, err)
	}
}

// A webhook of before they were per org (no ?org=) is taken over, not
// doubled.
func TestHookTakesOverLegacy(t *testing.T) {
	_, h, gh, p, _ := newHookWatcher(t)
	gh.hooks = []gitops.Hook{{ID: 5, URL: "https://dash.example.com/api/github/hook", Active: true}}
	h.Check(context.Background())
	rec, err := h.Store.GitHubHook(p.OrgID, "o/r")
	if err != nil || rec.HookID != 5 || len(gh.hooks) != 1 || gh.secrets[5] != rec.Secret {
		t.Fatalf("took over: %+v %v %+v", rec, err, gh.hooks)
	}
}
