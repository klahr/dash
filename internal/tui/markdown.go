package tui

import (
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
)

var (
	mdFence   = regexp.MustCompile("^\\s*(```+|~~~+)")
	mdHeading = regexp.MustCompile(`^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$`)
	mdRule    = regexp.MustCompile(`^\s{0,3}([-*_])(\s*[-*_]){2,}\s*$`)
	mdQuote   = regexp.MustCompile(`^\s*>\s?(.*)$`)
	mdList    = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(\[[ xX]\]\s+)?(.*)$`)
	mdCode    = regexp.MustCompile("``(.+?)``|`([^`]+)`")
	mdStrong  = regexp.MustCompile(`(\*\*|__)(\S(?:.*?\S)?)(\*\*|__)`)
	mdEm      = regexp.MustCompile(`(^|[^*\w])[*_](\S(?:[^*_]*?\S)?)[*_]($|[^*\w])`)
	mdLink    = regexp.MustCompile(`!?\[([^\]]*)\]\(([^)\s]+)[^)]*\)`)

	styleMdCode = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleMdH    = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
)

// mdInline styles code spans, bold, italics and links on one line. Code
// spans are cut out first so the other rules leave their contents alone.
func mdInline(s string) string {
	parts := mdCode.FindAllStringSubmatchIndex(s, -1)
	var b strings.Builder
	last := 0
	for _, p := range parts {
		b.WriteString(mdText(s[last:p[0]]))
		code := s[p[4]:p[5]]
		if p[2] >= 0 {
			code = s[p[2]:p[3]]
		}
		b.WriteString(styleMdCode.Render(strings.TrimSpace(code)))
		last = p[1]
	}
	b.WriteString(mdText(s[last:]))
	return b.String()
}

func mdText(s string) string {
	s = mdLink.ReplaceAllStringFunc(s, func(m string) string {
		sm := mdLink.FindStringSubmatch(m)
		if sm[1] == "" || sm[1] == sm[2] {
			return lipgloss.NewStyle().Underline(true).Foreground(cAccent).Render(sm[2])
		}
		return lipgloss.NewStyle().Underline(true).Foreground(cAccent).Render(sm[1]) + styleDim.Render(" ("+sm[2]+")")
	})
	s = mdStrong.ReplaceAllStringFunc(s, func(m string) string {
		sm := mdStrong.FindStringSubmatch(m)
		if sm[1] != sm[3] {
			return m
		}
		return styleBold.Render(sm[2])
	})
	return mdEm.ReplaceAllStringFunc(s, func(m string) string {
		sm := mdEm.FindStringSubmatch(m)
		return sm[1] + lipgloss.NewStyle().Italic(true).Render(sm[2]) + sm[3]
	})
}

// renderMarkdown turns a chat answer into styled terminal lines wrapped to w.
// Code blocks keep their lines as they are (fit cuts them at the edge).
func renderMarkdown(text string, w int) []string {
	var out []string
	wrap := func(s, first, rest string) {
		ls := strings.Split(lipgloss.NewStyle().Width(max(10, w-lipgloss.Width(first))).Render(s), "\n")
		for i, l := range ls {
			if i == 0 {
				out = append(out, first+l)
			} else {
				out = append(out, rest+l)
			}
		}
	}
	fence := ""
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if fence != "" {
			if t := strings.TrimSpace(line); strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
				fence = ""
				continue
			}
			out = append(out, styleDim.Render("│ ")+styleMdCode.Render(strings.ReplaceAll(line, "\t", "    ")))
			continue
		}
		if m := mdFence.FindStringSubmatch(line); m != nil {
			fence = m[1]
			continue
		}
		if m := mdHeading.FindStringSubmatch(line); m != nil {
			wrap(styleMdH.Render(mdInline(m[2])), "", "")
			continue
		}
		if mdRule.MatchString(line) {
			out = append(out, styleDim.Render(strings.Repeat("─", max(10, min(w, 40)))))
			continue
		}
		if m := mdQuote.FindStringSubmatch(line); m != nil {
			wrap(styleDim.Render(mdInline(m[1])), styleDim.Render("│ "), styleDim.Render("│ "))
			continue
		}
		if m := mdList.FindStringSubmatch(line); m != nil {
			bullet := m[2]
			if !strings.ContainsAny(bullet, "0123456789") {
				bullet = "•"
			}
			switch strings.TrimSpace(m[3]) {
			case "[ ]":
				bullet += " ☐"
			case "[x]", "[X]":
				bullet += " " + styleOK.Render("☑")
			}
			pad := strings.Repeat(" ", len(m[1]))
			first := pad + styleAccent.Render(bullet) + " "
			wrap(mdInline(m[4]), first, strings.Repeat(" ", lipgloss.Width(first)))
			continue
		}
		wrap(mdInline(line), "", "")
	}
	return out
}
