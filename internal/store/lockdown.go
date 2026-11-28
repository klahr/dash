package store

// ---- incident response (dash lockdown, Admin → Incident response) ----
//
// After a breach (a leaked database, say) a platform admin, or the server's
// operator with the server stopped, ends every way in at once: sessions and
// tokens, the passwords and authenticator secrets that may have leaked, and
// the secrets Dash keeps for its webhooks, push and board backends. See
// docs/breach.md.

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// LockdownSteps are the steps of a lockdown to take.
type LockdownSteps struct {
	// Sessions ends every session and deletes every API, OAuth and runner
	// token, invitation, email link (sign-up and reset) and wall link.
	Sessions bool `json:"sessions"`
	// Passwords asks every account with a password to choose a new one with
	// a reset link: its old one, which may have leaked, stops signing in.
	// Not those that sign in only with Google, nor the DASH_PASSWORD
	// account, whose password is the server's.
	Passwords bool `json:"passwords"`
	// TOTP turns off two-factor sign-in for everyone who has it, and asks
	// them to set it up again at their next sign-in.
	TOTP bool `json:"totp"`
	// Secrets makes new webhook secrets, tracker webhook secrets, GitHub
	// webhook secrets and VAPID key, and clears what Dash can't make anew:
	// webhook tokens, tracker credentials and push subscriptions.
	Secrets bool `json:"secrets"`
}

// Any reports whether any step is on.
func (s LockdownSteps) Any() bool { return s.Sessions || s.Passwords || s.TOTP || s.Secrets }

// String names the steps on, for the audit log.
func (s LockdownSteps) String() string {
	var on []string
	for _, x := range []struct {
		on   bool
		name string
	}{{s.Sessions, "sessions"}, {s.Passwords, "passwords"}, {s.TOTP, "2fa"}, {s.Secrets, "secrets"}} {
		if x.on {
			on = append(on, x.name)
		}
	}
	return strings.Join(on, ", ")
}

// Lockdown is what to do.
type Lockdown struct {
	LockdownSteps
	// Keep is the session (its key) of the admin running it from the web:
	// it and its family stay signed in, and its user is left out of the
	// Passwords and TOTP steps. "": nobody is kept (the command line).
	Keep string
	// NewVAPIDKey makes the new VAPID private key (notify.NewVAPIDPrivateKey).
	NewVAPIDKey func() (string, error)
	// NewNotifyKey makes a new key for unsubscribe links
	// (notify.NewSummaryKey), used only if the stored one doesn't open
	// (the secret key was replaced): links in sent emails keep working.
	NewNotifyKey func() (string, error)
	Now          time.Time
}

// LockdownResult is what a lockdown did, and what is left to do outside
// Dash (the checklist).
type LockdownResult struct {
	Steps LockdownSteps `json:"steps"`
	// Sessions step.
	Sessions    int      `json:"sessions"`
	APITokens   int      `json:"api_tokens"`  // API tokens and connected apps (OAuth)
	Invitations int      `json:"invitations"` // pending org invitations
	EmailLinks  int      `json:"email_links"` // sign-up and password reset links
	WallLinks   int      `json:"wall_links"`  // wall view links
	Runners     []string `json:"runners"`     // runners whose tokens were revoked: register them again
	// Revoked are the tokens deleted, so what holds one open (a runner's
	// tunnel) can be closed.
	Revoked []Token `json:"-"`
	// Passwords step: accounts that must choose a new password.
	Passwords int `json:"passwords"`
	// EnvAccount is the DASH_PASSWORD account, left out of the Passwords
	// step: change DASH_PASSWORD on the server ("" if none or not run).
	EnvAccount string `json:"env_account,omitempty"`
	// TOTP step: accounts whose two-factor sign-in was reset.
	TOTP int `json:"totp"`
	// Kept is the admin left signed in and out of the Passwords and TOTP
	// steps ("" for none).
	Kept string `json:"kept,omitempty"`
	// Secrets step.
	Webhooks          []RotatedWebhook `json:"webhooks"`
	GitHubHooks       []RotatedHook    `json:"github_hooks"`
	Trackers          []ClearedTracker `json:"trackers"`
	PushSubscriptions int              `json:"push_subscriptions"`
	VAPID             bool             `json:"vapid"`
	// SSO are the Google sign-in clients whose secrets are sealed with the
	// secret key: enter them again if DASH_SECRET_KEY may have leaked.
	SSO []SSOClient `json:"sso"`
}

