package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/klahr/dash/internal/branding"
	"github.com/klahr/dash/internal/localpath"
	"github.com/klahr/dash/internal/orgdomain"
	"github.com/klahr/dash/internal/store"
)

// Org subdomains (Auth.Domains): each org is served at <slug>.<base>.
//
// Session cookies stay host-only: a cookie for the base domain would also go
// to *.preview.<base>, where anyone's app runs. So a browser signs in on the
// base domain and is handed to an org's subdomain:
//
//  1. <slug>.<base> without a session sends the browser to <base>/go/<slug>,
//     which needs a session on the base domain (or sends it to sign in).
//  2. /go/<slug> checks the user is a member, and redirects to
//     <slug>.<base>/__dash/handoff with a token signed for that user, org and
//     host, good for HandoffTTL and only once.
//  3. The handoff starts a session on the org's host (a host-only cookie)
//     and redirects to where the browser was going.
//
// Every request to an org's host checks the user is still a member; anyone
// else gets the same 404 whether or not the org exists.

const (
	HandoffPath = "/__dash/handoff"
	HandoffTTL  = time.Minute
)

type hostSlugKey struct{}

// HostSlug is the org subdomain ctx's request came to, if it came to one
// (it may not be an org's: the slug is the host's label).
func HostSlug(ctx context.Context) (string, bool) {
	s, ok := ctx.Value(hostSlugKey{}).(string)
	return s, ok
}

type baseHostKey struct{}

// reqHost is the host the browser asked for: X-Forwarded-Host from a
// trusted proxy when it names the base domain or a host under it, else the
// Host header.
func (a *Auth) reqHost(r *http.Request) string {
	if a.TrustProxy && a.Domains != nil {
		if f := r.Header.Get("X-Forwarded-Host"); f != "" {
			if h := strings.TrimSpace(strings.Split(f, ",")[0]); a.Domains.Under(h) {
				return h
			}
		}
	}
	return r.Host
}

// scheme is the scheme links to Dash's other hosts get.
func (a *Auth) scheme(r *http.Request) string {
	if a.Secure(r) {
		return "https"
	}
	return a.Domains.Scheme()
}

// origin is the origin of slug's subdomain ("": the base domain), with the
// scheme and port r came with.
func (a *Auth) origin(r *http.Request, slug string) string {
	return a.Domains.URL(a.scheme(r), slug, orgdomain.Port(a.reqHost(r)))
}

// OrgOrigin is the origin r's org subdomain is reached at, if r came to
// one (runners and connectors are told it, so they act in that org).
func (a *Auth) OrgOrigin(r *http.Request) (string, bool) {
	slug, ok := HostSlug(r.Context())
	if !ok || a.Domains == nil {
		return "", false
	}
	return a.origin(r, slug), true
}

// hostOrg is the org whose subdomain r came to, by its current slug (an old
// slug that redirects isn't it).
func (a *Auth) hostOrg(r *http.Request) (store.Org, bool) {
	slug, ok := HostSlug(r.Context())
	if !ok {
		return store.Org{}, false
	}
	o, moved, err := a.acc.OrgBySlug(slug, a.now())
	return o, err == nil && !moved
}

// pinned reports whether id may be used on r's host: anywhere without org
// subdomains or off them; on an org's subdomain, only in that org.
func (a *Auth) pinned(r *http.Request, id *Identity) bool {
	if _, onOrg := HostSlug(r.Context()); !onOrg {
		return true
	}
	o, ok := a.hostOrg(r)
	return ok && id.Org != nil && id.Org.ID == o.ID
}

// sameOrigin rejects a browser request that changes something (or opens a
// WebSocket) from a page on another host: SameSite cookies don't stop that
// between subdomains of one site, which org subdomains are. Requests with
// a bearer token carry no ambient credentials and are let through.
func (a *Auth) sameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		if !isUpgrade(r) {
			return true
		}
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return true // not a browser, or an old one that doesn't send it; SameSite still holds
	}
	if _, ok := bearer(r); ok {
		return true
	}
	if u, err := url.Parse(o); err == nil && u.Host != "" && strings.EqualFold(u.Host, a.reqHost(r)) {
		return true
	}
	writeError(w, http.StatusForbidden, "cross-origin request refused")
	return false
}

