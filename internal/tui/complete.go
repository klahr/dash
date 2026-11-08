package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// commands offered by Tab completion on the : line, most used first. A
// trailing space means the command takes an argument.
var commands = []string{
	"project ", "push", "task ", "todo ", "new ", "board", "chat", "procs",
	"ci", "fix", "cleanup", "pause", "resume", "perms", "copy path", "copy prompt", "copy",
	"run ", "newproject ", "addclone ", "noh", "old", "clear", "refresh", "help", "q",
}

// commandSuggestions are whole command lines for the : prompt: every
// command, plus "project <name>" for each project.
func (a *App) commandSuggestions() []string {
	out := append([]string(nil), commands...)
	for _, p := range a.projects {
		if a.multi() {
			out = append(out, "project "+p.Name+"@"+p.Machine)
		} else {
			out = append(out, "project "+p.Name)
		}
	}
	return out
}

// wildmenu lists the completions for what's typed so far, the one Tab
// accepts highlighted, like vim's wildmenu.
func (a *App) wildmenu(width int) string {
	if strings.TrimSpace(a.cmdline.Value()) == "" {
		return ""
	}
	matches := a.cmdline.MatchedSuggestions()
	if len(matches) == 0 || (len(matches) == 1 && matches[0] == a.cmdline.Value()) {
		return ""
	}
	cur := a.cmdline.CurrentSuggestionIndex()
	var parts []string
	for i, m := range matches {
		m = strings.TrimSpace(m)
		if i == cur {
			parts = append(parts, styleSel.Render(" "+m+" "))
		} else {
			parts = append(parts, styleDim.Render(" "+m+" "))
		}
	}
	line := strings.Join(parts, "")
	hint := styleDim.Render("  Tab ⏎ · ↑↓")
	return ansi.Truncate(line, max(0, width-lipgloss.Width(hint)), "…") + hint
}
