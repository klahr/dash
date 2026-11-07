// Package orgdomain tells the hosts of a Dash with org subdomains apart:
// the base domain (dash.example.com: landing page, sign-in, the org
// picker), each org's subdomain (acme.dash.example.com) and everything else
// (localhost, an IP address: served as a single host, as without org
// subdomains). It is off unless a base domain is configured.
package orgdomain

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/klahr/dash/internal/store"
)

// Kind is what a host is to Dash.
type Kind int

const (
	// Elsewhere: not the base domain or under it (localhost, an IP, another
	// name): served as Dash always was, on its own.
	Elsewhere Kind = iota
	// Base is the base domain itself.
	Base
	// Org is <slug>.<base> for a slug that may be an org's: valid and not
	// reserved. Whether an org has it is the store's to say.
	Org
	// Invalid is a name under the base domain that can't be an org's: a
	// reserved label, a malformed one, or one more level down.
	Invalid
)

// Domain is the base domain orgs get subdomains of.
type Domain struct {
	// Base is the base domain, lower-case, without port or trailing dot.
	Base     string
	reserved map[string]bool
	// scheme and port of URLs made without a request to go by (see Origin):
	// from -public-url when it is on the base domain, else https.
	scheme, port string
	public       bool // UseDefaults found a public URL on the base domain
}

// New makes the Domain for base. reserved are the labels no org may have
// (nil: store.DefaultReservedSlugs); the labels the preview domain takes
// (its first, and the one under base when it is under base) are always
// reserved too, so previews and org subdomains never share a host.
func New(base string, reserved []string, previewDomain string) (*Domain, error) {
	base = strings.Trim(strings.ToLower(strings.TrimSpace(base)), ".")
	if err := checkDomain(base); err != nil {
		return nil, fmt.Errorf("org domain %q: %w", base, err)
	}
	d := &Domain{Base: base, reserved: map[string]bool{}, scheme: "https"}
	if reserved == nil {
		reserved = store.DefaultReservedSlugs
	}
	for _, r := range reserved {
		if r = strings.ToLower(strings.TrimSpace(r)); r != "" {
			d.reserved[r] = true
		}
	}
	if p := strings.Trim(strings.ToLower(strings.TrimSpace(previewDomain)), "."); p != "" {
		switch {
		case p == base:
			return nil, errors.New("the preview domain and the org domain must differ: use preview." + base + " for previews")
		case strings.HasSuffix(base, "."+p):
			return nil, fmt.Errorf("the org domain %s can't be under the preview domain %s", base, p)
		}
		first, _, _ := strings.Cut(p, ".")
		d.reserved[first] = true
		if rest, ok := strings.CutSuffix(p, "."+base); ok {
			labels := strings.Split(rest, ".")
			d.reserved[labels[len(labels)-1]] = true
		}
	}
	return d, nil
}

// checkDomain checks that d is a DNS name of two labels or more.
func checkDomain(d string) error {
	if d == "" {
		return errors.New("empty")
	}
	if net.ParseIP(d) != nil {
		return errors.New("want a domain name, not an IP address")
	}
	if strings.ContainsAny(d, ":/ ") {
		return errors.New("want a bare domain name like dash.example.com, without scheme, port or path")
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 || len(d) > 253 {
		return errors.New("want a domain name like dash.example.com")
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return fmt.Errorf("bad label %q", l)
		}
		for i := 0; i < len(l); i++ {
			if c := l[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return fmt.Errorf("bad label %q", l)
			}
		}
	}
	return nil
}

// ParseList splits a list of reserved names given as one setting: comma-
// or space-separated. Empty: nil (the defaults).
func ParseList(s string) []string {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
	if len(f) == 0 {
		return nil
	}
	return f
}

// UseDefaults takes the scheme and port of URLs made without a request
// (Origin) from publicURL, when it is on the base domain.
func (d *Domain) UseDefaults(publicURL string) {
	u, err := url.Parse(publicURL)
	if err != nil || !strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), d.Base) {
		return
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		d.scheme = u.Scheme
	}
	d.port = u.Port()
	d.public = true
}

// Scheme is the scheme of links to Dash's hosts for a request that didn't
// come over HTTPS (as far as Dash can tell): the public URL's when it is on
// the base domain, else http.
func (d *Domain) Scheme() string {
	if d.public {
		return d.scheme
	}
	return "http"
}

// Reserved reports whether no org may have label as its slug.
func (d *Domain) Reserved(label string) bool { return d.reserved[strings.ToLower(label)] }

// ReservedList is the reserved labels, sorted.
func (d *Domain) ReservedList() []string {
	out := make([]string, 0, len(d.reserved))
	for r := range d.reserved {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// Usable reports whether slug may be an org's subdomain.
func (d *Domain) Usable(slug string) bool { return store.ValidSlug(slug) && !d.Reserved(slug) }

// Hostname is host (from a Host header: maybe with a port, maybe an IPv6
// literal) lower-case, without port, brackets or trailing dot.
func Hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// Port is the port in host, if any.
func Port(host string) string {
	if _, p, err := net.SplitHostPort(host); err == nil {
		return p
	}
	return ""
}

// Parse says what host (a Host header) is, and for Org, the slug.
func (d *Domain) Parse(host string) (Kind, string) {
	h := Hostname(host)
	if h == d.Base {
		return Base, ""
	}
	label, ok := strings.CutSuffix(h, "."+d.Base)
	if !ok || label == "" {
		return Elsewhere, ""
	}
	if !d.Usable(label) {
		return Invalid, label
	}
	return Org, label
}

// Under reports whether host is the base domain or a name under it.
func (d *Domain) Under(host string) bool {
	k, _ := d.Parse(host)
	return k != Elsewhere
}

// Host is slug's host name ("": the base domain's), with port when not "".
func (d *Domain) Host(slug, port string) string {
	h := d.Base
	if slug != "" {
		h = slug + "." + d.Base
	}
	if port != "" {
		h += ":" + port
	}
	return h
}

// URL is the origin of slug's host ("": the base domain's) with scheme and
// port.
func (d *Domain) URL(scheme, slug, port string) string {
	return scheme + "://" + d.Host(slug, port)
}

// Origin is URL with the scheme and port from UseDefaults, for links made
// without a request to go by.
func (d *Domain) Origin(slug string) string { return d.URL(d.scheme, slug, d.port) }
