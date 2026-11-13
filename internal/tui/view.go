package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/klahr/dash/internal/model"
)

// ANSI palette indices, so the terminal's own theme decides the exact colours.
var (
	cAccent  = lipgloss.Color("4")
	cAccent2 = lipgloss.Color("5")
	cOK      = lipgloss.Color("2")
	cWarn    = lipgloss.Color("3")
	cErr     = lipgloss.Color("1")
	cDim     = lipgloss.Color("8")

	styleDim    = lipgloss.NewStyle().Foreground(cDim)
	styleOK     = lipgloss.NewStyle().Foreground(cOK)
	styleErr    = lipgloss.NewStyle().Foreground(cErr)
	styleWarn   = lipgloss.NewStyle().Foreground(cWarn)
	styleAccent = lipgloss.NewStyle().Foreground(cAccent)
	styleBold   = lipgloss.NewStyle().Bold(true)
	styleTabOn  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(cAccent).Padding(0, 1)
	styleTabOff = lipgloss.NewStyle().Foreground(cDim).Padding(0, 1)
	styleSel    = lipgloss.NewStyle().Reverse(true)
	styleBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(0, 1)
	styleModeN  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(cAccent).Padding(0, 1)
	styleModeI  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(cOK).Padding(0, 1)
	// goalColors are the colours goal tags take, one per goal (goalStyle).
	goalColors = []lipgloss.Style{
		lipgloss.NewStyle().Foreground(lipgloss.Color("5")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("13")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("12")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
	}
	statusColors = map[model.TodoStatus]lipgloss.Style{
		model.StatusInProgress: styleWarn,
		model.StatusReview:     styleAccent,
		model.StatusMerged:     styleOK,
		model.StatusFailed:     styleErr,
	}
)

func (a *App) bodyHeight() int { return max(1, a.h-2) }

func (a *App) View() tea.View {
	if a.w == 0 {
		return tea.NewView("")
	}
	body := a.renderBody()
	body = fit(body, a.w, a.bodyHeight())
	if a.mode == modeCommand {
		if wm := a.wildmenu(a.w); wm != "" {
			lines := strings.Split(body, "\n")
			lines[len(lines)-1] = wm // like vim, the completions cover the line above the command
			body = strings.Join(lines, "\n")
		}
	}
	v := tea.NewView(a.renderHeader() + "\n" + body + "\n" + a.renderStatus())
	v.AltScreen = true
	return v
}

// fit pads or crops s to exactly w×h cells.
func fit(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i, l := range lines {
		if lipgloss.Width(l) > w {
			l = ansi.Truncate(l, w, "")
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}

func (a *App) renderHeader() string {
	var b strings.Builder
	b.WriteString(styleBold.Render(" Dash "))
	for i := tab(0); i < numTabs; i++ {
		if i == a.tab && a.overlay != ovDetail {
			b.WriteString(styleTabOn.Render(tabNames[i]))
		} else {
			b.WriteString(styleTabOff.Render(tabNames[i]))
		}
	}
	right := ""
	if a.query != "" {
		right += styleWarn.Render("/"+a.query) + styleDim.Render(" (:noh)  ")
	}
	if a.goal != "" {
		right += goalStyle(a.goal).Render("◆ "+ansi.Truncate(a.goalName(a.goal), 24, "…")) + styleDim.Render(" (:goal)  ")
	}
	if a.proj != nil {
		where := a.proj.Base()
		if a.multi() {
			where += " @" + a.proj.Machine
		}
		right += styleAccent.Render(a.proj.Name) + styleDim.Render(" ("+where+")")
	}
	if n := len(a.perms); n > 0 {
		right += styleWarn.Bold(true).Render(fmt.Sprintf("  ⚠ %d waiting (P)", n))
	}
	if c := a.ci; c != nil {
		switch c.State {
		case "passing":
			right += styleOK.Render("  CI ✓")
		case "failing":
			right += styleErr.Bold(true).Render("  CI ✗")
		case "running":
			right += styleWarn.Render("  CI …")
		}
	}
	if u := a.usage; u != nil && (u.Known || u.ManualPause) {
		switch {
		case u.Paused:
			right += styleErr.Render("  ⏸ " + u.Reason)
		default:
			top, name := 0.0, ""
			for _, w := range u.Windows {
				if w.ResetsAt.After(time.Now()) && w.Utilization >= top {
					top, name = w.Utilization, map[string]string{"five_hour": "5h", "seven_day": "7d"}[w.Name]
				}
			}
			if name != "" {
				st := styleDim
				if top >= 0.7 {
					st = styleWarn
				}
				right += st.Render(fmt.Sprintf("  %s %.0f%%", name, top*100))
			}
		}
	}
	right += styleDim.Render(fmt.Sprintf("  merge:%d ", a.mergeQueue))
	if a.connected {
		right += styleOK.Render("●")
	} else {
		right += styleErr.Render("●")
	}
	right += " "
	gap := a.w - lipgloss.Width(b.String()) - lipgloss.Width(right)
	return b.String() + strings.Repeat(" ", max(1, gap)) + right
}

func (a *App) renderStatus() string {
	var left string
	switch a.mode {
	case modeCommand:
		return a.cmdline.View()
	case modePrompt:
		return a.promptIn.View()
	case modeConfirm:
		return styleWarn.Render(a.question)
	case modeInsert:
		left = styleModeI.Render("INSERT")
	default:
		left = styleModeN.Render("NORMAL")
	}
	msg := ""
	if a.flash != "" && time.Since(a.flashAt) < 6*time.Second {
		if a.flashErr {
			msg = styleErr.Render(a.flash)
		} else {
			msg = a.flash
		}
	} else {
		msg = styleDim.Render(a.hints())
	}
	pend := ""
	if a.count > 0 {
		pend = fmt.Sprint(a.count)
	}
	pend += a.pending
	right := styleDim.Render(pend + "  ? help ")
	gap := a.w - lipgloss.Width(left) - lipgloss.Width(msg) - lipgloss.Width(right) - 1
	if gap < 1 {
		msg = ansi.Truncate(msg, max(0, a.w-lipgloss.Width(left)-lipgloss.Width(right)-2), "…")
		gap = 1
	}
	return left + " " + msg + strings.Repeat(" ", gap) + right
}

func (a *App) hints() string {
	if a.mode == modeInsert {
		if a.overlay == ovForm {
			return "Tab switch field · Esc normal mode, then :w draft / :wq queue / :q! discard"
		}
		return "Enter send · Alt+Enter newline · Esc normal"
	}
	switch a.overlay {
	case ovDetail:
		return "Tab log/diff/tests · j/k ^d/^u gg/G · s queue a approve r rework x cancel z pause t try ! shell · q back"
	case ovForm:
		return "i insert · j/k field · +/- priority · _ clear · :w draft · :wq queue · :q! discard"
	case ovPicker:
		return "j/k · Enter select · q close"
	case ovPerms:
		return "y allow · A always allow this tool · n deny · o open task · j/k · q close"
	}
	switch a.tab {
	case tabBoard:
		return "hjkl move · Enter open · o new · s queue · a approve · r rework · / search · gf goal filter · T retest · gt next tab"
	case tabChat:
		return "i type · j/k scroll · t make last answer a task · x stop · :clear"
	case tabProcs:
		return "Enter attach · R run · T test · D deploy · ! shell · x stop · o open URL"
	}
	return "j/k scroll · G bottom"
}

func (a *App) renderBody() string {
	switch a.overlay {
	case ovDetail:
		return a.renderDetail()
	case ovForm:
		return a.center(a.renderForm())
	case ovPicker:
		return a.center(a.renderPicker())
	case ovHelp:
		return a.center(helpText)
	case ovPerms:
		return a.center(a.renderPerms())
	}
	if a.proj == nil && a.tab != tabChat {
		return a.center(styleBox.Render("No project yet.\n\n" + styleDim.Render(":newproject <name> [path]")))
	}
	switch a.tab {
	case tabBoard:
		return a.renderBoard()
	case tabChat:
		return a.renderChat()
	case tabProcs:
		return a.renderProcs()
	}
	return ""
}

func (a *App) center(s string) string {
	return lipgloss.Place(a.w, a.bodyHeight(), lipgloss.Center, lipgloss.Center, s)
}

// ---- board

func (a *App) renderBoard() string {
	if len(a.cols) == 0 {
		return a.center(styleDim.Render("No tasks. Press o to create one."))
	}
	n := len(columns)
	cw := max(12, a.w/n)
	h := a.bodyHeight()
	cardsFit := max(1, (h-2)/3)

	var rendered []string
	for ci, c := range columns {
		items := a.cols[ci]
		head := fmt.Sprintf("%s %d", strings.ToUpper(c.title), len(items))
		if ci == a.col {
			head = styleAccent.Bold(true).Render(head)
		} else {
			head = styleDim.Render(head)
		}
		lines := []string{" " + head, " " + styleDim.Render(strings.Repeat("─", cw-3))}

		start := 0
		if ci == a.col && a.row >= cardsFit {
			start = a.row - cardsFit + 1
		}
		for ri := start; ri < len(items) && ri < start+cardsFit; ri++ {
			lines = append(lines, a.renderCard(items[ri], cw-2, ci == a.col && ri == a.row)...)
		}
		if rest := len(items) - start - cardsFit; rest > 0 {
			lines = append(lines, styleDim.Render(fmt.Sprintf("  +%d more", rest)))
		}
		if c.title == "Done" && !a.showOld {
			if old := a.hiddenOld(); old > 0 {
				lines = append(lines, styleDim.Render(fmt.Sprintf("  %d older (:old)", old)))
			}
		}
		col := lipgloss.NewStyle().Width(cw).Render(strings.Join(lines, "\n"))
		rendered = append(rendered, col)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
}

func (a *App) renderCard(t model.Todo, w int, sel bool) []string {
	name := ansi.Truncate(title(t), w-3, "…")
	sub := ""
	depText, depBad := a.depWait(t)
	switch {
	case a.waiting(t.ID):
		sub = "⚠ waiting for your approval (P)"
	case depText != "":
		sub = depText
	case t.Paused:
		sub = "⏸ paused (z resumes)"
	case t.Checks != nil && !t.Checks.Passed && t.Status == model.StatusReview:
		sub = "✗ tests fail"
	case t.Status == model.StatusInProgress:
		sub = a.lastLine[t.ID]
	case t.Error != "":
		sub = t.Error
	case t.Status == model.StatusFailed:
		sub = "failed"
	case t.Status == model.StatusMerged && t.Summary != "":
		sub = t.Summary
	default:
		sub = strings.ReplaceAll(t.Prompt, "\n", " ")
	}
	var meta []string
	if t.DiffFiles > 0 && (t.Status == model.StatusReview || t.Status == model.StatusMerged || t.Status == model.StatusFailed || t.Status == model.StatusMerging) {
		meta = append(meta, fmt.Sprintf("+%d −%d", t.DiffAdd, t.DiffDel))
	}
	if t.Status == model.StatusInProgress && t.StartedAt != nil {
		meta = append(meta, ago(*t.StartedAt))
	}
	if t.PR != nil && t.Status != model.StatusMerged {
		meta = append(meta, fmt.Sprintf("PR #%d", t.PR.Number))
	}
	if t.Revert != nil {
		meta = append(meta, "reverted")
	}
	if len(t.Attachments) > 0 {
		meta = append(meta, fmt.Sprintf("%d attached", len(t.Attachments)))
	}
	if t.Status == model.StatusReview && t.FinishedAt != nil {
		meta = append(meta, "waiting "+ago(*t.FinishedAt))
	}
	if len(meta) > 0 && !a.waiting(t.ID) {
		sub = strings.Join(meta, " · ") + " · " + sub
	}
	tag := ""
	if t.GoalID != "" {
		tag = "◆ " + ansi.Truncate(a.goalName(t.GoalID), max(4, (w-4)/2), "…")
	}
	sub = ansi.Truncate(sub, max(0, w-2-lipgloss.Width(tag)-1), "…")
	st, ok := statusColors[t.Status]
	if !ok {
		st = styleDim
	}
	l1 := "   " + name
	if a.waiting(t.ID) || depBad {
		st = styleWarn
	} else if depText != "" {
		st = styleDim
	} else if t.Checks != nil && !t.Checks.Passed && t.Status == model.StatusReview {
		st = styleErr
	}
	l2 := "   " + st.Render(ansi.Strip(sub))
	if tag != "" {
		l2 = "   " + goalStyle(t.GoalID).Render(tag) + " " + st.Render(ansi.Strip(sub))
	}
	if sel {
		l1 = styleSel.Render("   " + name + strings.Repeat(" ", max(0, w-3-lipgloss.Width(name))))
	}
	return []string{l1, ansi.Truncate(l2, w+1, ""), ""}
}

// depWait describes why a queued todo waits for its dependencies (or the
// task it builds on): "⏳
// waiting for <titles>", or "blocked: <title> failed" when a dependency
// failed or went back to draft (bad). Empty when nothing holds it back.
func (a *App) depWait(t model.Todo) (text string, bad bool) {
	if t.Status != model.StatusQueued {
		return "", false
	}
	bl := t.BlockersIn(a.todos)
	if len(bl) == 0 {
		return "", false
	}
	for _, d := range bl {
		if d.Status == model.StatusFailed || d.Status == model.StatusDraft {
			return fmt.Sprintf("⚠ blocked: %s %s", title(d), d.Status), true
		}
	}
	names := title(bl[0])
	if len(bl) == 2 {
		names += ", " + title(bl[1])
	} else if len(bl) > 2 {
		names += fmt.Sprintf(" +%d", len(bl)-1)
	}
	return "⏳ waiting for " + names, false
}

// ---- detail

func (a *App) renderDetail() string {
	t, ok := a.todos[a.dID]
	if !ok {
		return a.center("task not found")
	}
	st, okc := statusColors[t.Status]
	if !okc {
		st = styleDim
	}
	head := styleBold.Render(title(t)) + "  " + st.Render(string(t.Status))
	var meta []string
	if t.GoalID != "" {
		meta = append(meta, goalStyle(t.GoalID).Render("◆ goal "+a.goalName(t.GoalID)))
	}
	if t.Priority != nil {
		meta = append(meta, fmt.Sprintf("priority %d", *t.Priority))
	}
	if t.Worktree != nil {
		meta = append(meta, t.Worktree.Branch)
	}
	if t.Runs > 0 {
		meta = append(meta, fmt.Sprintf("%d runs ≈$%.2f", t.Runs, t.CostUSD))
	}
	if t.AutoFixes > 0 {
		meta = append(meta, fmt.Sprintf("auto-fixed %d×", t.AutoFixes))
	}
	if t.PR != nil {
		meta = append(meta, fmt.Sprintf("PR #%d %s (gx copies the link)", t.PR.Number, strings.ToLower(t.PR.State)))
	}
	if t.MergedCommit != "" {
		meta = append(meta, "commit "+t.MergedCommit[:min(8, len(t.MergedCommit))])
	}
	if t.Revert != nil {
		meta = append(meta, "reverted")
	}
	for _, at := range t.Attachments {
		meta = append(meta, "📎 "+at.Path)
	}
	if t.Checks != nil {
		if t.Checks.Passed {
			meta = append(meta, styleOK.Render("✓ tests pass"))
		} else {
			meta = append(meta, styleErr.Render("✗ tests fail"))
		}
	}
	if a.waiting(t.ID) {
		meta = append(meta, styleWarn.Render("⚠ waiting for approval (P)"))
	}
	if txt, bad := a.depWait(t); txt != "" {
		if bad {
			meta = append(meta, styleWarn.Render(txt))
		} else {
			meta = append(meta, styleDim.Render(txt))
		}
	} else if len(t.DependsOn) > 0 {
		var names []string
		for _, id := range t.DependsOn {
			if d, ok := a.todos[id]; ok {
				names = append(names, title(d))
			}
		}
		if len(names) > 0 {
			meta = append(meta, "depends on "+strings.Join(names, ", "))
		}
	}
	if t.Error != "" {
		meta = append(meta, styleErr.Render(t.Error))
	}
	tabs := ""
	for _, k := range []string{"log", "diff", "tests"} {
		if k == a.dKind {
			tabs += styleTabOn.Render(k)
		} else {
			tabs += styleTabOff.Render(k)
		}
	}
	prompt := ansi.Truncate(strings.ReplaceAll(t.Prompt, "\n", " ⏎ "), a.w-2, "…")
	top := []string{" " + head, " " + styleDim.Render(strings.Join(meta, " · ")), " " + styleDim.Render(prompt), " " + tabs}

	text := a.dText[a.dID+a.dKind]
	if a.dKind == "tests" {
		text = ""
		if c := t.Checks; c != nil {
			state := styleOK.Render("passed")
			if !c.Passed {
				state = styleErr.Render(fmt.Sprintf("failed (exit %d)", c.ExitCode))
			}
			text = fmt.Sprintf("%s  %s · %.0fs\n$ %s\n\n%s", state, map[string]string{"merge": "after merging the default branch", "review": "before review"}[c.Stage], c.Duration, c.Command, c.Output)
		} else {
			text = styleDim.Render("No test run yet. Set a test command with the web UI's project settings.")
		}
	}
	var lines []string
	if text == "" {
		lines = []string{styleDim.Render("  (empty)")}
	} else if a.dKind == "diff" {
		for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			lines = append(lines, colorDiff(l))
		}
	} else {
		wrap := lipgloss.NewStyle().Width(a.w - 1)
		for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			lines = append(lines, strings.Split(wrap.Render(l), "\n")...)
		}
	}
	pane := a.renderPane(lines, &a.dOff)
	return strings.Join(top, "\n") + "\n" + fit(pane, a.w, a.bodyHeight()-len(top))
}

func colorDiff(l string) string {
	switch {
	case strings.HasPrefix(l, "diff --git"):
		return styleWarn.Bold(true).Render(l)
	case strings.HasPrefix(l, "@@"):
		return styleAccent.Render(l)
	case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
		return styleBold.Render(l)
	case strings.HasPrefix(l, "+"):
		return styleOK.Render(l)
	case strings.HasPrefix(l, "-"):
		return styleErr.Render(l)
	}
	return l
}

// renderPane shows the window of lines starting at *off, clamping *off.
func (a *App) renderPane(lines []string, off *int) string {
	h := a.bodyHeight()
	if a.overlay == ovDetail {
		h -= 4
	}
	*off = clamp(*off, 0, max(0, len(lines)-h))
	return strings.Join(lines[*off:min(len(lines), *off+h)], "\n")
}

// ---- chat

func (a *App) renderChat() string {
	inputH := 5
	h := a.bodyHeight() - inputH
	wrapW := min(a.w-4, 110)
	var lines []string
	if len(a.chat) == 0 {
		lines = append(lines, "", styleDim.Render("  Ask Claude anything. Press i to start typing."))
	}
	for _, e := range a.chat {
		switch e.role {
		case "user":
			lines = append(lines, "", styleAccent.Bold(true).Render("  you"))
			lines = append(lines, indent(lipgloss.NewStyle().Width(wrapW).Render(e.text), "  ")...)
		case "assistant":
			lines = append(lines, "", lipgloss.NewStyle().Foreground(cAccent2).Bold(true).Render("  claude"))
			if e.thinking != "" {
				th := lipgloss.NewStyle().Width(wrapW - 2).Foreground(cDim).Italic(true).Render(e.thinking)
				lines = append(lines, indent(th, "  │ ")...)
			}
			if e.text == "" && a.chatCancel != nil {
				lines = append(lines, "  "+styleDim.Render("…"))
			} else {
				lines = append(lines, indent(strings.Join(renderMarkdown(e.text, wrapW), "\n"), "  ")...)
			}
			if u := usageText(e.usage); u != "" {
				lines = append(lines, "  "+styleDim.Render(u))
			}
		case "error":
			lines = append(lines, indent(styleErr.Width(wrapW).Render("⚠ "+e.text), "  ")...)
		}
	}
	// chatOff counts lines up from the bottom.
	maxOff := max(0, len(lines)-h)
	a.chatOff = clamp(a.chatOff, 0, maxOff)
	end := len(lines) - a.chatOff
	start := max(0, end-h)
	hist := fit(strings.Join(lines[start:end], "\n"), a.w, h)
	border := styleDim
	if a.mode == modeInsert {
		border = styleOK
	}
	input := border.Render(strings.Repeat("─", a.w)) + "\n" + a.chatInput.View()
	return hist + "\n" + input
}

// usageText shows what an answer took: "12k in · 845 out · thought 8s · 23s".
func usageText(u *model.ChatUsage) string {
	if u == nil {
		return ""
	}
	parts := []string{
		fmtTokens(u.InputTokens+u.CacheReadTokens+u.CacheCreationTokens) + " in",
		fmtTokens(u.OutputTokens) + " out",
	}
	if u.ThinkingMS >= 500 {
		parts = append(parts, "thought "+fmtMS(u.ThinkingMS))
	}
	if u.DurationMS > 0 {
		parts = append(parts, fmtMS(u.DurationMS))
	}
	return strings.Join(parts, " · ")
}

func fmtTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func fmtMS(ms int64) string {
	s := (ms + 500) / 1000
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm %ds", s/60, s%60)
}

// ago formats elapsed time compactly: "now", "4m", "3h", "2d".
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func indent(s, prefix string) []string {
	ls := strings.Split(s, "\n")
	for i := range ls {
		ls[i] = prefix + ls[i]
	}
	return ls
}

// ---- processes

func (a *App) renderProcs() string {
	if len(a.procs) == 0 {
		return a.center(styleDim.Render("No processes. R run server · T test · D deploy · ! shell"))
	}
	var lines []string
	lines = append(lines, styleDim.Render("    KIND  STATUS     COMMAND"), "")
	for i, p := range a.procs {
		state := styleOK.Render("running ")
		if !p.Running {
			code := "?"
			if p.ExitCode != nil {
				code = fmt.Sprint(*p.ExitCode)
			}
			state = styleDim.Render(fmt.Sprintf("exit %-3s", code))
		}
		line := fmt.Sprintf("  %-5s %s  %s", p.Kind, state, p.Command)
		if p.URL != "" {
			line += "  " + styleAccent.Underline(true).Render(p.URL)
		}
		if i == a.procRow {
			line = styleAccent.Render("▌") + line[1:]
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// ---- overlays

func (a *App) renderForm() string {
	head := "New task"
	if a.fEditID != "" {
		head = "Edit task"
	}
	label := func(s string, f int) string {
		if a.fField == f {
			return styleAccent.Bold(true).Render("▸ " + s)
		}
		return styleDim.Render("  " + s)
	}
	body := strings.Join([]string{
		styleBold.Render(head), "",
		label("Title", 0), a.fTitle.View(), "",
		label("Prompt", 1), a.fPrompt.View(), "",
		styleDim.Render(formPriority(a.fPrio)), "",
		styleDim.Render(":w save draft · :wq save & queue · :q! discard"),
	}, "\n")
	return styleBox.Width(min(a.w-4, 104)).Render(body)
}

func (a *App) renderPerms() string {
	lines := []string{styleBold.Render("Claude is waiting for you"), ""}
	w := min(a.w-10, 96)
	for i, p := range a.perms {
		name := p.TodoID
		if t, ok := a.todos[p.TodoID]; ok {
			name = title(t)
		}
		head := fmt.Sprintf("%s  %s", styleWarn.Render(p.Tool), styleDim.Render(name))
		body := ansi.Truncate(permSummary(p), w, "…")
		if i == a.permRow {
			head = styleAccent.Render("▸ ") + head
			body = "  " + styleSel.Render(" "+body+" ")
		} else {
			head = "  " + head
			body = "  " + body
		}
		lines = append(lines, head, body, "")
	}
	lines = append(lines, styleDim.Render("y allow · A always allow this tool in the project · n deny · o open task"))
	return styleBox.Render(strings.Join(lines, "\n"))
}

func (a *App) renderPicker() string {
	if len(a.projects) == 0 {
		return styleBox.Render("No projects.\n" + styleDim.Render(":newproject <name> [path]"))
	}
	lines := []string{styleBold.Render("Projects"), ""}
	for i, p := range a.projects {
		where := p.Path
		if a.multi() {
			where = p.Machine + "  " + p.Path
		}
		l := fmt.Sprintf(" %s  %s", p.Name, styleDim.Render(where))
		if i == a.pickRow {
			l = styleSel.Render(fmt.Sprintf(" %s ", p.Name)) + "  " + styleDim.Render(where)
		}
		lines = append(lines, l)
	}
	for _, m := range a.machines {
		if !m.OK {
			lines = append(lines, "", styleErr.Render(" "+m.Name+": "+m.Error))
		}
	}
	return styleBox.Render(strings.Join(lines, "\n"))
}

var helpText = styleBox.Render(strings.Join([]string{
	styleBold.Render("Dash — keys"),
	"",
	styleAccent.Render("Global") + "        gt/gT next/prev tab · p projects · : command · ? help · ctrl+l reload · q/ZZ/:q quit",
	styleAccent.Render("Motion") + "        h j k l · w/b next/prev column · gg G · {count}j · ctrl+d/u/f/b",
	styleAccent.Render("Task") + "          Enter open · o new · e edit · s queue · x cancel · z pause · a approve · r rework",
	"              dd delete · +/- (K/J) priority · _ clear priority · t try in worktree · ! shell in worktree",
	styleAccent.Render("More") + "          / search · n/N next/prev match · :noh clear · T rerun tests · Y duplicate · E open in editor · :copy [path|prompt]",
	"              gf show only the selected task's goal (again: every task) · :goal <name> filter by goal · :goal clear",
	"              u revert a merged task · gx copy its pull request link · :cleanup remove stale worktrees",
	"              :ci build status · :fix ask Claude to fix the failing build · Tab completes : commands",
	styleAccent.Render("Detail") + "        Tab/h/l cycle log → diff → tests · R reload · q back",
	styleAccent.Render("Approvals") + "     P list Claude's permission requests · y allow · A always allow · n deny",
	styleAccent.Render("Chat") + "          t make the last answer a task · i insert · Enter send · Alt+Enter newline · Esc normal · x stop · dd/:clear",
	styleAccent.Render("Procs") + "         Enter attach (Ctrl-] detaches) · R run · T test · D deploy · ! shell · x stop · o open",
	styleAccent.Render("Form") + "          Tab field · Esc normal · :w draft · :wq/ZZ queue · :q! discard",
	styleAccent.Render("Commands") + "      :project <name> · :newproject <name> [path] · :addclone <path> [name] · :task <prompt> · :new [prompt]",
	"              :run <cmd> · :push · :pause / :resume the queue · :perms · :board :chat :procs · :clear",
}, "\n"))

// formPriority is the task form's priority line.
func formPriority(p *int) string {
	if p == nil {
		return "no priority (+/- to set)"
	}
	return fmt.Sprintf("priority %d (+/- · _ clears)", *p)
}

// goalStyle is goal id's colour: the same for a goal every time, and
// different goals mostly different.
func goalStyle(id string) lipgloss.Style {
	var h uint32 = 2166136261 // FNV-1a
	for i := 0; i < len(id); i++ {
		h = (h ^ uint32(id[i])) * 16777619
	}
	return goalColors[h%uint32(len(goalColors))]
}
