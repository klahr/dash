package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/mail"
	"github.com/klahr/dash/internal/runnerctl"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// lockdownCmd is `dash lockdown`: the incident response of Admin →
// Incident response, on the server's database directly, so it works with
// the server stopped, when its web pages can't be trusted (docs/breach.md).
// Nobody is left out: the server's operator has no session to keep.
func lockdownCmd(args []string, in io.Reader, out, errOut io.Writer) int {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("dash lockdown", flag.ContinueOnError)
	fs.SetOutput(errOut)
	dataDir := fs.String("data", runnerctl.HubData(home), "the server's -data directory")
	envFile := fs.String("env", ".env", "file with the server's DASH_SECRET_KEY and DASH_SMTP_* (real env vars take precedence)")
	var steps store.LockdownSteps
	fs.BoolVar(&steps.Sessions, "sessions", false, "sign out everyone: end every session, delete every API, OAuth and runner token, invitation, email link and wall link")
	fs.BoolVar(&steps.Passwords, "passwords", false, "ask everyone with a password for a new one at their next sign-in")
	fs.BoolVar(&steps.TOTP, "2fa", false, "reset two-factor sign-in: everyone who had it sets it up again at their next sign-in")
	fs.BoolVar(&steps.Secrets, "rotate-secrets", false, "new webhook, board and GitHub webhook secrets and VAPID key; clear webhook tokens, board credentials and push subscriptions")
	all := fs.Bool("all", false, "all four steps above")
	noticeFile := fs.String("notice-file", "", "a notice to everyone, from this file: emailed (with SMTP set up) and shown after sign-in")
	noEmail := fs.Bool("no-email", false, "don't email the notice")
	noBanner := fs.Bool("no-banner", false, "don't show the notice after sign-in")
	csvPath := fs.String("csv", "", "write the active accounts' names and email addresses to this CSV file")
	yes := fs.Bool("yes", false, "don't ask to confirm")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: dash lockdown [-data dir] [--all | --sessions --passwords --2fa --rotate-secrets] [--notice-file path] [--csv path] [--yes]\n\n"+
			"Responds to a breach: signs everyone out, asks for new passwords and two-factor setups, rotates the\n"+
			"secrets in the database and tells everyone. Stop the server first. See docs/breach.md.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	if *all {
		steps = store.LockdownSteps{Sessions: true, Passwords: true, TOTP: true, Secrets: true}
	}
	var notice string
	if *noticeFile != "" {
		b, err := os.ReadFile(*noticeFile)
		if err != nil {
			fmt.Fprintln(errOut, "notice:", err)
			return 1
		}
		if notice = strings.TrimSpace(string(b)); notice == "" || len(notice) > store.MaxNotice {
			fmt.Fprintln(errOut, "notice:", store.ErrBadNotice)
			return 1
		}
	}
	email, banner := notice != "" && !*noEmail, notice != "" && !*noBanner
	if !steps.Any() && !email && !banner && *csvPath == "" {
		fmt.Fprintln(errOut, "choose what to do: --all, or any of --sessions --passwords --2fa --rotate-secrets, --notice-file, --csv")
		fs.Usage()
		return 2
	}

	if err := auth.LoadDotEnv(*envFile); err != nil {
		fmt.Fprintln(errOut, "read env file:", err)
		return 1
	}
	path := filepath.Join(*dataDir, "dash.db")
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintln(errOut, "no Dash database at", path, "(use -data)")
		return 1
	}
	st, err := store.Open(path, nil)
	if err != nil {
		fmt.Fprintln(errOut, "open store:", err)
		return 1
	}
	defer st.Close()
	// The server's key, never a new one: secrets sealed with another key
	// wouldn't open on the server.
	keyFile := filepath.Join(*dataDir, "secret.key")
	if os.Getenv("DASH_SECRET_KEY") == "" {
		if _, err := os.Stat(keyFile); err != nil {
			fmt.Fprintln(errOut, "secret key: set DASH_SECRET_KEY as the server has it (or -env), or keep", keyFile)
			return 1
		}
	}
	key, err := auth.LoadSecretKey(os.Getenv("DASH_SECRET_KEY"), keyFile)
	var sealer *auth.Sealer
	if err == nil {
		sealer, err = auth.NewSealer(key)
	}
	if err != nil {
		fmt.Fprintln(errOut, "secret key:", err)
		return 1
	}
	st.SetSealer(sealer)
	if _, bad, err := st.SealSecrets(); err != nil {
		fmt.Fprintln(errOut, "secret key:", err)
		return 1
	} else if bad > 0 {
		fmt.Fprintf(errOut, "warning: %d secrets in the database don't open with this secret key. Unless you just replaced the key "+
			"because it leaked, stop and give this command the server's DASH_SECRET_KEY (-env).\n", bad)
	}
	acc := st.Accounts()
	authn := auth.New(acc)
	if email {
		configureMail(authn, false, mail.Config{
			Host: os.Getenv("DASH_SMTP_HOST"), Username: os.Getenv("DASH_SMTP_USERNAME"),
			Password: os.Getenv("DASH_SMTP_PASSWORD"), From: os.Getenv("DASH_SMTP_FROM"),
		}, os.Getenv("DASH_PUBLIC_URL"))
	}

	p, err := acc.LockdownPreview("")
	if err != nil {
		fmt.Fprintln(errOut, "count:", err)
		return 1
	}
	printLockdownPlan(out, steps, p, email, banner, authn.MailOn(), *csvPath)
	if !*yes {
		fmt.Fprintf(out, "\nType %s to go on: ", auth.LockdownConfirm)
		line, _ := bufio.NewReader(in).ReadString('\n')
		if strings.TrimSpace(line) != auth.LockdownConfirm {
			fmt.Fprintln(errOut, "Nothing was done.")
			return 1
		}
	}

	res, err := authn.RunLockdown(nil, 0, store.Lockdown{LockdownSteps: steps, Now: time.Now()})
	if err != nil {
		fmt.Fprintln(errOut, "lockdown:", err)
		return 1
	}
	if steps.Sessions {
		// The server keeps its runners in runners.json: forget those whose
		// tokens are gone, so they can register again under their names,
		// and cancel the registration codes.
		hub := &tunnel.Hub{Accounts: acc}
		if err := hub.Keep(filepath.Join(*dataDir, "runners.json")); err != nil {
			fmt.Fprintln(errOut, "runners.json:", err)
		} else {
			for _, t := range res.Revoked {
				if t.Kind == store.TokenRunner {
					if err := hub.Revoke(t.RunnerID); err != nil && !errors.Is(err, tunnel.ErrUnknownName) {
						fmt.Fprintln(errOut, "forget runner", t.Name+":", err)
					}
				}
			}
			hub.DropCodes()
		}
	}
	printLockdownResult(out, res)

	code := 0
	if banner {
		if _, err := acc.SetNotice(notice, 0, time.Now()); err != nil {
			fmt.Fprintln(errOut, "notice:", err)
			code = 1
		} else {
			_ = acc.Audit(store.AuthEvent{At: time.Now(), Kind: "admin.lockdown.banner", Detail: "dash lockdown on the server"})
			fmt.Fprintln(out, "\nThe notice shows after sign-in until each person dismisses it (Admin → Incident response clears it).")
		}
	}
	if *csvPath != "" {
		if err := writeUsersCSV(acc, *csvPath); err != nil {
			fmt.Fprintln(errOut, "csv:", err)
			code = 1
		} else {
			fmt.Fprintln(out, "Wrote the active accounts' names and email addresses to", *csvPath)
		}
	}
	if email {
		if !authn.MailOn() {
			fmt.Fprintln(out, "\nEmail is off: set DASH_SMTP_HOST, DASH_SMTP_FROM (and DASH_SMTP_USERNAME, DASH_SMTP_PASSWORD) and DASH_PUBLIC_URL to email the notice,")
			fmt.Fprintln(out, "or send it yourself to the addresses that --csv <file> writes.")
		} else {
			users, err := acc.LockdownUsers()
			if err != nil {
				fmt.Fprintln(errOut, "email:", err)
				return 1
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			fmt.Fprintf(out, "\nEmailing the notice to %d people…\n", len(users))
			sent, failed := authn.MailNotice(ctx, notice, users, func(sent, failed int) {
				fmt.Fprintf(out, "\r%d sent, %d failed of %d", sent, failed, len(users))
			})
			fmt.Fprintln(out)
			_ = acc.Audit(store.AuthEvent{At: time.Now(), Kind: "admin.lockdown.emailed",
				Detail: fmt.Sprintf("dash lockdown on the server: notice emailed: %d sent, %d failed of %d", sent, failed, len(users))})
			if failed > 0 || sent < len(users) {
				fmt.Fprintf(errOut, "%d of %d emails didn't go out (see above)\n", len(users)-sent, len(users))
				code = 1
			}
		}
	}
	return code
}

