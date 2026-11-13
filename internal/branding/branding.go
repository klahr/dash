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
	// TermsFile and PrivacyFile are HTML files on the server that Dash
	// serves itself at TermsPath and PrivacyPath, instead of linking
	// TermsURL and PrivacyURL elsewhere (optional).
	TermsFile, PrivacyFile string

	Landing    bool   // show the landing page at / to visitors who aren't signed in
	TeamPrice  string // the team plan's price as shown, e.g. "€8 per seat / month" (optional)
	PricingURL string // where "see pricing" links to (optional)
}

// WithDefaults fills in what's empty and trims the rest.
func (c Config) WithDefaults() Config {
	for _, p := range []*string{&c.Name, &c.SourceURL, &c.TermsURL, &c.PrivacyURL, &c.TermsFile, &c.PrivacyFile, &c.Contact, &c.Logo, &c.TeamPrice, &c.PricingURL} {
		*p = strings.TrimSpace(*p)
	}
	if c.TermsFile != "" && c.TermsURL == "" {
		c.TermsURL = TermsPath
	}
	if c.PrivacyFile != "" && c.PrivacyURL == "" {
		c.PrivacyURL = PrivacyPath
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
	for _, u := range []struct{ name, v, file, path string }{
		{"source URL", c.SourceURL, "", ""}, {"terms URL", c.TermsURL, c.TermsFile, TermsPath},
		{"privacy URL", c.PrivacyURL, c.PrivacyFile, PrivacyPath}, {"pricing URL", c.PricingURL, "", ""},
	} {
		if u.file != "" && u.v != u.path {
			errs = append(errs, fmt.Errorf("%s %q: set the URL or the file, not both", u.name, u.v))
			continue
		}
		if u.v == "" || u.file != "" {
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
	for _, f := range []struct{ name, path string }{{"logo", c.Logo}, {"terms file", c.TermsFile}, {"privacy file", c.PrivacyFile}} {
		if f.path == "" {
			continue
		}
		if st, err := os.Stat(f.path); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.name, err))
		} else if st.IsDir() {
			errs = append(errs, fmt.Errorf("%s %q is a directory", f.name, f.path))
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

// TermsPath and PrivacyPath serve TermsFile and PrivacyFile (public: people
// read them before they sign up).
const (
	TermsPath   = "/terms"
	PrivacyPath = "/privacy"
)

// PublicPath reports whether path is one of the branding's own pages,
// served without signing in: the logo, the terms, the privacy policy.
func PublicPath(path string) bool {
	return path == LogoPath || path == TermsPath || path == PrivacyPath
}

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

// MissingLegal names what a hub that strangers sign up on should publish
// but doesn't: the terms, the privacy policy, a contact address.
func (c Config) MissingLegal() []string {
	var out []string
	for _, v := range []struct{ name, v string }{
		{"terms (DASH_TERMS_URL or DASH_TERMS_FILE)", c.TermsURL}, {"privacy policy (DASH_PRIVACY_URL or DASH_PRIVACY_FILE)", c.PrivacyURL}, {"contact (DASH_CONTACT_EMAIL)", c.Contact},
	} {
		if strings.TrimSpace(v.v) == "" {
			out = append(out, v.name)
		}
	}
	return out
}

var signupNoteTmpl = template.Must(template.New("signup-note").Parse(
	`{{if or .TermsURL .PrivacyURL}}<p class="signup-note">` +
		`{{if .TermsURL}}By creating an account, you accept the <a href="{{.TermsURL}}" rel="noopener" target="_blank">terms of service</a>.{{end}}` +
		`{{if and .TermsURL .PrivacyURL}} {{end}}` +
		`{{if .PrivacyURL}}The <a href="{{.PrivacyURL}}" rel="noopener" target="_blank">privacy policy</a> says what we keep about you, and for how long.{{end}}` +
		`</p>{{end}}`))

// SignupNote is the line under the signup form that links the terms and
// the privacy policy; empty when neither is set.
func (c Config) SignupNote() template.HTML {
	var b bytes.Buffer
	_ = signupNoteTmpl.Execute(&b, c)
	return template.HTML(b.String())
}

// Page stamps c into one of the frontend's HTML files: {{DASH_NAME}},
// {{DASH_LOGO}}, <!--DASH_SIGNUP_NOTE--> and <!--DASH_FOOTER-->.
func (c Config) Page(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("{{DASH_NAME}}"), []byte(template.HTMLEscapeString(c.Name)))
	b = bytes.ReplaceAll(b, []byte("{{DASH_LOGO}}"), []byte(template.HTMLEscapeString(c.LogoURL())))
	b = bytes.ReplaceAll(b, []byte("<!--DASH_SIGNUP_NOTE-->"), []byte(c.SignupNote()))
	return bytes.ReplaceAll(b, []byte("<!--DASH_FOOTER-->"), []byte(c.Footer()))
}
