package mail

import (
	"bytes"
	"html/template"
	"strings"
)

// Page is an email's HTML version: the product's name across the top, a
// heading and the content on a white card, and small print under it. It
// is built from tables with inline styles, which is what mail apps show
// alike (Gmail, Outlook and Apple Mail drop or ignore much of the rest).
type Page struct {
	Brand string // the product's name, at the top
	// Preheader is the line mail apps show after the subject in the inbox.
	Preheader string
	Title     string        // the heading
	Body      template.HTML // the content, made with the helpers below
	Footer    []template.HTML
}

// The palette: Dash's own colours, on white, each readable on its
// background (WCAG AA).
const (
	colInk    = "#1b2433"
	colMuted  = "#5b6678"
	colLine   = "#e3e8f0"
	colPage   = "#f3f5f9"
	colBand   = "#101722"
	colAccent = "#3b55d9"
	fontStack = `-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif`
)

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light"><meta name="supported-color-schemes" content="light">
<title>{{.Title}}</title></head>
<body style="margin:0;padding:0;background:` + colPage + `;color:` + colInk + `;font-family:` + fontStack + `;-webkit-text-size-adjust:100%">
{{if .Preheader}}<div style="display:none;max-height:0;overflow:hidden;opacity:0;mso-hide:all">{{.Preheader}}</div>{{end}}
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:` + colPage + `">
<tr><td align="center" style="padding:32px 16px">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="max-width:600px">
<tr><td style="background:` + colBand + `;border-radius:10px 10px 0 0;padding:18px 32px;color:#ffffff;font-size:17px;font-weight:700;letter-spacing:.2px">{{.Brand}}</td></tr>
<tr><td style="background:#ffffff;border:1px solid ` + colLine + `;border-top:0;border-radius:0 0 10px 10px;padding:32px;font-size:15px;line-height:1.6">
<h1 style="margin:0 0 16px;font-size:21px;line-height:1.3;font-weight:700;color:` + colInk + `">{{.Title}}</h1>
{{.Body}}
</td></tr>
{{if .Footer}}<tr><td style="padding:20px 32px 0;font-size:12px;line-height:1.6;color:` + colMuted + `;text-align:center">
{{range .Footer}}<p style="margin:0 0 6px">{{.}}</p>{{end}}
</td></tr>{{end}}
</table>
</td></tr>
</table>
</body></html>
`))

// HTML renders the page.
func (p Page) HTML() string {
	var b bytes.Buffer
	if err := pageTmpl.Execute(&b, p); err != nil {
		return "" // the template is fixed and its fields are strings: can't happen
	}
	return b.String()
}

var partTmpl = template.Must(template.New("parts").Parse(
	`{{define "p"}}<p style="margin:0 0 16px">{{.}}</p>{{end}}` +
		`{{define "muted"}}<p style="margin:0 0 12px;font-size:13px;color:` + colMuted + `">{{.}}</p>{{end}}` +
		`{{define "button"}}<table role="presentation" cellpadding="0" cellspacing="0" border="0" style="margin:8px 0 24px"><tr>` +
		`<td style="border-radius:6px;background:` + colAccent + `"><a href="{{.URL}}" style="display:inline-block;padding:12px 24px;` +
		`font-size:15px;font-weight:600;color:#ffffff;text-decoration:none;border-radius:6px">{{.Label}}</a></td></tr></table>{{end}}` +
		`{{define "spelled"}}<p style="margin:0 0 16px;font-size:13px;color:` + colMuted + `">Or copy this link into your browser:<br>` +
		`<a href="{{.URL}}" style="color:` + colAccent + `;word-break:break-all">{{.URL}}</a></p>{{end}}` +
		`{{define "link"}}<a href="{{.URL}}" style="color:` + colAccent + `;text-decoration:none">{{.Label}}</a>{{end}}` +
		`{{define "rule"}}<hr style="border:0;border-top:1px solid ` + colLine + `;margin:24px 0">{{end}}`))

func part(name string, data any) template.HTML {
	var b bytes.Buffer
	if err := partTmpl.ExecuteTemplate(&b, name, data); err != nil {
		return ""
	}
	return template.HTML(b.String())
}

// P is a paragraph of text.
func P(text string) template.HTML { return part("p", text) }

// Muted is a paragraph of small grey text.
func Muted(text string) template.HTML { return part("muted", text) }

// Button is a call to action.
func Button(label, url string) template.HTML {
	return part("button", Link{Label: label, URL: url})
}

// ButtonAndLink is a button with its link written out under it, for a
// link that matters (confirm an address, reset a password) in mail apps
// that hide buttons.
func ButtonAndLink(label, url string) template.HTML {
	return Join(Button(label, url), part("spelled", Link{Label: label, URL: url}))
}

// A is a link.
func A(label, url string) template.HTML {
	return part("link", Link{Label: label, URL: url})
}

// Rule is a thin line between sections.
func Rule() template.HTML { return part("rule", nil) }

// Link is a label and where it goes.
// The URL is checked as html/template checks any: only http(s) and mailto
// links stay.
type Link struct {
	Label string
	URL   string
}

// Join puts pieces of HTML one after another.
func Join(parts ...template.HTML) template.HTML {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(string(p))
	}
	return template.HTML(b.String())
}

// Notice is a short account email (confirm an address, reset a password):
// a greeting, what happened, one action, and what to do if it wasn't you.
// Message makes both its plain-text and its HTML version.
type Notice struct {
	Brand    string // the product's name
	Contact  string // an address to write to with questions (optional)
	Title    string // the HTML version's heading
	Greeting string // "Hello," or "Hello ann,"
	Before   []string
	Action   string // the button's label (optional)
	URL      string // where it goes
	After    []string
}

// Message is the notice to one address.
func (n Notice) Message(to, subject string) Message {
	var t strings.Builder
	var h []template.HTML
	t.WriteString(n.Greeting + "\n\n")
	h = append(h, P(n.Greeting))
	for _, s := range n.Before {
		t.WriteString(s + "\n\n")
		h = append(h, P(s))
	}
	if n.URL != "" {
		t.WriteString(n.URL + "\n\n")
		h = append(h, ButtonAndLink(n.Action, n.URL))
	}
	for _, s := range n.After {
		t.WriteString(s + "\n\n")
		h = append(h, Muted(s))
	}
	t.WriteString("-- \n" + n.Brand + "\n")
	foot := []template.HTML{template.HTML(template.HTMLEscapeString("Sent by " + n.Brand + "."))}
	if n.Contact != "" {
		t.WriteString("Questions? Write to " + n.Contact + "\n")
		foot = append(foot, Join(template.HTML("Questions? Write to "), A(n.Contact, "mailto:"+n.Contact)))
	}
	preheader := ""
	if len(n.Before) > 0 {
		preheader = n.Before[0]
	}
	page := Page{Brand: n.Brand, Preheader: preheader, Title: n.Title, Body: Join(h...), Footer: foot}
	return Message{To: to, Subject: subject, Body: t.String(), HTML: page.HTML()}
}
