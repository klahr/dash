// Package secretscan finds credentials in text before it leaves this
// computer: well-known token formats (cloud, GitHub, Slack, Stripe, AI
// providers, Dash's own tokens) and private keys. It errs on the side of
// precision: a hit should be a real secret, so callers can refuse outright.
package secretscan

import (
	"regexp"
	"slices"
)

// Finding is one secret found in text.
type Finding struct {
	Kind string // what it looks like, e.g. "GitHub token"
	// Offset is where it starts in the text (bytes). The secret itself
	// isn't kept, so a finding can be logged or shown safely.
	Offset int
}

type rule struct {
	kind string
	re   *regexp.Regexp
}

// Token formats. Each is anchored on a distinctive prefix and a length
// typical of the real thing, so prose mentioning "sk-" doesn't trip it.
var rules = []rule{
	{"private key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP |ENCRYPTED )?PRIVATE KEY(?: BLOCK)?-----`)},
	{"AWS access key", regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16}\b`)},
	{"AWS secret key", regexp.MustCompile(`(?i)aws_?secret_?access_?key["'\s:=]+[A-Za-z0-9/+]{40}\b`)},
	{"GitHub token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,255}|github_pat_[A-Za-z0-9_]{60,255})\b`)},
	{"GitLab token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}\b`)},
	{"Slack webhook", regexp.MustCompile(`https://hooks\.slack\.com/services/T[A-Za-z0-9_]+/B[A-Za-z0-9_]+/[A-Za-z0-9_]+`)},
	{"Stripe key", regexp.MustCompile(`\b(?:sk|rk)_live_[A-Za-z0-9]{20,}\b`)},
	{"Anthropic API key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"OpenAI API key", regexp.MustCompile(`\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}T3BlbkFJ[A-Za-z0-9_-]{20,}`)},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"Google OAuth secret", regexp.MustCompile(`\bGOCSPX-[A-Za-z0-9_-]{28}\b`)},
	{"npm token", regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`)},
	{"PyPI token", regexp.MustCompile(`\bpypi-AgEIcHlwaS5vcmc[A-Za-z0-9_-]{50,}`)},
	{"SendGrid key", regexp.MustCompile(`\bSG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}\b`)},
	{"JSON web token", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{20,}`)},
	// Dash's own secrets (store/tokens.go, walltokens.go, webhooks.go,
	// tracker.go): API, OAuth, runner and wall tokens, signing secrets.
	{"Dash token", regexp.MustCompile(`\b(?:dash[orw]?|dr|whsec|trsec)_[A-Za-z0-9_-]{20,}`)},
	{"credentials in a URL", regexp.MustCompile(`\b[a-z][a-z0-9+.-]*://[^\s/:@]+:[^\s/@]{6,}@[A-Za-z0-9.-]+`)},
}

// Scan returns the secrets in text, in order of where they start.
func Scan(text string) []Finding {
	var out []Finding
	for _, r := range rules {
		for _, loc := range r.re.FindAllStringIndex(text, -1) {
			out = append(out, Finding{Kind: r.kind, Offset: loc[0]})
		}
	}
	slices.SortFunc(out, func(a, b Finding) int { return a.Offset - b.Offset })
	return out
}

// Has reports whether text holds a secret, and the first one's kind.
func Has(text string) (kind string, found bool) {
	if fs := Scan(text); len(fs) > 0 {
		return fs[0].Kind, true
	}
	return "", false
}
