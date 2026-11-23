// Package textutil shortens text for logs, prompts and commit subjects
// without splitting a UTF-8 character.
package textutil

import (
	"strings"
	"unicode/utf8"
)

// Clip is s cut to at most n bytes, at a character boundary, with "…"
// added when anything was cut.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 0 {
		n = 0
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// FirstLine is s's first line: s trimmed, cut at its first newline, and
// trimmed again.
func FirstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}

// LastLine is s's last line that says something: s trimmed, cut after its
// last newline, and trimmed again.
func LastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

// Summary is s's first line, with " …" when more lines follow, clipped to n
// bytes: a one-line preview of s for a log.
func Summary(s string, n int) string {
	line := FirstLine(s)
	if strings.Contains(strings.TrimSpace(s), "\n") {
		line += " …"
	}
	return Clip(line, n)
}

// OneLine is s with its runs of whitespace, newlines included, collapsed to
// single spaces, clipped so that it, with "…" added when anything was cut,
// is at most n characters long.
func OneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// Fence wraps text in a Markdown code fence longer than any run of
// backticks in it, so nothing in it can close the fence early.
func Fence(text string) string {
	n, run := 3, 0
	for _, c := range text {
		if c == '`' {
			run++
			n = max(n, run+1)
		} else {
			run = 0
		}
	}
	f := strings.Repeat("`", n)
	return f + "\n" + text + "\n" + f
}