func writeUsersCSV(acc *store.Accounts, path string) error {
	users, err := acc.LockdownUsers()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := auth.WriteUsersCSV(f, users); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func printLockdownPlan(out io.Writer, s store.LockdownSteps, p store.LockdownPreview, email, banner, mailOn bool, csv string) {
	fmt.Fprintln(out, "dash lockdown will:")
	if s.Sessions {
		fmt.Fprintf(out, "  - sign everyone out: %d sessions, %d API tokens and connected apps, %d runners, %d invitations, %d wall links, every email link\n",
			p.Sessions, p.APITokens, p.Runners, p.Invitations, p.WallLinks)
	}
	if s.Passwords {
		fmt.Fprintf(out, "  - ask %d accounts for a new password at their next sign-in\n", p.Passwords)
	}
	if s.TOTP {
		fmt.Fprintf(out, "  - reset two-factor sign-in for %d accounts: they set it up again at their next sign-in\n", p.TOTP)
	}
	if s.Secrets {
		fmt.Fprintf(out, "  - rotate secrets: %d webhooks, %d GitHub webhooks, %d board backends, the VAPID key (%d push subscriptions cleared)\n",
			p.Webhooks, p.GitHubHooks, p.Trackers, p.Push)
	}
	if email {
		if mailOn {
			fmt.Fprintf(out, "  - email the notice to %d people\n", p.Emails)
		} else {
			fmt.Fprintln(out, "  - not email the notice: email isn't set up (DASH_SMTP_*, DASH_PUBLIC_URL)")
		}
	}
	if banner {
		fmt.Fprintln(out, "  - show the notice after sign-in")
	}
	if csv != "" {
		fmt.Fprintf(out, "  - write %d names and email addresses to %s\n", p.Emails, csv)
	}
}

func printLockdownResult(out io.Writer, r store.LockdownResult) {
	if !r.Steps.Any() {
		return
	}
	fmt.Fprintln(out, "\nDone:")
	if r.Steps.Sessions {
		fmt.Fprintf(out, "  - signed out: %d sessions, %d API tokens and connected apps, %d runners, %d invitations, %d email links, %d wall links\n",
			r.Sessions, r.APITokens, len(r.Runners), r.Invitations, r.EmailLinks, r.WallLinks)
	}
	if r.Steps.Passwords {
		fmt.Fprintf(out, "  - %d accounts choose a new password at their next sign-in\n", r.Passwords)
	}
	if r.Steps.TOTP {
		fmt.Fprintf(out, "  - %d accounts set up two-factor sign-in again at their next sign-in\n", r.TOTP)
	}
	if r.Steps.Secrets {
		fmt.Fprintf(out, "  - new secrets for %d webhooks, %d GitHub webhooks, %d board backends; new VAPID key\n",
			len(r.Webhooks), len(r.GitHubHooks), len(r.Trackers))
	}
	list := r.Checklist()
	if len(list) == 0 {
		return
	}
	fmt.Fprintln(out, "\nStill to do outside Dash (new secrets are shown only now):")
	for i, c := range list {
		fmt.Fprintf(out, "%2d. %s\n", i+1, c.Text)
		if c.Secret != "" {
			fmt.Fprintf(out, "    %s\n", c.Secret)
		}
	}
}
