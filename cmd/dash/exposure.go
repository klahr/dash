package main

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sort"
	"strings"
)

// The startup checks of an internet-facing Dash (-public-url or
// -trust-proxy): each refuses to start on an unsafe setting unless named in
// -allow-unsafe (or $DASH_ALLOW_UNSAFE), which turns it into a loud warning.
// See docs/deploy.md, "The login is internet-facing".
const (
	checkPassword   = "password"   // DASH_PASSWORD missing, short or a known default
	checkPublicURL  = "public-url" // -public-url isn't https
	checkProxyAddr  = "proxy-addr" // -trust-proxy while listening beyond loopback
	checkSignupMail = "signup"     // open signup without working email to verify addresses
)

var exposureChecks = []string{checkPassword, checkPublicURL, checkProxyAddr, checkSignupMail}

// minPublicPassword is the shortest DASH_PASSWORD an internet-facing Dash
// accepts.
const minPublicPassword = 16

// knownPasswords are the examples in Dash's docs and .env files, and the
// usual defaults: guessed first, whatever their length.
var knownPasswords = []string{
	"change-me", "changeme", "change-me-please", "password", "passw0rd", "secret", "admin", "dash",
	"a-long-random-password", "a-long-enough-password", "correct horse battery staple",
	"123456789012345678", "1234567890123456", "qwertyuiopasdfgh",
}

// exposure is what the checks look at.
type exposure struct {
	PublicURL   string
	TrustProxy  bool
	Addr        string
	Username    string
	Password    string // DASH_PASSWORD; empty when unset
	SignupOpen  bool   // anyone may create an account
	MailOn      bool   // email works, so signup verifies addresses
	AllowUnsafe string // -allow-unsafe: comma-separated check names, or "all"
}

// Public reports whether Dash is meant to be reached from the internet.
func (e exposure) Public() bool { return e.PublicURL != "" || e.TrustProxy }

// problem is one unsafe setting, by the check that found it.
type problem struct {
	Check, Msg string
}

// problems lists the unsafe settings of an internet-facing Dash; none when
// it isn't one.
func (e exposure) problems() []problem {
	if !e.Public() {
		return nil
	}
	var out []problem
	if msg := weakPassword(e.Username, e.Password); msg != "" {
		out = append(out, problem{checkPassword, msg})
	}
	if e.PublicURL != "" {
		if u, err := url.Parse(e.PublicURL); err != nil || u.Host == "" || !strings.EqualFold(u.Scheme, "https") && !loopbackHost(u.Hostname()) {
			out = append(out, problem{checkPublicURL, fmt.Sprintf("-public-url %q isn't an https:// URL: passwords and session cookies would cross the internet unencrypted", e.PublicURL)})
		}
	}
	if e.TrustProxy && !isLoopback(e.Addr) {
		out = append(out, problem{checkProxyAddr, fmt.Sprintf("-trust-proxy while listening on %s: anyone who reaches that address directly can forge X-Forwarded-For (dodging the sign-in lockout) and X-Forwarded-Proto. "+
			"Listen on 127.0.0.1 behind the proxy, or, when only the proxy can reach the port (a container network), say so with -allow-unsafe=%s", e.Addr, checkProxyAddr)})
	}
	if e.SignupOpen && !e.MailOn {
		out = append(out, problem{checkSignupMail, "open signup without working email: new accounts' addresses can't be verified"})
	}
	return out
}

// weakPassword says what's wrong with DASH_PASSWORD on an internet-facing
// Dash, or "" when nothing is.
func weakPassword(user, pass string) string {
	switch p := strings.ToLower(strings.TrimSpace(pass)); {
	case pass == "":
		return "DASH_PASSWORD isn't set, so the first account's password can't be checked: set it to a long random one (openssl rand -base64 24)"
	case len(pass) < minPublicPassword:
		return fmt.Sprintf("DASH_PASSWORD is %d characters: an internet-facing Dash needs at least %d (openssl rand -base64 24)", len(pass), minPublicPassword)
	case user != "" && p == strings.ToLower(strings.TrimSpace(user)):
		return "DASH_PASSWORD is DASH_USERNAME: use a long random one (openssl rand -base64 24)"
	default:
		for _, k := range knownPasswords {
			if p == k {
				return "DASH_PASSWORD is a well-known example password: use a long random one (openssl rand -base64 24)"
			}
		}
		if distinct(p) < 5 {
			return "DASH_PASSWORD repeats a few characters: use a long random one (openssl rand -base64 24)"
		}
	}
	return ""
}

func distinct(s string) int {
	seen := map[rune]bool{}
	for _, r := range s {
		seen[r] = true
	}
	return len(seen)
}

func loopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// parseAllowUnsafe reads -allow-unsafe: check names, or "all".
func parseAllowUnsafe(v string) (map[string]bool, error) {
	allowed := map[string]bool{}
	for _, name := range strings.Split(v, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		switch {
		case name == "":
		case name == "all":
			for _, c := range exposureChecks {
				allowed[c] = true
			}
		case contains(exposureChecks, name):
			allowed[name] = true
		default:
			return nil, fmt.Errorf("unknown check %q: want all or some of %s", name, strings.Join(exposureChecks, ","))
		}
	}
	return allowed, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// checkExposure logs the unsafe settings of an internet-facing Dash and
// returns an error when any of them isn't allowed by -allow-unsafe.
func checkExposure(e exposure) error {
	allowed, err := parseAllowUnsafe(e.AllowUnsafe)
	if err != nil {
		return fmt.Errorf("-allow-unsafe: %w", err)
	}
	var refused []string
	for _, p := range e.problems() {
		if allowed[p.Check] {
			slog.Warn("UNSAFE: internet-facing Dash started anyway (-allow-unsafe="+p.Check+")", "problem", p.Msg)
			continue
		}
		slog.Error("unsafe for an internet-facing Dash", "check", p.Check, "problem", p.Msg)
		refused = append(refused, p.Check)
	}
	if len(refused) > 0 {
		sort.Strings(refused)
		return fmt.Errorf("refusing to start: fix the settings above, or accept the risk with -allow-unsafe=%s (see docs/deploy.md, \"The login is internet-facing\")",
			strings.Join(refused, ","))
	}
	return nil
}