// RotatedWebhook is a notification webhook given a new signing secret
// (shown once here) and, if it had one, its bearer token cleared.
type RotatedWebhook struct {
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	URL          string `json:"url"`
	Format       string `json:"format"`
	Project      string `json:"project,omitempty"` // "" for a personal webhook
	Owner        string `json:"owner,omitempty"`   // a personal webhook's
	Secret       string `json:"secret"`
	TokenCleared bool   `json:"token_cleared,omitempty"`
}

// RotatedHook is a GitHub webhook whose secret was replaced: Dash
// registers it again on GitHub with a new one at its next check.
type RotatedHook struct {
	Org  string `json:"org"`
	Repo string `json:"repo"`
}

// ClearedTracker is a project's board backend whose credentials were
// cleared and webhook secret replaced (shown once here).
type ClearedTracker struct {
	ProjectID     string `json:"project_id"`
	Project       string `json:"project"`
	Backend       string `json:"backend"`
	Credentials   bool   `json:"credentials"` // it had credentials, now gone
	WebhookSecret string `json:"webhook_secret,omitempty"`
}

// SSOClient is a Google sign-in client Dash keeps a secret for.
type SSOClient struct {
	Org      string `json:"org,omitempty"` // "" for this Dash's own
	ClientID string `json:"client_id"`
}

// ErrNoSealer is rotating secrets without the hub's secret key: the new
// ones would be stored in the clear.
var ErrNoSealer = errors.New("rotating secrets needs the hub's secret key (DASH_SECRET_KEY or <data>/secret.key), to keep them encrypted")

