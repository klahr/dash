// Package runnerctl sets up a computer as a Dash runner: the install script
// the hub serves, the checks before registering, and the background service
// (`dash runner setup|install|uninstall|status`).
package runnerctl

import (
	"bytes"
	_ "embed"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"text/template"

	"github.com/klahr/dash/internal/tunnel"
)

// Where releases are published (see .goreleaser.yaml).
const (
	Releases  = "https://github.com/klahr/dash/releases"
	LatestAPI = "https://api.github.com/repos/klahr/dash/releases/latest"
)

//go:embed install.sh.tmpl
var scriptSrc string

var scriptTmpl = template.Must(template.New("install.sh").Funcs(template.FuncMap{"sq": ShellQuote}).Parse(scriptSrc))

// releaseRe matches the tags GoReleaser publishes, e.g. 1.2.0 or 1.2.0-rc.1
// (and v1.2.0, the format of earlier releases).
var releaseRe = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

// ReleaseVersion is the release a runner of this hub should install: the
// hub's own version, or "" (the latest release) for a build from source.
func ReleaseVersion(version string) string {
	if releaseRe.MatchString(version) {
		return version
	}
	return ""
}

// ScriptParams fill in the install script.
type ScriptParams struct {
	Hub       string // the hub's base URL, e.g. https://dash.example.com
	Version   string // release to install, e.g. 1.2.0; "" = latest
	Releases  string // default Releases
	LatestAPI string // default LatestAPI
}

// Script renders the install script for p. The hub URL must be one a runner
// may connect to (https, or http on loopback).
func Script(p ScriptParams) ([]byte, error) {
	u, err := tunnel.CheckServer(p.Hub)
	if err != nil {
		return nil, err
	}
	p.Hub = u.String()
	if p.Version != "" && !releaseRe.MatchString(p.Version) {
		return nil, fmt.Errorf("version %q is not a release", p.Version)
	}
	if p.Releases == "" {
		p.Releases = Releases
	}
	if p.LatestAPI == "" {
		p.LatestAPI = LatestAPI
	}
	var b bytes.Buffer
	if err := scriptTmpl.Execute(&b, p); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// ErrorScript is a script that only prints msg and fails, served when the
// hub can't make a working one (e.g. it is reached over plain HTTP).
func ErrorScript(msg string) []byte {
	return []byte("#!/bin/sh\nprintf '%s\\n' " + ShellQuote("dash install: "+msg) + " >&2\nexit 1\n")
}

// ShellQuote quotes s as one POSIX shell word.
func ShellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@%+,", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hostRe is what a Host header may hold for the hub URL baked into a script.
var hostRe = regexp.MustCompile(`^([A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.?|\[[0-9A-Fa-f:.]+\])(:[0-9]{1,5})?$`)

// HubURL is the hub's base URL: publicURL when set, else the origin r
// reached it at (scheme from secure, host from X-Forwarded-Host when
// trustProxy).
func HubURL(r *http.Request, publicURL string, secure, trustProxy bool) (string, error) {
	if publicURL != "" {
		return strings.TrimRight(publicURL, "/"), nil
	}
	host := r.Host
	if trustProxy {
		if f := r.Header.Get("X-Forwarded-Host"); f != "" {
			host = strings.TrimSpace(strings.Split(f, ",")[0])
		}
	}
	if !hostRe.MatchString(host) {
		return "", fmt.Errorf("odd host %q", host)
	}
	scheme := "http"
	if secure {
		scheme = "https"
	}
	return scheme + "://" + host, nil
}

// ScriptHandler serves GET /runner/install.sh. It holds no secrets (the
// registration code is passed to the script), so it needs no sign-in.
func ScriptHandler(publicURL, version string, secure func(*http.Request) bool, trustProxy bool) http.HandlerFunc {
	return ScriptHandlerFor(func(r *http.Request) (string, error) { return HubURL(r, publicURL, secure(r), trustProxy) }, version)
}

// ScriptHandlerFor is ScriptHandler with the hub URL baked into the script
// from hubURL (with org subdomains: the subdomain the script was fetched
// from).
func ScriptHandlerFor(hubURL func(*http.Request) (string, error), version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		hub, err := hubURL(r)
		var b []byte
		if err == nil {
			b, err = Script(ScriptParams{Hub: hub, Version: ReleaseVersion(version)})
		}
		if err != nil {
			b = ErrorScript("this hub can't serve a working installer (" + err.Error() +
				"): runners need to reach the hub over HTTPS. Serve it over HTTPS or set -public-url (see docs/deploy.md).")
		}
		_, _ = w.Write(b)
	}
}