func isUpgrade(r *http.Request) bool {
	return r.Header.Get("Upgrade") != "" || strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// isAPI reports whether path is one of the API's (JSON answers, no
// redirects to pages).
func isAPI(path string) bool {
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/ws/") || strings.HasPrefix(path, "/peer/")
}

// accountEntry are the public API calls that sign in or make an account:
// they happen on the base domain only.
func accountEntry(path string) bool {
	return strings.HasPrefix(path, "/api/login") || strings.HasPrefix(path, "/api/signup") ||
		strings.HasPrefix(path, "/api/password/") || strings.HasPrefix(path, "/api/invitations/")
}

// safeNext is next if it's a path on this host, else "/".
func safeNext(next string) string {
	return localpath.Safe(next)
}

// goURL is where on the base domain a browser goes to be handed to slug's
// subdomain, then on to next there.
func (a *Auth) goURL(r *http.Request, slug, next string) string {
	u := a.origin(r, "") + "/go/" + slug
	if next = safeNext(next); next != "/" {
		u += "?next=" + url.QueryEscape(next)
	}
	return u
}

// serveDomains handles what is different with org subdomains: the Origin
// check, framing, and the hosts other than the base domain. It returns
// whether it answered r; if not, r goes on (possibly with a new context).
func (a *Auth) serveDomains(w http.ResponseWriter, r *http.Request, next http.Handler) (*http.Request, bool) {
	if !a.sameOrigin(w, r) {
		return r, true
	}
	// Each host may be framed by itself only: not by another org's host,
	// and not by a preview.
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	switch kind, label := a.Domains.Parse(a.reqHost(r)); kind {
	case orgdomain.Invalid:
		if label == "www" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			http.Redirect(w, r, a.origin(r, "")+r.URL.RequestURI(), http.StatusMovedPermanently)
			return r, true
		}
		a.notFound(w, r, isAPI(r.URL.Path))
		return r, true
	case orgdomain.Org:
		a.serveOrgHost(w, r.WithContext(context.WithValue(r.Context(), hostSlugKey{}, label)), label, next)
		return r, true
	case orgdomain.Base:
		return r.WithContext(context.WithValue(r.Context(), baseHostKey{}, true)), false
	}
	return r, false
}