// Lockdown takes the steps of l in one transaction, and reports what it
// did and what is left to do outside Dash.
func (a *Accounts) Lockdown(l Lockdown) (LockdownResult, error) {
	res := LockdownResult{Steps: l.LockdownSteps, Runners: []string{}, Webhooks: []RotatedWebhook{}, GitHubHooks: []RotatedHook{},
		Trackers: []ClearedTracker{}, SSO: []SSOClient{}}
	if !l.Any() {
		return res, errors.New("choose at least one step")
	}
	s := a.s
	if l.Secrets && s.sealer == nil {
		return res, ErrNoSealer
	}
	if l.Now.IsZero() {
		l.Now = time.Now()
	}
	var vapid string
	if l.Secrets && l.NewVAPIDKey != nil {
		var err error
		if vapid, err = l.NewVAPIDKey(); err != nil {
			return res, err
		}
	}
	err := inTx(a.db, func(tx *sql.Tx) error {
		var keptUser int64
		if l.Keep != "" {
			err := tx.QueryRow("SELECT s.user_id, u.username FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.key = ?", l.Keep).Scan(&keptUser, &res.Kept)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
		}
		if l.Sessions {
			if err := lockdownSessions(tx, l, &res); err != nil {
				return fmt.Errorf("sign out everyone: %w", err)
			}
		}
		if l.Passwords {
			if err := lockdownPasswords(tx, keptUser, &res); err != nil {
				return fmt.Errorf("new passwords: %w", err)
			}
		}
		if l.TOTP {
			if err := lockdownTOTP(tx, keptUser, &res); err != nil {
				return fmt.Errorf("reset two-factor sign-in: %w", err)
			}
		}
		if l.Secrets {
			if err := s.lockdownSecrets(tx, vapid, l.NewNotifyKey, l.Now, &res); err != nil {
				return fmt.Errorf("rotate secrets: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return LockdownResult{}, err
	}
	return res, nil
}

func count(res sql.Result) int {
	n, _ := res.RowsAffected()
	return int(n)
}

func lockdownSessions(tx *sql.Tx, l Lockdown, res *LockdownResult) error {
	var err error
	if res.Revoked, err = queryAll(tx, scanToken, tokenCols+" ORDER BY t.id"); err != nil {
		return err
	}
	for _, t := range res.Revoked {
		if t.Kind == TokenRunner {
			res.Runners = append(res.Runners, t.Name)
		} else {
			res.APITokens++
		}
	}
	if _, err := tx.Exec("DELETE FROM tokens"); err != nil {
		return err
	}
	// The admin's own sign-in stays: this session and the ones it was
	// handed to on org subdomains (its family).
	r, err := tx.Exec(`DELETE FROM sessions WHERE NOT (key = ?1
		OR COALESCE(family IN (SELECT family FROM sessions WHERE key = ?1 AND family IS NOT NULL), 0))`, l.Keep)
	if err != nil {
		return err
	}
	res.Sessions = count(r)
	for _, x := range []struct {
		n *int
		q string
	}{
		{&res.Invitations, "DELETE FROM org_invitations"},
		{&res.EmailLinks, "DELETE FROM email_tokens"},
		{&res.WallLinks, "DELETE FROM wall_tokens"},
	} {
		r, err := tx.Exec(x.q)
		if err != nil {
			return err
		}
		*x.n = count(r)
	}
	return nil
}

func lockdownPasswords(tx *sql.Tx, kept int64, res *LockdownResult) error {
	r, err := tx.Exec(`INSERT INTO user_lockdown (user_id, new_password)
		SELECT id, 1 FROM users WHERE has_password = 1 AND env_managed = 0 AND unverified = 0 AND id != ?
		ON CONFLICT (user_id) DO UPDATE SET new_password = 1`, kept)
	if err != nil {
		return err
	}
	res.Passwords = count(r)
	err = tx.QueryRow("SELECT username FROM users WHERE env_managed = 1 AND id != ? LIMIT 1", kept).Scan(&res.EnvAccount)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return err
}

func lockdownTOTP(tx *sql.Tx, kept int64, res *LockdownResult) error {
	uids, err := queryAll(tx, scanOne[int64], "SELECT id FROM users WHERE (totp_secret IS NOT NULL OR totp_pending IS NOT NULL) AND id != ?", kept)
	if err != nil {
		return err
	}
	for _, uid := range uids {
		if err := resetTOTP(tx, uid); err != nil {
			return err
		}
	}
	// They set it up again at their next sign-in.
	if _, err := tx.Exec(`INSERT INTO user_lockdown (user_id, totp_reenroll) SELECT value, 1 FROM json_each(?) WHERE true
		ON CONFLICT (user_id) DO UPDATE SET totp_reenroll = 1`, jsonIDs(uids)); err != nil {
		return err
	}
	res.TOTP = len(uids)
	return nil
}

func jsonIDs(ids []int64) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprint(&b, id)
	}
	b.WriteByte(']')
	return b.String()
}

func (s *Store) lockdownSecrets(tx *sql.Tx, vapid string, newNotifyKey func() (string, error), now time.Time, res *LockdownResult) error {
	// Notification webhooks: a new signing secret each; a bearer token (the
	// receiving service's) can't be made anew, so it is cleared.
	hooks, err := queryAll(tx, scanWebhookRow, "SELECT "+webhookCols+" FROM webhooks ORDER BY created_at, id")
	if err != nil {
		return err
	}
	for _, w := range hooks {
		secret := newWebhookSecret()
		sealed, err := s.seal(secret, webhookSecretContext(w.ID))
		if err != nil {
			return err
		}
		rw := RotatedWebhook{ID: w.ID, Name: w.Name, URL: w.URL, Format: w.Format, Secret: secret, TokenCleared: w.Token != ""}
		q := "UPDATE webhooks SET secret = ? WHERE id = ?"
		if rw.TokenCleared {
			q = "UPDATE webhooks SET secret = ?, token = '', last_error = 'Its access token was cleared by incident response: enter a new one.' WHERE id = ?"
		}
		if _, err := tx.Exec(q, sealed, w.ID); err != nil {
			return err
		}
		if w.ProjectID != "" {
			rw.Project = s.projectName(w.ProjectID)
		} else {
			_ = tx.QueryRow("SELECT username FROM users WHERE id = ?", w.UserID).Scan(&rw.Owner)
		}
		res.Webhooks = append(res.Webhooks, rw)
	}

	// GitHub webhooks: a new secret, and no URL, so the hook manager
	// registers each again (updating the one on GitHub with a secret of its
	// own) at its next check.
	gh, err := queryAll(tx, scanGitHubHook, "SELECT "+githubHookCols+" FROM github_hooks ORDER BY org_id, repo")
	if err != nil {
		return err
	}
	for _, h := range gh {
		sealed, err := s.seal(randomHex(32), githubHookContext(h.OrgID, h.Repo))
		if err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE github_hooks SET secret = ?, url = '' WHERE org_id = ? AND repo = ?", sealed, h.OrgID, h.Repo); err != nil {
			return err
		}
		rh := RotatedHook{Repo: h.Repo}
		_ = tx.QueryRow("SELECT name FROM organizations WHERE id = ?", h.OrgID).Scan(&rh.Org)
		res.GitHubHooks = append(res.GitHubHooks, rh)
	}

	// Board backends: credentials can't be made anew (they're the
	// tracker's), so they're cleared; the webhook secret is Dash's.
	type trk struct{ pid, backend, creds, secret string }
	trackers, err := queryAll(tx, func(sc scanner) (trk, error) {
		var t trk
		err := sc.Scan(&t.pid, &t.backend, &t.creds, &t.secret)
		return t, err
	}, "SELECT project_id, backend, credentials, webhook_secret FROM tracker_projects ORDER BY project_id")
	if err != nil {
		return err
	}
	for _, t := range trackers {
		ct := ClearedTracker{ProjectID: t.pid, Project: s.projectName(t.pid), Backend: t.backend, Credentials: t.creds != "" && t.creds != noCreds}
		secret := t.secret
		if t.secret != "" {
			ct.WebhookSecret = NewTrackerSecret()
			if secret, err = s.seal(ct.WebhookSecret, trackerSecretContext(t.pid)); err != nil {
				return err
			}
		}
		if !ct.Credentials && ct.WebhookSecret == "" {
			continue
		}
		msg := ""
		if ct.Credentials {
			msg = "Its credentials were cleared by incident response: enter new ones in Project settings → Board."
		}
		if _, err := tx.Exec("UPDATE tracker_projects SET credentials = ?, webhook_secret = ?, last_error = CASE WHEN ? = '' THEN last_error ELSE ? END WHERE project_id = ?",
			noCreds, secret, msg, msg, t.pid); err != nil {
			return err
		}
		res.Trackers = append(res.Trackers, ct)
	}

	// Web Push: a new VAPID key, which the browsers subscribed with the old
	// one can't use: they subscribe again.
	r, err := tx.Exec("DELETE FROM push_subscriptions")
	if err != nil {
		return err
	}
	res.PushSubscriptions = count(r)
	if vapid != "" {
		ctx := singletonContext("push_vapid", "private_key")
		sealed, err := s.seal(vapid, ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO push_vapid (id, private_key, created_at) VALUES (1, ?1, ?2)
			ON CONFLICT (id) DO UPDATE SET private_key = ?1, created_at = ?2`, sealed, now.Unix()); err != nil {
			return err
		}
		res.VAPID = true
	}

	// The unsubscribe links' key, if it doesn't open with this secret key.
	var notifyKey string
	if err := tx.QueryRow("SELECT key FROM notify_key WHERE id = 1").Scan(&notifyKey); err == nil && newNotifyKey != nil {
		ctx := singletonContext("notify_key", "key")
		if _, err := s.open(notifyKey, ctx); err != nil {
			k, err := newNotifyKey()
			if err != nil {
				return err
			}
			sealed, err := s.seal(k, ctx)
			if err != nil {
				return err
			}
			if _, err := tx.Exec("UPDATE notify_key SET key = ?, created_at = ? WHERE id = 1", sealed, now.Unix()); err != nil {
				return err
			}
		}
	}

	// Google sign-in clients: their secrets are sealed with the secret key;
	// listed for the checklist, to enter again if that key leaked.
	res.SSO, err = queryAll(tx, func(sc scanner) (SSOClient, error) {
		var c SSOClient
		err := sc.Scan(&c.Org, &c.ClientID)
		return c, err
	}, `SELECT '', google_client_id FROM instance_sso WHERE google_client_secret != ''
		UNION ALL SELECT o.name, x.google_client_id FROM org_sso x JOIN organizations o ON o.id = x.org_id WHERE x.google_client_secret != ''`)
	return err
}

// projectName is the project's name, or its id if it's gone.
func (s *Store) projectName(id string) string {
	if p, err := s.GetProject(id); err == nil && p.Name != "" {
		return p.Name
	}
	return id
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// LockdownUsers are the active accounts (not disabled, address confirmed)
// with an email address, for the notice: by username.
func (a *Accounts) LockdownUsers() ([]User, error) {
	return queryAll(a.db, func(sc scanner) (User, error) { return scanUser(sc) },
		userCols+" WHERE unverified = 0 AND COALESCE(email, '') != '' AND "+enabled("users.id")+" ORDER BY username COLLATE NOCASE")
}

// ---- the new password ----

// ErrSamePassword is choosing the password that may have leaked again.
var ErrSamePassword = errors.New("choose a new password, not the one you had")

// ---- the notice ----

// Notice is a message from the platform admins, shown as a banner after
// sign-in until each user dismisses it or an admin clears it.
type Notice struct {
	ID      int64     `json:"id"`
	Body    string    `json:"body"`
	By      string    `json:"by,omitempty"`
	Created time.Time `json:"created"`
}

// MaxNotice is the longest notice, in bytes.
const MaxNotice = 10000

// ErrBadNotice is an empty or too long notice.
var ErrBadNotice = fmt.Errorf("a notice needs some text, up to %d characters", MaxNotice)

// SetNotice makes body the current notice, as by, replacing the one before.
func (a *Accounts) SetNotice(body string, by int64, now time.Time) (Notice, error) {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > MaxNotice {
		return Notice{}, ErrBadNotice
	}
	var n Notice
	err := inTx(a.db, func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE notices SET cleared_at = ? WHERE cleared_at IS NULL", now.Unix()); err != nil {
			return err
		}
		return tx.QueryRow("INSERT INTO notices (body, created_by, created_at) VALUES (?, ?, ?) RETURNING id", body, nullID(by), now.Unix()).Scan(&n.ID)
	})
	if err != nil {
		return n, err
	}
	return a.notice(n.ID)
}

const noticeCols = `SELECT n.id, n.body, COALESCE(u.username, ''), n.created_at FROM notices n LEFT JOIN users u ON u.id = n.created_by`

func scanNotice(sc scanner) (Notice, error) {
	var n Notice
	var at int64
	err := sc.Scan(&n.ID, &n.Body, &n.By, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrNotFound
	}
	n.Created = time.Unix(at, 0).UTC()
	return n, err
}

func (a *Accounts) notice(id int64) (Notice, error) {
	return scanNotice(a.db.QueryRow(noticeCols+" WHERE n.id = ?", id))
}

// CurrentNotice is the notice shown now, or ErrNotFound.
func (a *Accounts) CurrentNotice() (Notice, error) {
	return scanNotice(a.db.QueryRow(noticeCols + " WHERE n.cleared_at IS NULL ORDER BY n.id DESC LIMIT 1"))
}

// NoticeFor is the current notice if uid hasn't dismissed it, or
// ErrNotFound.
func (a *Accounts) NoticeFor(uid int64) (Notice, error) {
	return scanNotice(a.db.QueryRow(noticeCols+` WHERE n.cleared_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM notice_dismissals d WHERE d.notice_id = n.id AND d.user_id = ?)
		ORDER BY n.id DESC LIMIT 1`, uid))
}

// DismissNotice hides notice id from uid.
func (a *Accounts) DismissNotice(id, uid int64, now time.Time) error {
	if _, err := a.notice(id); err != nil {
		return err
	}
	_, err := a.db.Exec("INSERT INTO notice_dismissals (notice_id, user_id, at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING", id, uid, now.Unix())
	return err
}

// ClearNotice stops showing the current notice to anyone. No notice is no
// error.
func (a *Accounts) ClearNotice(now time.Time) error {
	_, err := a.db.Exec("UPDATE notices SET cleared_at = ? WHERE cleared_at IS NULL", now.Unix())
	return err
}

// ---- what is left to do outside Dash ----

// ChecklistItem is one thing to do after a lockdown. Secret, when set, is a
// new secret to copy to where Text says: shown once, never stored in the
// clear.
type ChecklistItem struct {
	Text   string `json:"text"`
	Secret string `json:"secret,omitempty"`
}

// Checklist is what is left to do after r, outside Dash or by the people it
// signed out, in order.
func (r LockdownResult) Checklist() []ChecklistItem {
	var out []ChecklistItem
	add := func(secret, format string, args ...any) {
		out = append(out, ChecklistItem{Text: fmt.Sprintf(format, args...), Secret: secret})
	}
	if r.Kept != "" && (r.Steps.Passwords || r.Steps.TOTP) {
		add("", "Your own account (%s) was left out so you stay signed in: once you're done here, sign out and choose a new password with “Forgot password?” "+
			"(or ask another platform admin for a reset link), and turn two-factor sign-in off and on again under Account → Sign-in for a new secret.", r.Kept)
	}
	if r.Passwords > 0 {
		add("", "%d accounts choose a new password with a reset link: their old password no longer signs in. They use “Forgot password?” on the sign-in page; "+
			"if this Dash doesn't send email (or an account has no address), make them a link under Admin → Users → Reset password… and hand it over another way.", r.Passwords)
	}
	if r.EnvAccount != "" {
		add("", "The account %s signs in with DASH_PASSWORD from the server's environment: set a new DASH_PASSWORD there and restart Dash.", r.EnvAccount)
	}
	for _, name := range r.Runners {
		add("", "Runner “%s”: its token was revoked. Make a registration code under Runners and run `dash runner setup --code <code>` on its computer.", name)
	}
	if r.APITokens > 0 {
		add("", "%d API tokens and connected apps were revoked: tell their owners to make new tokens (Account → Apps & tokens) and connect their apps again (such as a claude.ai connector).", r.APITokens)
	}
	if r.WallLinks > 0 {
		add("", "%d wall view links stopped working: make new ones for the screens under Organization → Wall view.", r.WallLinks)
	}
	if r.Invitations > 0 {
		add("", "%d pending invitations were cancelled: invite those people again.", r.Invitations)
	}
	for _, w := range r.Webhooks {
		where := "project " + w.Project
		if w.Project == "" {
			where = w.Owner + "'s personal webhook"
		}
		name := w.Name
		if name == "" {
			name = w.URL
		}
		switch w.Format {
		case "slack":
			add("", "Webhook “%s” (%s): a Slack webhook URL is itself a secret. Make a new incoming webhook in Slack, delete the old one, and enter the new URL in the webhook's settings.", name, where)
		case "ntfy":
			add("", "Webhook “%s” (%s): if the ntfy topic is private, move to a new topic name and enter the new URL.", name, where)
		default:
			add(w.Secret, "Webhook “%s” (%s, %s): put this new signing secret in the receiver; deliveries are signed with it from now on.", name, where, w.URL)
		}
		if w.TokenCleared {
			add("", "Webhook “%s” (%s): its access token was cleared. Revoke the old token at the receiving service, make a new one and enter it in the webhook's settings.", name, where)
		}
	}
	for _, h := range r.GitHubHooks {
		add("", "GitHub webhook of %s (%s): Dash registers it again with a new secret at its next check, through a runner of the project with a gh login that may manage webhooks. "+
			"If the project's GitHub status says it couldn't, delete the webhook under the repository's Settings → Webhooks and Dash adds it again.", h.Repo, h.Org)
	}
	for _, t := range r.Trackers {
		if t.Credentials {
			add("", "Board of project %s (%s): its credentials were cleared. Revoke them at %s, make new ones and enter them in Project settings → Board.", t.Project, t.Backend, t.Backend)
		}
		if t.WebhookSecret != "" {
			add(t.WebhookSecret, "Board of project %s (%s): put this new webhook secret in the %s webhook that calls Dash.", t.Project, t.Backend, t.Backend)
		}
	}
	if r.Steps.Secrets {
		if r.PushSubscriptions > 0 {
			add("", "%d browsers' push notifications were turned off (a new VAPID key): people turn them on again under Account → Notifications.", r.PushSubscriptions)
		}
		sso := make([]string, 0, len(r.SSO))
		for _, c := range r.SSO {
			who := "this Dash"
			if c.Org != "" {
				who = c.Org
			}
			sso = append(sso, fmt.Sprintf("%s (client %s)", who, c.ClientID))
		}
		text := "If the secret key (DASH_SECRET_KEY, or secret.key in the data directory) may have leaked too: set a new one, restart Dash and run this again with “Rotate secrets”"
		if len(sso) > 0 {
			text += "; then make new Google client secrets and enter them for " + strings.Join(sso, ", ")
		}
		add("", "%s. See docs/breach.md.", text)
	}
	return out
}

// LockdownPreview is how much each step would touch, for the summary shown
// before running it.
type LockdownPreview struct {
	Users       int `json:"users"`       // active accounts
	Sessions    int `json:"sessions"`    // sessions other than the admin's
	APITokens   int `json:"api_tokens"`  // API tokens and connected apps
	Runners     int `json:"runners"`     // runner tokens
	Invitations int `json:"invitations"` // pending invitations
	WallLinks   int `json:"wall_links"`
	Passwords   int `json:"passwords"` // accounts that would choose a new password
	TOTP        int `json:"totp"`      // accounts with two-factor sign-in
	Webhooks    int `json:"webhooks"`
	GitHubHooks int `json:"github_hooks"`
	Trackers    int `json:"trackers"` // board backends with credentials or a webhook secret
	Push        int `json:"push_subscriptions"`
	Emails      int `json:"emails"` // active accounts with an email address
}

// LockdownPreview counts what a lockdown would touch, keeping the session
// keep (and its user) as Lockdown does.
func (a *Accounts) LockdownPreview(keep string) (LockdownPreview, error) {
	var p LockdownPreview
	var kept int64
	_ = a.db.QueryRow("SELECT user_id FROM sessions WHERE key = ?", keep).Scan(&kept)
	err := a.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM users WHERE unverified = 0 AND `+enabled("users.id")+`),
		(SELECT COUNT(*) FROM sessions WHERE NOT (key = ?1 OR COALESCE(family IN (SELECT family FROM sessions WHERE key = ?1 AND family IS NOT NULL), 0))),
		(SELECT COUNT(*) FROM tokens WHERE kind != 'runner'),
		(SELECT COUNT(*) FROM tokens WHERE kind = 'runner'),
		(SELECT COUNT(*) FROM org_invitations),
		(SELECT COUNT(*) FROM wall_tokens),
		(SELECT COUNT(*) FROM users WHERE has_password = 1 AND env_managed = 0 AND unverified = 0 AND id != ?2),
		(SELECT COUNT(*) FROM users WHERE (totp_secret IS NOT NULL OR totp_pending IS NOT NULL) AND id != ?2),
		(SELECT COUNT(*) FROM webhooks),
		(SELECT COUNT(*) FROM github_hooks),
		(SELECT COUNT(*) FROM tracker_projects WHERE credentials NOT IN ('', '{}') OR webhook_secret != ''),
		(SELECT COUNT(*) FROM push_subscriptions),
		(SELECT COUNT(*) FROM users WHERE unverified = 0 AND COALESCE(email, '') != '' AND `+enabled("users.id")+`)`, keep, kept).
		Scan(&p.Users, &p.Sessions, &p.APITokens, &p.Runners, &p.Invitations, &p.WallLinks, &p.Passwords, &p.TOTP,
			&p.Webhooks, &p.GitHubHooks, &p.Trackers, &p.Push, &p.Emails)
	return p, err
}

// NeedsPassword reports whether a lockdown asked uid for a new password
// they haven't chosen yet.
func (a *Accounts) NeedsPassword(uid int64) (bool, error) {
	var need bool
	err := a.db.QueryRow("SELECT "+needsPassword+" FROM users u WHERE u.id = ?", uid).Scan(&need)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return need, err
}

// LockdownEvents are the audit log's latest entries about incident
// responses and notices, newest first, at most limit.
func (a *Accounts) LockdownEvents(limit int) ([]AuthEvent, error) {
	return queryAll(a.db, func(sc scanner) (e AuthEvent, err error) {
		var at int64
		err = sc.Scan(&e.ID, &at, &e.Actor, &e.ActorN, &e.Kind, &e.Detail, &e.IP)
		e.At = time.Unix(at, 0).UTC()
		return e, err
	}, `SELECT e.id, e.at, COALESCE(e.actor_id, 0), COALESCE(x.username, ''), e.kind, e.detail, e.ip
		FROM auth_events e LEFT JOIN users x ON x.id = e.actor_id
		WHERE e.kind LIKE 'admin.lockdown.%' OR e.kind LIKE 'admin.notice.%' ORDER BY e.id DESC LIMIT ?`, limit)
}
