package textutil

import (
	"regexp"
	"strings"
)

// handle is a GitHub @mention at the start of a string: a user ("@claude")
// or a team ("@org/team", which also catches "@types/node").
var handle = regexp.MustCompile(`^@[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:/[A-Za-z0-9_](?:[A-Za-z0-9_.-]*[A-Za-z0-9_])?)?`)

// NoMentions is Markdown s with each bare @handle put in inline code
// ("@claude" becomes "`@claude`"), so that GitHub, which notifies the owner
// of every @handle in a pull request or comment, notifies no one. Handles
// already in inline code or a fenced code block, email addresses
// ("a@example.com") and handles in URLs ("https://example.com/@a") are left
// as they are.
func NoMentions(s string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	var out, chunk strings.Builder
	flush := func() {
		out.WriteString(inlineNoMentions(chunk.String()))
		chunk.Reset()
	}
	var fence string // the open fence's marker, "" outside a fenced block
	for _, line := range strings.SplitAfter(s, "\n") {
		if fence != "" {
			out.WriteString(line)
			if m := fenceMarker(line); m != "" && m[0] == fence[0] && len(m) >= len(fence) &&
				strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), m[:1])) == "" {
				fence = ""
			}
			continue
		}
		if m := fenceMarker(line); m != "" {
			flush()
			out.WriteString(line)
			fence = m
			continue
		}
		chunk.WriteString(line)
	}
	flush()
	return out.String()
}

// fenceMarker is the run of ``` or ~~~ (three or more) that opens line as a
// code fence (indented at most three spaces), or "".
func fenceMarker(line string) string {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 || len(t) < 3 || (t[0] != '`' && t[0] != '~') {
		return ""
	}
	n := 0
	for n < len(t) && t[n] == t[0] {
		n++
	}
	if n < 3 || (t[0] == '`' && strings.Contains(t[n:], "`")) {
		return ""
	}
	return t[:n]
}

// inlineNoMentions is NoMentions for text outside fenced code blocks: it
// skips inline code spans and wraps the other bare handles.
func inlineNoMentions(s string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '`':
			n := 1
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			if end := closingTicks(s, i+n, n); end >= 0 {
				b.WriteString(s[i:end])
				i = end
			} else {
				b.WriteString(s[i : i+n]) // no closing run: literal backticks
				i += n
			}
		case c == '@' && (i == 0 || !inWord(s[i-1])):
			if h := handle.FindString(s[i:]); h != "" {
				b.WriteString("`" + h + "`")
				i += len(h)
				continue
			}
			b.WriteByte(c)
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// closingTicks is the index just past the first run of exactly n backticks
// in s at or after from, or -1.
func closingTicks(s string, from, n int) int {
	for i := from; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '`' {
			j++
		}
		if j-i == n {
			return j
		}
		i = j
	}
	return -1
}

// inWord reports a byte before "@" that makes it no mention: one that can
// be in the local part of an email address (a letter, digit, "_", ".", "-",
// "+", or a byte of a non-ASCII letter), or "/" (a URL).
func inWord(c byte) bool {
	return c == '_' || c == '/' || c == '.' || c == '-' || c == '+' || c >= 0x80 ||
		('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}
