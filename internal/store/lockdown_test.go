package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

// lockdownFixture is a Dash with two accounts, signed in, with a token,
// a runner, an invitation, a reset link, a wall link and one of each secret.
type lockdownFixture struct {
	s          *Store
	acc        *Accounts
	admin, ann int64
	secrets    secretFixture
	now        time.Time
}

const annPassword = "ann-password-1"

func newLockdownFixture(t *testing.T) lockdownFixture {
	t.Helper()
	s := openTest(t, filepath.Join(t.TempDir(), "dash.db"))
	s.SetSealer(newTestSealer(t))
	f := lockdownFixture{s: s, acc: s.Accounts(), now: time.Now()}
	f.secrets = writeSecrets(t, s) // the admin, with two-factor sign-in
	f.admin = f.secrets.uid
	def, err := f.acc.DefaultOrg()
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := f.acc.InviteWith(f.admin, def.ID, "ann@example.com", RoleMember, true)
	if err != nil {
		t.Fatal(err)
	}
	u, _, err := f.acc.RegisterWithInvitation(tok, "ann", annPassword)
	if err != nil {
		t.Fatal(err)
	}
	f.ann = u.ID
	if _, _, err := f.acc.InviteWith(f.admin, def.ID, "bob@example.com", RoleMember, true); err != nil {
		t.Fatal(err)
	}
	// The admin's sign-in is two sessions of one family (a base domain's
	// and an org subdomain's); ann's is one of her own.
	for _, x := range []struct {
		key, family string
		uid         int64
	}{{"session-a", "fam-a", f.admin}, {"session-a2", "fam-a", f.admin}, {"session-b", "fam-b", f.ann}} {
		if err := f.acc.CreateSessionVia(x.key, x.uid, 0, x.family, MethodPassword, f.now, f.now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := f.acc.CreateToken(NewToken{Kind: TokenAPI, Name: "ci", UserID: f.ann, OrgID: def.ID, Scopes: []string{ScopeRead}}, f.now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.acc.CreateToken(NewToken{Kind: TokenRunner, Name: "laptop", UserID: f.ann, OrgID: def.ID, RunnerID: "r1"}, f.now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.acc.CreateWallToken(def.ID, f.admin, "TV", false, f.now); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.acc.AdminResetLink(f.ann, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavePushSubscription(model.PushSubscription{UserID: f.ann, OrgID: def.ID, Endpoint: "https://push.example.com/1", P256dh: "k", Auth: "a"}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f lockdownFixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.s.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(q, err)
	}
	return n
}

func newVAPID() (string, error) { return "new-vapid-key", nil }

func TestLockdownSessions(t *testing.T) {
	t.Parallel()
	f := newLockdownFixture(t)
	res, err := f.acc.Lockdown(Lockdown{LockdownSteps: LockdownSteps{Sessions: true}, Keep: "session-a", Now: f.now})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sessions != 1 || res.APITokens != 1 || len(res.Runners) != 1 || res.Runners[0] != "laptop" ||
		res.Invitations != 1 || res.EmailLinks != 1 || res.WallLinks != 1 || res.Kept != "admin" || len(res.Revoked) != 2 {
		t.Fatalf("result = %+v", res)
	}
	for q, want := range map[string]int{
		"SELECT COUNT(*) FROM sessions":        2, // the admin's sign-in, kept
		"SELECT COUNT(*) FROM tokens":          0,
		"SELECT COUNT(*) FROM org_invitations": 0,
		"SELECT COUNT(*) FROM email_tokens":    0,
		"SELECT COUNT(*) FROM wall_tokens":     0,
	} {
		if n := f.count(t, q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	if _, err := f.acc.Session("session-a", f.now); err != nil {
		t.Errorf("the admin's session: %v", err)
	}
	// Only that step: passwords, two-factor and secrets are as they were.
	if f.count(t, "SELECT COUNT(*) FROM user_lockdown") != 0 {
		t.Error("the sessions step asked for something else")
	}
	checkSecrets(t, f.s, f.secrets)
}

func TestLockdownPasswords(t *testing.T) {
	t.Parallel()
	f := newLockdownFixture(t)
	res, err := f.acc.Lockdown(Lockdown{LockdownSteps: LockdownSteps{Passwords: true}, Keep: "session-a", Now: f.now})
	if err != nil {
		t.Fatal(err)
	}
	// The admin runs it (kept) and is the env account anyway: only ann.
	if res.Passwords != 1 || res.EnvAccount != "" {
		t.Fatalf("result = %+v", res)
	}
	s, err := f.acc.Session("session-b", f.now)
	if err != nil || !s.NeedsPassword {
		t.Fatalf("ann's session = %+v, %v", s, err)
	}
	if s, _ := f.acc.Session("session-a", f.now); s.NeedsPassword {
		t.Error("the admin running it was asked too")
	}
	// The env account is never asked: its password is the server's.
	res, err = f.acc.Lockdown(Lockdown{LockdownSteps: LockdownSteps{Passwords: true}, Now: f.now})
	if err != nil || res.EnvAccount != "admin" {
		t.Errorf("without a kept session = %+v, %v", res, err)
	}
	if s, _ := f.acc.Session("session-a", f.now); s.NeedsPassword {
		t.Error("the env account was asked for a new password")
	}
	if f.count(t, "SELECT COUNT(*) FROM sessions") != 3 {
		t.Error("the passwords step signed people out")
	}

}

func TestLockdownPasswordsClearedByReset(t *testing.T) {
	t.Parallel()
	f := newLockdownFixture(t)
	if _, err := f.acc.Lockdown(Lockdown{LockdownSteps: LockdownSteps{Passwords: true}, Now: f.now}); err != nil {
		t.Fatal(err)
	}
	_, tok, _, err := f.acc.AdminResetLink(f.ann, f.now)
	if err != nil {
		t.Fatal(err)
	}
	// Not the password that may have leaked; the link still works after.
	if _, err := f.acc.ResetPassword(tok, annPassword, f.now); !errors.Is(err, ErrSamePassword) {
		t.Errorf("the old password again = %v", err)
	}
	if _, err := f.acc.ResetPassword(tok, "ann-reset-password", f.now); err != nil {
		t.Fatal(err)
	}
	if need, err := f.acc.NeedsPassword(f.ann); err != nil || need {
		t.Errorf("after a reset link: needs password %v, %v", need, err)
	}
}

func TestLockdownTOTP(t *testing.T) {
	t.Parallel()
	f := newLockdownFixture(t)
	res, err := f.acc.Lockdown(Lockdown{LockdownSteps: LockdownSteps{TOTP: true}, Now: f.now})
	if err != nil || res.TOTP != 1 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	s, err := f.acc.Session("session-a", f.now)
	if err != nil || s.User.TOTP || !s.Needs2FA {
		t.Fatalf("the admin's session = %+v, %v", s, err)
	}
	if s, _ := f.acc.Session("session-b", f.now); s.Needs2FA {
		t.Error("ann, who had no two-factor sign-in, is asked to set it up")
	}
	// Setting it up again clears the request.
	if err := f.acc.BeginTOTP(f.admin, "NEWSECRET"); err != nil {
		t.Fatal(err)
	}
	if err := f.acc.ConfirmTOTP(f.admin, "NEWSECRET", 1, nil); err != nil {
		t.Fatal(err)
	}
	if s, _ := f.acc.Session("session-a", f.now); !s.User.TOTP || s.Needs2FA {
		t.Errorf("after setting it up = %+v", s)
	}

	// The admin running it from the web keeps theirs.
	f = newLockdownFixture(t)
	if res, err := f.acc.Lockdown(Lockdown{LockdownSteps: LockdownSteps{TOTP: true}, Keep: "session-a", Now: f.now}); err != nil || res.TOTP != 0 {
		t.Errorf("kept = %+v, %v", res, err)
	}
}

func TestLockdownSecrets(t *testing.T) {
	t.Parallel()
	f := newLockdownFixture(t)
	before := rawSecrets(t, f.s)
	res, err := f.acc.Lockdown(Lockdown{LockdownSteps: LockdownSteps{Secrets: true}, NewVAPIDKey: newVAPID, Now: f.now})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Webhooks) != 1 || !res.Webhooks[0].TokenCleared || res.Webhooks[0].Project != "shop" ||
		len(res.GitHubHooks) != 1 || len(res.Trackers) != 1 || !res.Trackers[0].Credentials || res.PushSubscriptions != 1 || !res.VAPID {
		t.Fatalf("result = %+v", res)
	}
	// New values, which differ from the old ones and are what the store reads.
	w, err := f.s.GetWebhook(f.secrets.webhook)
	if err != nil || w.Secret == f.secrets.whSecret || w.Secret != res.Webhooks[0].Secret || w.Token != "" || !strings.HasPrefix(w.Secret, WebhookSecretPrefix) {
		t.Errorf("webhook = %+v, %v", w, err)
	}
	h, err := f.s.GitHubHook(1, "owner/repo")
	if err != nil || h.Secret == plainHook || h.Secret == "" || h.URL != "" {
		t.Errorf("GitHub hook = %+v, %v", h, err)
	}
	c, err := f.s.TrackerConfig(f.secrets.project)
	if err != nil || c.HasCredentials || c.WebhookSecret == plainTrackerWS || c.WebhookSecret != res.Trackers[0].WebhookSecret || c.LastError == "" {
		t.Errorf("tracker = %+v, %v", c, err)
	}
	if k, err := f.s.PushVAPIDKey(newVAPID); err != nil || k != "new-vapid-key" {
		t.Errorf("VAPID key = %q, %v", k, err)
	}
	// Still sealed, and not the old sealed values either.
	after := rawSecrets(t, f.s)
	for _, name := range []string{"webhook secret", "github hook", "tracker secret", "vapid key"} {
		if !IsSealed(after[name]) || after[name] == before[name] {
			t.Errorf("%s stored as %q (was %q)", name, after[name], before[name])
		}
	}
	for _, secret := range []string{w.Secret, h.Secret, c.WebhookSecret, "new-vapid-key"} {
		for name, v := range after {
			if strings.Contains(v, secret) {
				t.Errorf("%s holds the new secret in the clear", name)
			}
		}
	}
	if after["tracker creds"] != noCreds || after["webhook token"] != "" {
		t.Errorf("not cleared: creds %q, token %q", after["tracker creds"], after["webhook token"])
	}
	if f.count(t, "SELECT COUNT(*) FROM push_subscriptions") != 0 {
		t.Error("push subscriptions left")
	}
	// The checklist names each, with the new secrets.
	text := ""
	secrets := 0
	for _, c := range res.Checklist() {
		text += c.Text + "\n"
		if c.Secret != "" {
			secrets++
		}
	}
	for _, want := range []string{"hooks.example.com", "owner/repo", "Board of project shop", "access token was cleared", "DASH_SECRET_KEY"} {
		if !strings.Contains(text, want) {
			t.Errorf("checklist without %q:\n%s", want, text)
		}
	}
	if secrets != 2 { // the webhook's and the tracker's
		t.Errorf("%d secrets in the checklist", secrets)
	}
	// Two-factor sign-in and sessions are as they were.
	if f.count(t, "SELECT COUNT(*) FROM sessions") != 3 || f.count(t, "SELECT COUNT(*) FROM users WHERE totp_secret IS NOT NULL") != 1 {
		t.Error("the secrets step did more")
	}

	// Without the key, nothing is rotated in the clear.
	f.s.SetSealer(nil)
	if _, err := f.acc.Lockdown(Lockdown{LockdownSteps: LockdownSteps{Secrets: true}, NewVAPIDKey: newVAPID}); !errors.Is(err, ErrNoSealer) {
		t.Errorf("without a key = %v", err)
	}
}

func TestLockdownAll(t *testing.T) {
	t.Parallel()
	f := newLockdownFixture(t)
	all := LockdownSteps{Sessions: true, Passwords: true, TOTP: true, Secrets: true}
	p, err := f.acc.LockdownPreview("")
	if err != nil || p.Sessions != 3 || p.APITokens != 1 || p.Runners != 1 || p.Passwords != 1 || p.TOTP != 1 || p.Webhooks != 1 || p.Emails != 1 {
		t.Fatalf("preview = %+v, %v", p, err)
	}
	res, err := f.acc.Lockdown(Lockdown{LockdownSteps: all, NewVAPIDKey: newVAPID, Now: f.now})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sessions != 3 || res.Passwords != 1 || res.TOTP != 1 || len(res.Webhooks) != 1 || res.EnvAccount != "admin" || res.Kept != "" {
		t.Errorf("result = %+v", res)
	}
	if f.count(t, "SELECT COUNT(*) FROM sessions")+f.count(t, "SELECT COUNT(*) FROM tokens") != 0 {
		t.Error("sessions or tokens left")
	}
	if need, _ := f.acc.NeedsPassword(f.ann); !need {
		t.Error("ann isn't asked for a new password")
	}
	if _, err := f.acc.Lockdown(Lockdown{}); err == nil {
		t.Error("no steps: no error")
	}
}

func TestNotice(t *testing.T) {
	t.Parallel()
	f := newLockdownFixture(t)
	if _, err := f.acc.NoticeFor(f.ann); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no notice yet = %v", err)
	}
	if _, err := f.acc.SetNotice("  ", f.admin, f.now); !errors.Is(err, ErrBadNotice) {
		t.Errorf("empty notice = %v", err)
	}
	n, err := f.acc.SetNotice("We had a breach.\n\nChoose a new password.", f.admin, f.now)
	if err != nil || n.By != "admin" {
		t.Fatal(n, err)
	}
	if got, err := f.acc.NoticeFor(f.ann); err != nil || got.ID != n.ID {
		t.Errorf("ann's notice = %+v, %v", got, err)
	}
	if err := f.acc.DismissNotice(n.ID, f.ann, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.acc.NoticeFor(f.ann); !errors.Is(err, ErrNotFound) {
		t.Errorf("after dismissing = %v", err)
	}
	if _, err := f.acc.NoticeFor(f.admin); err != nil {
		t.Errorf("the admin's, not dismissed = %v", err)
	}
	// A new notice shows again, even to who dismissed the last one.
	n2, _ := f.acc.SetNotice("Update: all clear.", f.admin, f.now)
	if got, err := f.acc.NoticeFor(f.ann); err != nil || got.ID != n2.ID {
		t.Errorf("a new notice = %+v, %v", got, err)
	}
	if err := f.acc.ClearNotice(f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.acc.CurrentNotice(); !errors.Is(err, ErrNotFound) {
		t.Errorf("after clearing = %v", err)
	}
	users, err := f.acc.LockdownUsers()
	if err != nil || len(users) != 1 || users[0].Username != "ann" {
		t.Errorf("users to email = %+v, %v", users, err)
	}
}

func TestLockdownSecretsAfterANewSecretKey(t *testing.T) {
	t.Parallel()
	f := newLockdownFixture(t)
	newNotify := func() (string, error) { return "new-notify-key", nil }
	keep := func() (string, error) { return "", errors.New("made a new key") }
	// With the same key, the unsubscribe links' key stays: sent links work.
	if _, err := f.acc.Lockdown(Lockdown{LockdownSteps: LockdownSteps{Secrets: true}, NewVAPIDKey: newVAPID, NewNotifyKey: newNotify}); err != nil {
		t.Fatal(err)
	}
	if k, err := f.s.NotifyKey(keep); err != nil || k != plainNotify {
		t.Errorf("notify key = %q, %v", k, err)
	}
	// The key leaked and was replaced: what it sealed is made anew or cleared.
	f.s.SetSealer(newTestSealer(t))
	res, err := f.acc.Lockdown(Lockdown{LockdownSteps: LockdownSteps{Secrets: true, TOTP: true}, NewVAPIDKey: newVAPID, NewNotifyKey: newNotify})
	if err != nil {
		t.Fatal(err)
	}
	if k, err := f.s.NotifyKey(keep); err != nil || k != "new-notify-key" {
		t.Errorf("notify key = %q, %v", k, err)
	}
	if _, bad, err := f.s.SealSecrets(); err != nil || bad != 0 {
		t.Errorf("%d secrets still don't open, %v", bad, err)
	}
	if w, err := f.s.GetWebhook(f.secrets.webhook); err != nil || w.Unreadable || w.Secret != res.Webhooks[0].Secret {
		t.Errorf("webhook = %+v, %v", w, err)
	}
}
