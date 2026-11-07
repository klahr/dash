// Package branding is how a hosted Dash presents itself: its name, legal
// links, contact address and logo, the "Source code" link the AGPL asks for,
// and the optional landing page. Nothing about one deployment is built in;
// every value comes from flags or DASH_* variables (see docs/branding.md).
package branding

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"net/mail"
	"net/url"
	"os"
	"strings"
)

// Defaults for what isn't configured.
const (
	DefaultName      = "Dash"
	DefaultSourceURL = "https://github.com/klahr/dash"
)

// Config is the deployment's presentation.
type Config struct {
	Name       string // product name in titles, pages and emails
	SourceURL  string // where the running code's source is (AGPL §13)
	TermsURL   string // terms of service (optional)
	PrivacyURL string // privacy policy (optional)
	Contact    string // an email address people can write to (optional)
	Logo       string // a file on disk shown instead of the built-in mark (optional)

	Landing    bool   // show the landing page at / to visitors who aren't signed in
	TeamPrice  string // the team plan's price as shown, e.g. "€8 per seat / month" (optional)
	PricingURL string // where "see pricing" links to (optional)
}

// WithDefaults fills in what's empty and trims the rest.
func (c Config) WithDefaults() Config {
	for _, p := range []*string{&c.Name, &c.SourceURL, &c.TermsURL, &c.PrivacyURL, &c.Contact, &c.Logo, &c.TeamPrice, &c.PricingURL} {
		*p = strings.TrimSpace(*p)
	}
	if c.Name == "" {
		c.Name = DefaultName
	}
	if c.SourceURL == "" {
		c.SourceURL = DefaultSourceURL
	}
	return c
}

// Check reports values that would make broken links: URLs that aren't
// http(s), a contact that isn't an address, a logo that can't be read.
func (c Config) Check() error {
	var errs []error
	for _, u := range []struct{ name, v string }{
		{"source URL", c.SourceURL}, {"terms URL", c.TermsURL}, {"privacy URL", c.PrivacyURL}, {"pricing URL", c.PricingURL},
	} {
		if u.v == "" {
			continue
		}
		p, err := url.Parse(u.v)
		if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" {
			errs = append(errs, fmt.Errorf("%s %q: want an http(s) URL", u.name, u.v))
		}
	}
	if c.Contact != "" {
		if a, err := mail.ParseAddress(c.Contact); err != nil || a.Address != c.Contact {
			errs = append(errs, fmt.Errorf("contact email %q: want a bare address like help@example.com", c.Contact))
		}
	}
	if c.Logo != "" {
		if st, err := os.Stat(c.Logo); err != nil {
			errs = append(errs, fmt.Errorf("logo: %w", err))
		} else if st.IsDir() {
			errs = append(errs, fmt.Errorf("logo %q is a directory", c.Logo))
		}
	}
	return errors.Join(errs...)
}

// LogoURL is where pages load the logo from.
func (c Config) LogoURL() string {
	if c.Logo != "" {
		return LogoPath
	}
	return "/favicon.svg"
}

// LogoPath serves the configured logo (public, like the login page).
const LogoPath = "/brand/logo"

// MailFooter closes Dash's emails: who sent it, and where to write.
func (c Config) MailFooter() string {
	s := "\n-- \n" + c.Name + "\n"
	if c.Contact != "" {
		s += "Questions? Write to " + c.Contact + "\n"
	}
	return s
}

var footerTmpl = template.Must(template.New("footer").Parse(
	`<footer class="site-footer"><a href="{{.SourceURL}}" rel="noopener">Source code</a>` +
		`{{if .TermsURL}} · <a href="{{.TermsURL}}" rel="noopener">Terms</a>{{end}}` +
		`{{if .PrivacyURL}} · <a href="{{.PrivacyURL}}" rel="noopener">Privacy</a>{{end}}` +
		`{{if .Contact}} · <a href="mailto:{{.Contact}}">Contact</a>{{end}}</footer>`))

// Footer is the links every page ends with: the source code (AGPL §13),
// then the terms, privacy policy and contact address when set.
func (c Config) Footer() template.HTML {
	var b bytes.Buffer
	_ = footerTmpl.Execute(&b, c)
	return template.HTML(b.String())
}

// Page stamps c into one of the frontend's HTML files: {{DASH_NAME}},
// {{DASH_LOGO}} and <!--DASH_FOOTER-->.
func (c Config) Page(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("{{DASH_NAME}}"), []byte(template.HTMLEscapeString(c.Name)))
	b = bytes.ReplaceAll(b, []byte("{{DASH_LOGO}}"), []byte(template.HTMLEscapeString(c.LogoURL())))
	return bytes.ReplaceAll(b, []byte("<!--DASH_FOOTER-->"), []byte(c.Footer()))
}
