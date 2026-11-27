package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// lockdownData is a stopped server's data directory: its database, with
// the env account signed in, a member with a token and a webhook, sealed
// with the key in secret.key.
func lockdownData(t *testing.T) (dir, webhookID, oldSecret string) {
	t.Helper()
	dir = t.TempDir()
	key, err := auth.LoadSecretKey("", filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := auth.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.SetSealer(sealer)
	acc := st.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-password"); err != nil {
		t.Fatal(err)
	}
	env, _ := acc.EnvUser()
	org, _ := acc.DefaultOrg()
	tok, _, err := acc.InviteWith(env.ID, org.ID, "bea@example.com", store.RoleMember, true)
	if err != nil {
		t.Fatal(err)
	}
	bea, _, err := acc.RegisterWithInvitation(tok, "bea", "pw-bea-long")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for key, uid := range map[string]int64{"s-admin": env.ID, "s-bea": bea.ID} {
		if err := acc.CreateSession(key, uid, now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := acc.CreateToken(store.NewToken{Kind: store.TokenAPI, Name: "ci", UserID: bea.ID, OrgID: org.ID, Scopes: []string{store.ScopeRead}}, now); err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProject(model.Project{Name: "shop", Path: t.TempDir(), OrgID: org.ID})
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.CreateWebhook(model.Webhook{ProjectID: p.ID, URL: "https://hooks.example.com/x", Format: model.WebhookJSON})
	if err != nil {
		t.Fatal(err)
	}
	return dir, w.ID, w.Secret
}

func lockdownEnv(t *testing.T) {
	for _, k := range []string{"DASH_SECRET_KEY", "DASH_SMTP_HOST", "DASH_SMTP_FROM", "DASH_SMTP_USERNAME", "DASH_SMTP_PASSWORD", "DASH_PUBLIC_URL"} {
		t.Setenv(k, "")
	}
}

func TestLockdownCommand(t *testing.T) {
	lockdownEnv(t)
	dir, hook, old := lockdownData(t)
	notice := filepath.Join(t.TempDir(), "notice.txt")
	if err := os.WriteFile(notice, []byte("We had a breach.\n\nChoose a new password."), 0o600); err != nil {
		t.Fatal(err)
	}
	csv := filepath.Join(t.TempDir(), "users.csv")
	var out, errOut bytes.Buffer
	code := lockdownCmd([]string{"-data", dir, "-env", filepath.Join(dir, "none.env"), "--all", "--notice-file", notice, "--csv", csv, "--yes"},
		strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errOut.String())
	}
	for _, want := range []string{"signed out: 2 sessions, 1 API tokens", "1 accounts choose a new password", "Still to do outside Dash",
		"hooks.example.com", "Email is off", "DASH_PASSWORD"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output without %q:\n%s", want, out.String())
		}
	}

	key, _ := auth.LoadSecretKey("", filepath.Join(dir, "secret.key"))
	sealer, _ := auth.NewSealer(key)
	st, err := store.Open(filepath.Join(dir, "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.SetSealer(sealer)
	acc := st.Accounts()
	if p, _ := acc.LockdownPreview(""); p.Sessions != 0 || p.APITokens != 0 {
		t.Errorf("left: %+v", p)
	}
	bea, _, _ := acc.Credentials("bea")
	if need, _ := acc.NeedsPassword(bea.ID); !need {
		t.Error("bea isn't asked for a new password")
	}
	w, err := st.GetWebhook(hook)
	if err != nil || w.Secret == old || w.Unreadable || !strings.Contains(out.String(), w.Secret) {
		t.Errorf("webhook secret %q (was %q), %v", w.Secret, old, err)
	}
	if n, err := acc.CurrentNotice(); err != nil || !strings.Contains(n.Body, "breach") {
		t.Errorf("notice = %+v, %v", n, err)
	}
	if b, err := os.ReadFile(csv); err != nil || !strings.Contains(string(b), "bea,bea@example.com") {
		t.Errorf("csv = %q, %v", b, err)
	}
	if es, _ := acc.LockdownEvents(20); len(es) < 6 { // run, 4 steps, banner
		t.Errorf("audit log: %+v", es)
	}
}

func TestLockdownCommandAsksAndNeedsTheKey(t *testing.T) {
	lockdownEnv(t)
	dir, _, _ := lockdownData(t)
	var out, errOut bytes.Buffer
	if code := lockdownCmd([]string{"-data", dir, "--sessions"}, strings.NewReader("no\n"), &out, &errOut); code != 1 ||
		!strings.Contains(out.String(), "Type LOCKDOWN") {
		t.Errorf("not confirmed: exit %d\n%s%s", code, out.String(), errOut.String())
	}
	if code := lockdownCmd([]string{"-data", dir}, strings.NewReader(""), &out, &errOut); code != 2 {
		t.Errorf("no steps: exit %d", code)
	}
	if err := os.Remove(filepath.Join(dir, "secret.key")); err != nil {
		t.Fatal(err)
	}
	errOut.Reset()
	if code := lockdownCmd([]string{"-data", dir, "--sessions", "--yes"}, strings.NewReader(""), &out, &errOut); code != 1 ||
		!strings.Contains(errOut.String(), "DASH_SECRET_KEY") {
		t.Errorf("without the key: exit %d %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "secret.key")); err == nil {
		t.Error("made a new secret key")
	}
	// Typed: it runs.
	t.Setenv("DASH_SECRET_KEY", strings.Repeat("ab", 32))
	out.Reset()
	if code := lockdownCmd([]string{"-data", dir, "--sessions"}, strings.NewReader("LOCKDOWN\n"), &out, &errOut); code != 0 ||
		!strings.Contains(out.String(), "signed out: 2 sessions") {
		t.Errorf("confirmed: exit %d\n%s%s", code, out.String(), errOut.String())
	}
}
