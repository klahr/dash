package textutil

import "strings"

// WithoutCode is Markdown s with its fenced code blocks and inline code
// spans each replaced by a space, so that what is quoted as code can't be
// read as a command. The fences follow the same rules as NoMentions.
func WithoutCode(s string) string {
	if !strings.ContainsAny(s, "`~") {
		return s
	}
	var out, chunk strings.Builder
	flush := func() {
		out.WriteString(withoutSpans(chunk.String()))
		chunk.Reset()
	}
	var fence string // the open fence's marker, "" outside a fenced block
	for _, line := range strings.SplitAfter(s, "\n") {
		if fence != "" {
			if m := fenceMarker(line); m != "" && m[0] == fence[0] && len(m) >= len(fence) &&
				strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), m[:1])) == "" {
				fence = ""
				out.WriteString(" ")
			}
			continue
		}
		if m := fenceMarker(line); m != "" {
			flush()
			fence = m
			continue
		}
		chunk.WriteString(line)
	}
	flush()
	if fence != "" { // an unclosed block runs to the end
		out.WriteString(" ")
	}
	return out.String()
}

// withoutSpans is WithoutCode for text outside fenced code blocks.
func withoutSpans(s string) string {
	if !strings.Contains(s, "`") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '`' {
			b.WriteByte(s[i])
			i++
			continue
		}
		n := 1
		for i+n < len(s) && s[i+n] == '`' {
			n++
		}
		if end := closingTicks(s, i+n, n); end >= 0 {
			b.WriteByte(' ')
			i = end
		} else {
			b.WriteString(s[i : i+n]) // no closing run: literal backticks
			i += n
		}
	}
	return b.String()
}