// serveOrgHost serves a request to slug's subdomain.
func (a *Auth) serveOrgHost(w http.ResponseWriter, r *http.Request, slug string, next http.Handler) {
	path := r.URL.Path
	api := isAPI(path)
	switch {
	case path == HandoffPath:
		a.handoff(w, r, slug)
		return
	case path == "/login.html": // signing in happens on the base domain, then comes back
		http.Redirect(w, r, a.goURL(r, slug, ""), http.StatusSeeOther)
		return
	case path == "/invite.html": // the browser keeps the #token
		http.Redirect(w, r, a.origin(r, "")+"/invite.html", http.StatusSeeOther)
		return
	case accountEntry(path) || path == "/go/" || strings.HasPrefix(path, "/go/"):
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if publicPath(path) || publicAsset(path) || a.oauth.public(path) || path == branding.LogoPath {
		next.ServeHTTP(w, r) // the OAuth endpoints pin to the host's org themselves
		return
	}
	if tok, ok := bearer(r); ok {
		a.serveToken(w, r, tok, api, next)
		return
	}
	id, ok := a.session(r)
	if !ok {
		if api {
			writeError(w, http.StatusUnauthorized, "login required")
			return
		}
		http.Redirect(w, r, a.goURL(r, slug, r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	o, moved, err := a.acc.OrgBySlug(slug, a.now())
	var m store.Membership
	if err == nil {
		m, err = a.acc.Membership(id.User.ID, o.ID)
	}
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrNotMember) {
			slog.Error("org host", "err", err)
		}
		if path == "/api/logout" {
			next.ServeHTTP(w, r)
			return
		}
		a.notFound(w, r, api)
		return
	}
	if moved { // renamed: the old name points to the new one for a while
		if api {
			a.notFound(w, r, api)
			return
		}
		http.Redirect(w, r, a.origin(r, o.Slug)+r.URL.RequestURI(), http.StatusMovedPermanently)
		return
	}
	id.Org = &m // the host's org is the one this request works in
	a.serveSession(w, r, id, api, next)
}

// ---- pages ----

var hostPage = template.Must(template.New("host").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · {{.Brand.Name}}</title>
<style>
body{font:16px system-ui,sans-serif;background:#111;color:#eee;display:flex;justify-content:center;padding:3rem 1rem;margin:0}
main{max-width:26rem;width:100%}h1{font-size:1.4rem}p{color:#bbb;line-height:1.45}a{color:#f0a37f}
ul{list-style:none;padding:0;margin:1.2rem 0}li{margin:.5rem 0}
li a{display:block;padding:.8rem 1rem;border:1px solid #333;border-radius:8px;background:#1a1a1a;color:#eee;text-decoration:none}
li a:hover,li a:focus{border-color:#d97757}li small{display:block;color:#999;margin-top:.2rem}
button{padding:.55rem 1rem;border-radius:6px;border:0;background:#333;color:#eee;font-size:.95rem;cursor:pointer}
.site-footer{margin-top:2rem;font-size:.85rem;color:#888;text-align:center}.site-footer a{color:#aaa}
</style></head><body><main>
<h1>{{.Title}}</h1>
{{if .Text}}<p>{{.Text}}</p>{{end}}
{{if .Orgs}}<ul>{{range .Orgs}}<li><a href="/go/{{.Slug}}">{{.Name}}<small>{{.Host}}</small></a></li>{{end}}</ul>{{end}}
{{if .Link}}<p><a href="{{.Link}}">{{.LinkText}}</a></p>{{end}}
{{if .SignOut}}<form method="post" action="/api/logout"><button>Sign out</button></form>{{end}}
{{.Brand.Footer}}</main></body></html>
`))

type pageOrg struct{ Name, Slug, Host string }

type hostPageData struct {
	Title, Text    string
	Orgs           []pageOrg
	Link, LinkText string
	SignOut        bool
	Brand          branding.Config
}

func (a *Auth) page(w http.ResponseWriter, code int, d hostPageData) {
	d.Brand = a.brand()
	var buf bytes.Buffer
	if err := hostPage.Execute(&buf, d); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}

// notFound answers for a host or org that isn't there, or isn't the
// caller's: the same either way.
func (a *Auth) notFound(w http.ResponseWriter, r *http.Request, api bool) {
	if api {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	a.page(w, http.StatusNotFound, hostPageData{
		Title:    "No such organization",
		Text:     "There is no organization here, or your account isn't a member of it.",
		Link:     a.origin(r, "") + "/",
		LinkText: "Go to " + a.Domains.Base,
	})
}

// picker answers / on the base domain for a signed-in user: the orgs to go
// to. With one org, it goes straight there.
func (a *Auth) picker(w http.ResponseWriter, r *http.Request, id *Identity) {
	ms, err := a.acc.Memberships(id.User.ID)
	if err != nil {
		slog.Error("org picker", "err", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}
	var orgs []pageOrg
	for _, m := range ms {
		if a.Domains.Usable(m.Slug) {
			orgs = append(orgs, pageOrg{Name: m.Name, Slug: m.Slug, Host: a.Domains.Host(m.Slug, "")})
		}
	}
	if len(orgs) == 1 && len(ms) == 1 {
		http.Redirect(w, r, "/go/"+orgs[0].Slug, http.StatusSeeOther)
		return
	}
	d := hostPageData{Title: "Choose an organization", Orgs: orgs, SignOut: true}
	if len(orgs) == 0 {
		d.Title, d.Text = "No organization", "Your account isn't a member of any organization. Ask an owner or admin for an invitation link."
	}
	a.page(w, http.StatusOK, d)
}

// ---- the handoff ----

// goOrg handles GET /go/{slug} on the base domain: it hands the signed-in
// browser to the org's subdomain, then on to ?next there.
func (a *Auth) goOrg(w http.ResponseWriter, r *http.Request) {
	id, ok := From(r.Context())
	if _, onOrg := HostSlug(r.Context()); !ok || id.Token != nil || onOrg {
		a.notFound(w, r, false)
		return
	}
	o, _, err := a.acc.OrgBySlug(r.PathValue("slug"), a.now())
	if err == nil {
		_, err = a.acc.Membership(id.User.ID, o.ID)
	}
	if err != nil || !a.Domains.Usable(o.Slug) {
		a.notFound(w, r, false)
		return
	}
	family, err := a.acc.EnsureSessionFamily(id.key, newFamily())
	if err != nil {
		slog.Error("handoff", "err", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}
	tok := a.newHandoff(handoff{User: id.User.ID, Org: o.ID, Slug: o.Slug, Family: family})
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, a.origin(r, o.Slug)+HandoffPath+"?t="+url.QueryEscape(tok)+"&next="+url.QueryEscape(safeNext(r.URL.Query().Get("next"))),
		http.StatusSeeOther)
}

// handoff handles HandoffPath on slug's subdomain: a token from goOrg
// becomes a session on this host.
func (a *Auth) handoff(w http.ResponseWriter, r *http.Request, slug string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h, err := a.takeHandoff(r.URL.Query().Get("t"), slug)
	if err != nil {
		a.page(w, http.StatusForbidden, hostPageData{
			Title: "Link expired", Text: "This sign-in link has expired or was used already.",
			Link: a.goURL(r, slug, r.URL.Query().Get("next")), LinkText: "Try again",
		})
		return
	}
	if _, err := a.acc.Membership(h.User, h.Org); err != nil {
		a.notFound(w, r, false)
		return
	}
	if err := a.startSessionIn(w, r, h.User, h.Org, h.Family); err != nil {
		slog.Error("handoff session", "err", err)
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
}

// handoff is what a handoff token says: who, into which org, on which host,
// in which session family.
type handoff struct {
	User, Org    int64
	Slug, Family string
	Expires      time.Time
	Nonce        string
}

var errHandoff = errors.New("invalid or expired handoff token")

func (a *Auth) signHandoff(payload string) string {
	m := hmac.New(sha256.New, a.handoffKey)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// newHandoff makes a token for h, good for HandoffTTL.
func (a *Auth) newHandoff(h handoff) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	h.Nonce = base64.RawURLEncoding.EncodeToString(b)
	h.Expires = a.now().Add(HandoffTTL)
	payload := strings.Join([]string{"v1", strconv.FormatInt(h.User, 10), strconv.FormatInt(h.Org, 10), h.Slug, h.Family,
		strconv.FormatInt(h.Expires.Unix(), 10), h.Nonce}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + a.signHandoff(payload)
}

// takeHandoff checks a token for slug's host and spends it: each works once.
func (a *Auth) takeHandoff(tok, slug string) (handoff, error) {
	p64, sig, ok := strings.Cut(tok, ".")
	pb, err := base64.RawURLEncoding.DecodeString(p64)
	if !ok || err != nil || !hmac.Equal([]byte(sig), []byte(a.signHandoff(string(pb)))) {
		return handoff{}, errHandoff
	}
	f := strings.Split(string(pb), "|")
	if len(f) != 7 || f[0] != "v1" {
		return handoff{}, errHandoff
	}
	h := handoff{Slug: f[3], Family: f[4], Nonce: f[6]}
	h.User, _ = strconv.ParseInt(f[1], 10, 64)
	h.Org, _ = strconv.ParseInt(f[2], 10, 64)
	exp, _ := strconv.ParseInt(f[5], 10, 64)
	h.Expires = time.Unix(exp, 0)
	now := a.now()
	if !now.Before(h.Expires) || !strings.EqualFold(h.Slug, slug) || h.User == 0 || h.Org == 0 {
		return handoff{}, errHandoff
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for n, until := range a.handoffsUsed {
		if !now.Before(until) {
			delete(a.handoffsUsed, n)
		}
	}
	if _, used := a.handoffsUsed[h.Nonce]; used {
		return handoff{}, errHandoff
	}
	a.handoffsUsed[h.Nonce] = h.Expires
	return h, nil
}

func newFamily() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// orgHosts is what /api/me says about org subdomains: where the base domain
// is, and the subdomain of the org the request works in.
func (a *Auth) orgHosts(r *http.Request, id *Identity) map[string]any {
	out := map[string]any{"base_url": a.origin(r, ""), "domain": a.Domains.Base}
	if id.Org != nil && a.Domains.Usable(id.Org.Slug) {
		out["org_url"] = a.origin(r, id.Org.Slug)
	}
	if slug, ok := HostSlug(r.Context()); ok {
		out["host_slug"] = slug
	}
	return out
}
