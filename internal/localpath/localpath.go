// Package localpath checks redirect targets that must stay on the same host.
package localpath

import (
	"net/url"
	"strings"
)

// Safe returns next if it is a path (with optional query and fragment) on
// the current host, else "/". Browsers strip tabs and newlines from URLs and
// treat "\" like "/", so any control byte or backslash anywhere is refused:
// "/\t/evil.com" would otherwise become the protocol-relative "//evil.com".
func Safe(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	for i := 0; i < len(next); i++ {
		if c := next[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return "/"
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" ||
		!strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	return next
}
