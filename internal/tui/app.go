package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/klahr/dash/internal/agent"
	"github.com/klahr/dash/internal/model"
)

type tab int

const (
	tabBoard tab = iota
	tabChat
	tabProcs
	tabActivity
	numTabs
)

var tabNames = [numTabs]string{"Board", "Chat", "Procs", "Activity"}

type mode int

const (
	modeNormal mode = iota
	modeInsert
	modeCommand
	modePrompt  // single-line input at the bottom (rework feedback)
	modeConfirm // y/n at the bottom
)

type overlay int

const (
	ovNone overlay = iota
	ovDetail
	ovForm
	ovPicker
	ovHelp
	ovPerms
)

// Board columns; failed todos are shown with merged under "Done".
var columns = []struct {
	title    string
	statuses []model.TodoStatus
}{
	{"Draft", []model.TodoStatus{model.StatusDraft}},
	{"Queued", []model.TodoStatus{model.StatusQueued}},
	{"Running", []model.TodoStatus{model.StatusInProgress}},
	{"Review", []model.TodoStatus{model.StatusReview}},
	{"Merging", []model.TodoStatus{model.StatusMerging}},
	{"Done", []model.TodoStatus{model.StatusMerged, model.StatusFailed}},
}

type chatEntry struct {
	role     string // user | assistant | error
	text     string
	thinking string
}

// App is the Bubble Tea model. It uses pointer receivers; goroutines reach it
// only through program.Send.
type App struct {
	c       *Client
	prog    *tea.Program
	ctx     context.Context
	stop    context.CancelFunc
	startPj string // -project flag

	w, h    int
	tab     tab
	mode    mode
	overlay overlay

	projects []model.Project
	proj     *model.Project
	todos    map[string]model.Todo
	cols     [][]model.Todo
	col, row int
	lastLine map[string]string

	// detail view
	dID     string
	dKind   string // log | diff
	dText   map[string]string
	dOff    int
	dFollow bool

	procs   []model.Process
	procRow int

	activity []string
	actOff   int

	chat       []chatEntry
	chatHist   []agent.ChatMessage
	chatOff    int // lines scrolled up from the bottom
	chatCancel context.CancelFunc
	chatInput  textarea.Model

	// new/edit todo form
	fTitle  textinput.Model
	fPrompt textarea.Model
	fGoal   bool
	fPrio   int
	fField  int    // 0 title, 1 prompt
	fEditID string // "" = new todo

	pickRow int

	query   string // board search (/)
	showOld bool   // show merged todos older than a day
	Bell    bool   // ring the terminal bell for reviews, failures and approvals
	ci      *CIStatus

	perms   []PermRequest // pending permission requests for this project
	permRow int
	usage   *Usage

	cmdline   textinput.Model
	promptIn  textinput.Model
	onPrompt  func(string) tea.Cmd
	onConfirm func() tea.Cmd
	question  string

	pending string // multi-key sequences: g, d, Z
	count   int

	flash         string
	flashErr      bool
	flashAt       time.Time
	connected     bool            // live link to the selected project's computer
	links         map[string]bool // computer name → live link
	streaming     map[string]bool // computers we follow events from
	machines      []Machine
	seenElsewhere map[string]model.TodoStatus
	mergeQueue    int
}

func NewApp(c *Client, project string) *App {
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{
		c: c, ctx: ctx, stop: cancel, startPj: project, seenElsewhere: map[string]model.TodoStatus{},
		links: map[string]bool{}, streaming: map[string]bool{},
		todos: map[string]model.Todo{}, lastLine: map[string]string{}, dText: map[string]string{},
		dKind: "log",
	}

	a.cmdline = textinput.New()
	a.cmdline.Prompt = ":"
	a.cmdline.ShowSuggestions = true // Tab completes, Ctrl-n/Ctrl-p (↑/↓) pick another
	a.promptIn = textinput.New()

	a.chatInput = textarea.New()
	a.chatInput.Placeholder = "Ask Claude… (i to type, Enter to send, Alt+Enter for newline, Esc for normal mode)"
	a.chatInput.ShowLineNumbers = false
	a.chatInput.SetHeight(3)
	a.chatInput.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))

	a.fTitle = textinput.New()
	a.fTitle.Placeholder = "Title"
	a.fPrompt = textarea.New()
	a.fPrompt.Placeholder = "What should Claude do?"
	a.fPrompt.ShowLineNumbers = false
	a.fPrompt.SetHeight(10)
	return a
}

// SetProgram must be called before Run so background goroutines can deliver messages.
func (a *App) SetProgram(p *tea.Program) { a.prog = p }

// ---------------------------------------------------------------- messages

type (
	projectsMsg struct {
		ps  []model.Project
		ms  []Machine
		err error
	}
	todosMsg struct {
		pid string
		ts  []model.Todo
		err error
	}
	procsMsg struct {
		pid string
		ps  []model.Process
		err error
	}
	textMsg struct {
		id, kind, text string
		err            error
	}
	eventMsg wireEvent
	connMsg  struct {
		machine string
		ok      bool
		err     error
	}
	chatDeltaMsg agent.ChatDelta
	chatEndMsg   struct{ err error }
	chatLoadMsg  struct {
		ms  []agent.ChatMessage
		err error
	}
	resultMsg struct { // outcome of a one-shot action
		ok     string
		err    error
		reload bool
	}
	procStartedMsg struct {
		p   model.Process
		err error
	}
	attachEndMsg struct{ err error }
	permsMsg     struct {
		pid string
		ps  []PermRequest
	}
	usageMsg struct {
		u   Usage
		err error
	}
)

func (a *App) Init() tea.Cmd {
	return tea.Batch(a.loadProjects(), a.loadUsage(), a.loadChat(), tick())
}

func (a *App) loadChat() tea.Cmd {
	return func() tea.Msg { ms, err := a.c.Chat(); return chatLoadMsg{ms, err} }
}

// streamEvents follows every reachable computer's events (once each), so
// reviews and approvals elsewhere still ring the bell.
func (a *App) streamEvents() {
	if a.streaming == nil {
		a.streaming, a.links = map[string]bool{}, map[string]bool{}
	}
	for _, m := range a.machines {
		if !m.OK || a.streaming[m.Name] {
			continue
		}
		a.streaming[m.Name] = true
		go a.c.StreamEvents(a.ctx, m,
			func(ev wireEvent) { a.prog.Send(eventMsg(ev)) },
			func(ok bool, err error) { a.prog.Send(connMsg{m.Name, ok, err}) })
	}
}

func (a *App) loadProjects() tea.Cmd {
	return func() tea.Msg { ps, ms, err := a.c.AllProjects(); return projectsMsg{ps, ms, err} }
}

// machine is the selected project's computer.
func (a *App) machine() string {
	if a.proj != nil {
		return a.proj.Machine
	}
	if len(a.machines) > 0 {
		return a.machines[0].Name
	}
	return ""
}

func (a *App) multi() bool { return len(a.machines) > 1 }

func (a *App) prefixOf(machine string) string {
	for _, m := range a.machines {
		if m.Name == machine {
			return m.Prefix
		}
	}
	return ""
}

func (a *App) loadTodos() tea.Cmd {
	if a.proj == nil {
		return nil
	}
	pid := a.proj.ID
	return func() tea.Msg { ts, err := a.c.Todos(pid); return todosMsg{pid, ts, err} }
}

func (a *App) loadProcs() tea.Cmd {
	if a.proj == nil {
		return nil
	}
	pid := a.proj.ID
	return func() tea.Msg { ps, err := a.c.Processes(pid); return procsMsg{pid, ps, err} }
}

type tickMsg struct{}

// tick re-renders every 30s so elapsed times ("4m") stay current.
func tick() tea.Cmd { return tea.Tick(30*time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }

// bell rings the terminal bell on stderr (the same terminal) without
// disturbing the rendered screen.
func (a *App) bell() {
	if a.Bell {
		_, _ = os.Stderr.WriteString("\a")
	}
}

func (a *App) loadPerms() tea.Cmd {
	if a.proj == nil {
		return nil
	}
	pid := a.proj.ID
	return func() tea.Msg { ps, _ := a.c.Permissions(pid); return permsMsg{pid, ps} }
}

type ciMsg struct {
	pid    string
	st     CIStatus
	err    error
	report bool // show a summary (the :ci command)
}

func (a *App) loadCI(refresh, report bool) tea.Cmd {
	if a.proj == nil || a.proj.RemoteURL == "" {
		return nil
	}
	pid := a.proj.ID
	return func() tea.Msg { st, err := a.c.CI(pid, refresh); return ciMsg{pid, st, err, report} }
}

// ciSummary is a one-line build report, e.g. "build failing at 1a2b3c4d: CI ✗ · Lint ✓".
func ciSummary(st CIStatus) string {
	if st.Error != "" {
		return "builds: " + st.Error
	}
	if st.State == "none" {
		return "no GitHub Actions runs on " + st.Branch + " yet"
	}
	var parts []string
	seen := map[string]bool{}
	for _, r := range st.Runs {
		if r.SHA != st.SHA || seen[r.Workflow] {
			continue
		}
		seen[r.Workflow] = true
		mark := "✓"
		switch {
		case r.Status != "completed":
			mark = "…"
		case r.Conclusion == "failure" || r.Conclusion == "timed_out" || r.Conclusion == "startup_failure":
			mark = "✗"
		case r.Conclusion != "success":
			mark = "–"
		}
		parts = append(parts, r.Workflow+" "+mark)
	}
	s := fmt.Sprintf("build %s at %s: %s", st.State, short(st.SHA), strings.Join(parts, " · "))
	if st.State == "failing" && st.TodoID == "" {
		s += "  (:fix asks Claude to fix it)"
	}
	return s
}

func short(sha string) string { return sha[:min(8, len(sha))] }

func (a *App) loadUsage() tea.Cmd {
	return func() tea.Msg { u, err := a.c.Usage(); return usageMsg{u, err} }
}

func (a *App) waiting(todoID string) bool {
	for _, p := range a.perms {
		if p.TodoID == todoID {
			return true
		}
	}
	return false
}

func (a *App) loadText(id, kind string) tea.Cmd {
	return func() tea.Msg { s, err := a.c.TodoText(id, kind); return textMsg{id, kind, s, err} }
}

func (a *App) action(ok string, fn func() error) tea.Cmd {
	return func() tea.Msg { return resultMsg{ok: ok, err: fn()} }
}

func (a *App) setFlash(s string, isErr bool) {
	a.flash, a.flashErr, a.flashAt = s, isErr, time.Now()
}

// ---------------------------------------------------------------- update

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
		a.chatInput.SetWidth(a.w - 2)
		a.fTitle.SetWidth(min(a.w-8, 100))
		a.fPrompt.SetWidth(min(a.w-8, 100))
		a.fPrompt.SetHeight(max(3, min(12, a.h-14)))
		return a, nil

	case tea.KeyPressMsg:
		return a, a.handleKey(msg)

	case tea.PasteMsg:
		return a, a.forwardInput(msg)

	case projectsMsg:
		if msg.err != nil {
			a.setFlash(msg.err.Error(), true)
			return a, nil
		}
		a.projects, a.machines = msg.ps, msg.ms
		if a.prog != nil {
			a.streamEvents()
		}
		return a, a.reselectProject()

	case todosMsg:
		if msg.err != nil {
			a.setFlash(msg.err.Error(), true)
		} else if a.proj != nil && msg.pid == a.proj.ID {
			a.todos = map[string]model.Todo{}
			for _, t := range msg.ts {
				a.todos[t.ID] = t
			}
			a.rebuildCols()
		}
		return a, nil

	case procsMsg:
		if msg.err == nil && a.proj != nil && msg.pid == a.proj.ID {
			a.procs = msg.ps
			a.procRow = clamp(a.procRow, 0, len(a.procs)-1)
		}
		return a, nil

	case textMsg:
		if msg.err != nil {
			a.dText[msg.id+msg.kind] = "error: " + msg.err.Error()
		} else {
			a.dText[msg.id+msg.kind] = msg.text
		}
		if msg.id == a.dID && msg.kind == a.dKind && a.dFollow {
			a.dOff = 1 << 30 // clamped at render time → bottom
		}
		return a, nil

	case eventMsg:
		return a, a.handleEvent(wireEvent(msg))

	case connMsg:
		was := a.links[msg.machine]
		a.links[msg.machine] = msg.ok
		a.connected = a.links[a.machine()]
		if msg.ok && !was && msg.machine == a.machine() {
			return a, tea.Batch(a.loadProjects(), a.loadTodos(), a.loadProcs(), a.loadPerms(), a.loadUsage())
		}
		if msg.err != nil && errors.Is(msg.err, ErrUnauthorized) {
			a.setFlash("login rejected by server — check DASH_USERNAME / DASH_PASSWORD", true)
		}
		return a, nil

	case chatDeltaMsg:
		if len(a.chat) == 0 {
			return a, nil
		}
		last := &a.chat[len(a.chat)-1]
		switch msg.Kind {
		case "text":
			last.text += msg.Text
		case "thinking":
			last.thinking += msg.Text
		case "error":
			a.chat = append(a.chat, chatEntry{role: "error", text: msg.Text})
		}
		return a, nil

	case chatLoadMsg:
		if msg.err != nil {
			a.setFlash("couldn't load chat history: "+msg.err.Error(), true)
			return a, nil
		}
		if len(a.chatHist) > 0 || a.chatCancel != nil {
			return a, nil
		}
		a.chatHist = msg.ms
		for _, m := range msg.ms {
			a.chat = append(a.chat, chatEntry{role: m.Role, text: m.Content})
		}
		return a, nil

	case chatEndMsg:
		a.chatCancel = nil
		if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
			a.chat = append(a.chat, chatEntry{role: "error", text: msg.err.Error()})
		}
		// Record the assistant turn so the next question has context.
		for i := len(a.chat) - 1; i >= 0; i-- {
			if a.chat[i].role == "assistant" {
				if a.chat[i].text != "" {
					a.chatHist = append(a.chatHist, agent.ChatMessage{Role: "assistant", Content: a.chat[i].text})
				} else {
					a.chatHist = a.chatHist[:len(a.chatHist)-1] // drop the unanswered user turn
				}
				break
			}
		}
		return a, nil

	case resultMsg:
		if msg.err != nil {
			a.setFlash(msg.err.Error(), true)
		} else if msg.ok != "" {
			a.setFlash(msg.ok, false)
		}
		return a, nil

	case tickMsg:
		return a, tick()

	case permsMsg:
		if a.proj != nil && msg.pid == a.proj.ID {
			a.perms = msg.ps
			a.permRow = clamp(a.permRow, 0, len(a.perms)-1)
		}
		return a, nil

	case usageMsg:
		if msg.err == nil {
			a.usage = &msg.u
		}
		return a, nil

	case ciMsg:
		if msg.err != nil {
			if msg.report {
				a.setFlash(msg.err.Error(), true)
			}
			return a, nil
		}
		if a.proj != nil && msg.pid == a.proj.ID {
			a.ci = &msg.st
			if msg.report {
				a.setFlash(ciSummary(msg.st), msg.st.State == "failing" || msg.st.Error != "")
			}
		}
		return a, nil

	case procStartedMsg:
		if msg.err != nil {
			a.setFlash(msg.err.Error(), true)
			return a, nil
		}
		a.tab = tabProcs
		a.overlay = ovNone
		return a, tea.Batch(a.loadProcs(), a.attach(msg.p))

	case attachEndMsg:
		if msg.err != nil {
			a.setFlash("attach: "+msg.err.Error(), true)
		}
		return a, a.loadProcs()
	}
	return a, nil
}

func (a *App) reselectProject() tea.Cmd {
	if len(a.projects) == 0 {
		a.proj = nil
		return nil
	}
	want, wantMachine := a.startPj, ""
	if a.proj != nil {
		want, wantMachine = a.proj.ID, a.proj.Machine
	} else if n, m, ok := strings.Cut(want, "@"); ok {
		want, wantMachine = n, m
	}
	for i := range a.projects {
		p := &a.projects[i]
		if (p.ID == want || strings.EqualFold(p.Name, want)) && (wantMachine == "" || p.Machine == wantMachine) {
			if a.proj == nil || a.proj.ID != p.ID || a.proj.Machine != p.Machine {
				return a.selectProject(i)
			}
			a.proj = p
			return nil
		}
	}
	return a.selectProject(0)
}

func (a *App) selectProject(i int) tea.Cmd {
	a.proj = &a.projects[i]
	a.c.SetPrefix(a.prefixOf(a.proj.Machine))
	a.connected = a.links[a.proj.Machine]
	a.todos, a.procs, a.cols = map[string]model.Todo{}, nil, nil
	a.col, a.row, a.procRow = 0, 0, 0
	a.overlay = ovNone
	a.perms = nil
	a.ci = nil
	return tea.Batch(a.loadTodos(), a.loadProcs(), a.loadPerms(), a.loadCI(false, false), a.loadUsage())
}

func (a *App) matches(t model.Todo) bool {
	return a.query == "" || strings.Contains(strings.ToLower(t.Title+" "+t.Prompt), strings.ToLower(a.query))
}

func isOld(t model.Todo) bool {
	at := t.UpdatedAt
	if t.FinishedAt != nil {
		at = *t.FinishedAt
	}
	return t.Status == model.StatusMerged && time.Since(at) > 24*time.Hour
}

// hiddenOld counts merged todos older than a day that the Done column hides.
func (a *App) hiddenOld() int {
	n := 0
	for _, t := range a.todos {
		if isOld(t) && a.matches(t) {
			n++
		}
	}
	return n
}

func (a *App) rebuildCols() {
	a.cols = make([][]model.Todo, len(columns))
	for _, t := range a.todos {
		if !a.matches(t) || (!a.showOld && isOld(t)) {
			continue
		}
		for ci, c := range columns {
			for _, s := range c.statuses {
				if t.Status == s {
					a.cols[ci] = append(a.cols[ci], t)
				}
			}
		}
	}
	for _, col := range a.cols {
		sort.Slice(col, func(i, j int) bool {
			if col[i].Priority != col[j].Priority {
				return col[i].Priority < col[j].Priority
			}
			return col[i].CreatedAt.Before(col[j].CreatedAt)
		})
	}
	a.row = clamp(a.row, 0, len(a.cols[a.col])-1)
}

func (a *App) selected() (model.Todo, bool) {
	if a.overlay == ovDetail {
		t, ok := a.todos[a.dID]
		return t, ok
	}
	if a.tab != tabBoard || a.col >= len(a.cols) || a.row >= len(a.cols[a.col]) || a.row < 0 {
		return model.Todo{}, false
	}
	return a.cols[a.col][a.row], true
}

func (a *App) handleEvent(ev wireEvent) tea.Cmd {
	if ev.Machine != a.machine() {
		return a.elsewhere(ev)
	}
	mine := a.proj != nil && ev.ProjectID == a.proj.ID
	switch ev.Type {
	case model.EvProjectUpdated:
		return a.loadProjects()
	case model.EvTodoUpdated:
		if !mine {
			return nil
		}
		var t model.Todo
		if json.Unmarshal(ev.Data, &t) != nil {
			return nil
		}
		prev := a.todos[t.ID]
		a.todos[t.ID] = t
		a.rebuildCols()
		if prev.ID != "" && prev.Status != t.Status && (t.Status == model.StatusReview || t.Status == model.StatusFailed) {
			a.bell()
		}
		if a.overlay == ovDetail && a.dID == t.ID && prev.Status != t.Status && t.Status == model.StatusReview {
			return a.loadText(t.ID, "diff")
		}
	case model.EvTodoDeleted:
		if mine {
			delete(a.todos, ev.TodoID)
			a.rebuildCols()
			if a.overlay == ovDetail && a.dID == ev.TodoID {
				a.overlay = ovNone
			}
		}
	case model.EvTodoOutput:
		var line string
		_ = json.Unmarshal(ev.Data, &line)
		lines := strings.Split(line, "\n")
		a.lastLine[ev.TodoID] = lines[len(lines)-1]
		if k := ev.TodoID + "log"; a.dText[k] != "" || a.dID == ev.TodoID {
			a.dText[k] += ev.Time.Local().Format(time.TimeOnly) + " " + line + "\n"
		}
	case model.EvGitOp:
		if !mine && ev.ProjectID != "" {
			return nil
		}
		var d struct {
			Cmd string `json:"cmd"`
			OK  bool   `json:"ok"`
			MS  int64  `json:"ms"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		mark := styleOK.Render("✓")
		if !d.OK {
			mark = styleErr.Render("✗")
		}
		a.activity = append(a.activity, fmt.Sprintf("%s %s %s %s", styleDim.Render(ev.Time.Local().Format(time.TimeOnly)), mark, d.Cmd, styleDim.Render(fmt.Sprintf("(%dms)", d.MS))))
		if len(a.activity) > 2000 {
			a.activity = a.activity[len(a.activity)-2000:]
		}
	case model.EvMergeQueue:
		var d struct {
			Pending int `json:"pending"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		a.mergeQueue = d.Pending
	case model.EvProcUpdated:
		if mine {
			return a.loadProcs()
		}
	case model.EvPermission:
		if !mine {
			return nil
		}
		var p PermRequest
		if json.Unmarshal(ev.Data, &p) != nil {
			return nil
		}
		kept := a.perms[:0]
		for _, x := range a.perms {
			if x.ID != p.ID {
				kept = append(kept, x)
			}
		}
		a.perms = kept
		if p.Decision == "" {
			a.perms = append(a.perms, p)
			a.setFlash(fmt.Sprintf("Claude wants to %s — press P to answer", permSummary(p)), false)
			a.bell()
		}
		a.permRow = clamp(a.permRow, 0, len(a.perms)-1)
		if len(a.perms) == 0 && a.overlay == ovPerms {
			a.overlay = ovNone
		}
	case model.EvUsage:
		var u Usage
		if json.Unmarshal(ev.Data, &u) == nil {
			a.usage = &u
		}
	case model.EvCI:
		var st CIStatus
		if a.proj == nil || ev.ProjectID != a.proj.ID || json.Unmarshal(ev.Data, &st) != nil {
			break
		}
		if a.ci != nil && a.ci.State != "failing" && st.State == "failing" {
			msg := "build failing on " + st.Branch
			if st.TodoID != "" {
				msg += "; Claude is on it"
			}
			a.setFlash(msg, true)
			a.bell()
		} else if a.ci != nil && a.ci.State == "failing" && st.State == "passing" {
			a.setFlash("build passing again on "+st.Branch, false)
		}
		a.ci = &st
	}
	return nil
}

// elsewhere handles events from computers other than the selected
// project's: a flash and the bell when something there needs you.
func (a *App) elsewhere(ev wireEvent) tea.Cmd {
	name := ev.Machine
	for _, p := range a.projects {
		if p.ID == ev.ProjectID && p.Machine == ev.Machine {
			name = p.Name + "@" + p.Machine
		}
	}
	switch ev.Type {
	case model.EvProjectUpdated:
		return a.loadProjects()
	case model.EvTodoUpdated:
		var t model.Todo
		if json.Unmarshal(ev.Data, &t) != nil {
			return nil
		}
		key := ev.Machine + "/" + t.ID
		prev := a.seenElsewhere[key]
		a.seenElsewhere[key] = t.Status
		if prev != "" && prev != t.Status && (t.Status == model.StatusReview || t.Status == model.StatusFailed) {
			a.setFlash(fmt.Sprintf("%s: %s is %s", name, title(t), map[model.TodoStatus]string{model.StatusReview: "ready for review", model.StatusFailed: "failed"}[t.Status]), t.Status == model.StatusFailed)
			a.bell()
		}
	case model.EvPermission:
		var p PermRequest
		if json.Unmarshal(ev.Data, &p) == nil && p.Decision == "" {
			a.setFlash(name+": Claude is waiting for your approval (:project "+name+")", true)
			a.bell()
		}
	}
	return nil
}

// permSummary is a one-line description of what Claude asks to do.
func permSummary(p PermRequest) string {
	var in struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
		URL      string `json:"url"`
		Query    string `json:"query"`
	}
	_ = json.Unmarshal(p.Input, &in)
	switch {
	case in.Command != "":
		return "run `" + in.Command + "`"
	case in.FilePath != "":
		return "edit " + in.FilePath
	case in.URL != "":
		return "fetch " + in.URL
	case in.Query != "":
		return "search for " + in.Query
	}
	return "use " + p.Tool
}

// ---------------------------------------------------------------- keys

func (a *App) handleKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "ctrl+c" {
		a.stop()
		return tea.Quit
	}

	switch a.mode {
	case modeCommand:
		switch s {
		case "esc":
			a.mode = modeNormal
			return nil
		case "enter":
			line := strings.TrimSpace(a.cmdline.Value())
			a.mode = modeNormal
			return a.runCommand(line)
		case "backspace":
			if a.cmdline.Value() == "" {
				a.mode = modeNormal
				return nil
			}
		}
		var cmd tea.Cmd
		a.cmdline, cmd = a.cmdline.Update(k)
		return cmd

	case modePrompt:
		switch s {
		case "esc":
			a.mode = modeNormal
			return nil
		case "enter":
			a.mode = modeNormal
			return a.onPrompt(a.promptIn.Value())
		}
		var cmd tea.Cmd
		a.promptIn, cmd = a.promptIn.Update(k)
		return cmd

	case modeConfirm:
		a.mode = modeNormal
		if s == "y" || s == "Y" {
			return a.onConfirm()
		}
		a.setFlash("cancelled", false)
		return nil

	case modeInsert:
		if s == "esc" {
			a.mode = modeNormal
			a.chatInput.Blur()
			a.fTitle.Blur()
			a.fPrompt.Blur()
			return nil
		}
		if a.overlay == ovForm && s == "tab" {
			a.focusField(1 - a.fField)
			return nil
		}
		if a.overlay == ovNone && a.tab == tabChat && s == "enter" {
			return a.sendChat()
		}
		return a.forwardInput(k)
	}

	return a.normalKey(s)
}

func (a *App) forwardInput(msg tea.Msg) tea.Cmd {
	if a.mode != modeInsert {
		return nil
	}
	var cmd tea.Cmd
	switch {
	case a.overlay == ovForm && a.fField == 0:
		a.fTitle, cmd = a.fTitle.Update(msg)
	case a.overlay == ovForm:
		a.fPrompt, cmd = a.fPrompt.Update(msg)
	case a.tab == tabChat:
		a.chatInput, cmd = a.chatInput.Update(msg)
	}
	return cmd
}

func (a *App) focusField(f int) {
	a.fField = f
	if f == 0 {
		a.fPrompt.Blur()
		a.fTitle.Focus()
	} else {
		a.fTitle.Blur()
		a.fPrompt.Focus()
	}
}

func (a *App) enterInsert() {
	a.mode = modeInsert
	if a.overlay == ovForm {
		a.focusField(a.fField)
	} else {
		a.chatInput.Focus()
	}
}

func (a *App) normalKey(s string) tea.Cmd {
	// Counts: 5j, 10G …
	if len(s) == 1 && s[0] >= '0' && s[0] <= '9' && !(s == "0" && a.count == 0) && a.pending == "" {
		a.count = a.count*10 + int(s[0]-'0')
		return nil
	}
	n := max(a.count, 1)
	hadCount := a.count > 0
	a.count = 0

	if a.pending != "" {
		seq := a.pending + s
		a.pending = ""
		switch seq {
		case "gg":
			if hadCount {
				return a.moveTo(n - 1)
			}
			return a.moveTo(0)
		case "gt":
			a.tab = (a.tab + 1) % numTabs
			a.overlay = ovNone
		case "gT":
			a.tab = (a.tab + numTabs - 1) % numTabs
			a.overlay = ovNone
		case "dd":
			return a.deleteSelected()
		case "gx":
			t, ok := a.selected()
			if !ok || t.PR == nil {
				a.setFlash("this todo has no pull request", true)
				return nil
			}
			a.setFlash("copied "+t.PR.URL, false)
			return tea.SetClipboard(t.PR.URL)
		case "ZZ":
			if a.overlay == ovForm {
				return a.saveForm(true)
			}
			a.stop()
			return tea.Quit
		case "ZQ":
			if a.overlay == ovForm {
				a.overlay = ovNone
				return nil
			}
			a.stop()
			return tea.Quit
		}
		return nil
	}

	switch s {
	case "g", "d", "Z":
		a.pending = s
		return nil
	case ":":
		a.mode = modeCommand
		a.cmdline.Reset()
		a.cmdline.SetSuggestions(a.commandSuggestions())
		return a.cmdline.Focus()
	case "?":
		a.overlay = ovHelp
		return nil
	case "esc":
		if a.overlay == ovForm {
			return nil // vim: Esc in normal mode does nothing; use :q
		}
		a.overlay = ovNone
		return nil
	case "q":
		switch a.overlay {
		case ovNone:
			a.stop()
			return tea.Quit
		case ovForm:
			return a.runCommand("q")
		default:
			a.overlay = ovNone
		}
		return nil
	case "ctrl+l":
		return tea.Batch(a.loadProjects(), a.loadTodos(), a.loadProcs(), a.loadPerms(), a.loadUsage())
	case "P":
		if a.overlay != ovForm {
			if len(a.perms) == 0 {
				a.setFlash("no permission requests waiting", false)
				return nil
			}
			a.overlay = ovPerms
			return nil
		}
	case "p":
		if a.overlay != ovForm {
			a.overlay = ovPicker
			for i, p := range a.projects {
				if a.proj != nil && p.ID == a.proj.ID && p.Machine == a.proj.Machine {
					a.pickRow = i
				}
			}
			return nil
		}
	}

	switch a.overlay {
	case ovHelp:
		a.overlay = ovNone
		return nil
	case ovPicker:
		return a.pickerKey(s, n)
	case ovPerms:
		return a.permsKey(s, n)
	case ovForm:
		return a.formKey(s)
	case ovDetail:
		return a.detailKey(s, n)
	}

	switch a.tab {
	case tabBoard:
		return a.boardKey(s, n)
	case tabChat:
		return a.chatKey(s, n)
	case tabProcs:
		return a.procsKey(s, n)
	case tabActivity:
		a.actOff = scrollKey(s, n, a.actOff, a.bodyHeight())
	}
	return nil
}

// moveTo jumps to an absolute position (gg / G) in whatever is focused.
func (a *App) moveTo(i int) tea.Cmd {
	switch {
	case a.overlay == ovDetail:
		a.dOff, a.dFollow = i, false
	case a.overlay == ovPicker:
		a.pickRow = clamp(i, 0, len(a.projects)-1)
	case a.tab == tabBoard && len(a.cols) > 0:
		a.row = clamp(i, 0, len(a.cols[a.col])-1)
	case a.tab == tabProcs:
		a.procRow = clamp(i, 0, len(a.procs)-1)
	case a.tab == tabActivity:
		a.actOff = i
	case a.tab == tabChat:
		a.chatOff = 1 << 30 // top
	}
	return nil
}

// scrollKey handles j/k/ctrl-d/u/f/b/G for a text pane; off is the top line.
func scrollKey(s string, n, off, page int) int {
	switch s {
	case "j", "down":
		return off + n
	case "k", "up":
		return max(0, off-n)
	case "ctrl+d":
		return off + page/2*n
	case "ctrl+u":
		return max(0, off-page/2*n)
	case "ctrl+f", "space":
		return off + page*n
	case "ctrl+b":
		return max(0, off-page*n)
	case "G":
		return 1 << 30
	}
	return off
}

func (a *App) boardKey(s string, n int) tea.Cmd {
	if len(a.cols) == 0 {
		if s == "o" && a.proj != nil {
			return a.openForm(nil)
		}
		return nil
	}
	switch s {
	case "j", "down":
		a.row = clamp(a.row+n, 0, len(a.cols[a.col])-1)
	case "k", "up":
		a.row = clamp(a.row-n, 0, len(a.cols[a.col])-1)
	case "h", "left":
		a.col = clamp(a.col-n, 0, len(a.cols)-1)
		a.row = clamp(a.row, 0, len(a.cols[a.col])-1)
	case "l", "right":
		a.col = clamp(a.col+n, 0, len(a.cols)-1)
		a.row = clamp(a.row, 0, len(a.cols[a.col])-1)
	case "/":
		a.ask("/", func(q string) tea.Cmd {
			a.query = strings.TrimSpace(q)
			a.col, a.row = 0, 0
			a.rebuildCols()
			a.jumpMatch(1, true)
			return nil
		})
		a.promptIn.SetValue(a.query)
		return nil
	case "n":
		a.jumpMatch(n, false)
		return nil
	case "N":
		a.jumpMatch(-n, false)
		return nil
	case "w": // next non-empty column
		for c := a.col + 1; c < len(a.cols); c++ {
			if len(a.cols[c]) > 0 {
				a.col, a.row = c, 0
				break
			}
		}
	case "b":
		for c := a.col - 1; c >= 0; c-- {
			if len(a.cols[c]) > 0 {
				a.col, a.row = c, 0
				break
			}
		}
	case "G":
		a.row = max(0, len(a.cols[a.col])-1)
	case "enter":
		if t, ok := a.selected(); ok {
			return a.openDetail(t)
		}
	case "o", "O":
		return a.openForm(nil)
	default:
		return a.todoAction(s)
	}
	return nil
}

// todoAction handles keys shared by the board and the detail view.
func (a *App) todoAction(s string) tea.Cmd {
	t, ok := a.selected()
	if !ok {
		return nil
	}
	switch s {
	case "s":
		return a.action("queued", func() error { return a.c.TodoAction(t.ID, "enqueue", nil) })
	case "x":
		return a.action("cancelled", func() error { return a.c.TodoAction(t.ID, "cancel", nil) })
	case "a":
		return a.action("approved → merge queue", func() error { return a.c.TodoAction(t.ID, "approve", nil) })
	case "r":
		a.ask("rework feedback: ", func(fb string) tea.Cmd {
			return a.action("sent back for rework", func() error {
				return a.c.TodoAction(t.ID, "rework", map[string]string{"feedback": fb})
			})
		})
	case "e", "c":
		return a.openForm(&t)
	case "+", "K":
		return a.action("", func() error { return a.c.UpdateTodo(t.ID, map[string]any{"priority": t.Priority - 1}) })
	case "-", "J":
		return a.action("", func() error { return a.c.UpdateTodo(t.ID, map[string]any{"priority": t.Priority + 1}) })
	case "*":
		return a.action("", func() error { return a.c.UpdateTodo(t.ID, map[string]any{"goal": !t.Goal}) })
	case "t":
		return a.startProc(StartProc{Kind: model.ProcWeb, Preset: "run", TodoID: t.ID})
	case "!":
		return a.startProc(StartProc{Kind: model.ProcPTY, TodoID: t.ID})
	case "T":
		return a.action("running tests…", func() error { return a.c.RunTests(t.ID) })
	case "Y":
		return a.action("duplicated as a draft", func() error { return a.c.Duplicate(t.ID) })
	case "E":
		return a.action("opening in your editor", func() error { return a.c.OpenEditor(t.ID) })
	case "u":
		if t.Status != model.StatusMerged || t.Revert != nil {
			a.setFlash("only merged todos can be reverted", true)
			return nil
		}
		a.confirm(fmt.Sprintf("revert %q? (y/n)", title(t)), func() tea.Cmd {
			return a.action("reverting…", func() error { return a.c.Revert(t.ID) })
		})
	}
	return nil
}

// jumpMatch moves the cursor dir cards forward/back across columns (n / N).
func (a *App) jumpMatch(dir int, fromStart bool) {
	type pos struct{ c, r int }
	var all []pos
	for ci, col := range a.cols {
		for ri := range col {
			all = append(all, pos{ci, ri})
		}
	}
	if len(all) == 0 {
		a.setFlash("no matches", true)
		return
	}
	i := 0
	if !fromStart {
		for j, p := range all {
			if p.c == a.col && p.r == a.row {
				i = j
			}
		}
		i = ((i+dir)%len(all) + len(all)) % len(all)
	}
	a.col, a.row = all[i].c, all[i].r
}

func (a *App) deleteSelected() tea.Cmd {
	switch {
	case a.tab == tabProcs && a.overlay == ovNone:
		return a.procsKey("x", 1)
	case a.tab == tabChat && a.overlay == ovNone:
		return a.runCommand("clear")
	}
	t, ok := a.selected()
	if !ok {
		return nil
	}
	a.confirm(fmt.Sprintf("delete %q? (y/n)", title(t)), func() tea.Cmd {
		a.overlay = ovNone
		return a.action("deleted", func() error { return a.c.DeleteTodo(t.ID) })
	})
	return nil
}

func (a *App) ask(label string, fn func(string) tea.Cmd) {
	a.mode = modePrompt
	a.promptIn.Reset()
	a.promptIn.Prompt = label
	a.promptIn.Focus()
	a.onPrompt = fn
}

func (a *App) confirm(q string, fn func() tea.Cmd) {
	a.mode = modeConfirm
	a.question = q
	a.onConfirm = fn
}

func (a *App) openDetail(t model.Todo) tea.Cmd {
	a.overlay = ovDetail
	a.dID = t.ID
	a.dOff, a.dFollow = 1<<30, true
	a.dKind = "log"
	if t.Status == model.StatusReview || t.Status == model.StatusMerging {
		a.dKind = "diff"
		a.dOff, a.dFollow = 0, false
	}
	cmds := []tea.Cmd{a.loadText(t.ID, "log")}
	if t.Worktree != nil {
		cmds = append(cmds, a.loadText(t.ID, "diff"))
	}
	return tea.Batch(cmds...)
}

func (a *App) detailKey(s string, n int) tea.Cmd {
	switch s {
	case "tab", "l", "h":
		kinds := []string{"log", "diff", "tests"}
		i := 0
		for j, k := range kinds {
			if k == a.dKind {
				i = j
			}
		}
		if s == "h" {
			i = (i + len(kinds) - 1) % len(kinds)
		} else {
			i = (i + 1) % len(kinds)
		}
		a.dKind, a.dOff, a.dFollow = kinds[i], 0, kinds[i] == "log"
		if a.dFollow {
			a.dOff = 1 << 30
		}
		return nil
	case "R":
		return tea.Batch(a.loadText(a.dID, "log"), a.loadText(a.dID, "diff"))
	case "j", "k", "down", "up", "ctrl+d", "ctrl+u", "ctrl+f", "ctrl+b", "space", "G":
		a.dOff = scrollKey(s, n, a.dOff, a.bodyHeight()-4)
		a.dFollow = s == "G"
		return nil
	}
	return a.todoAction(s)
}

func (a *App) permsKey(s string, n int) tea.Cmd {
	if len(a.perms) == 0 {
		a.overlay = ovNone
		return nil
	}
	p := a.perms[clamp(a.permRow, 0, len(a.perms)-1)]
	decide := func(d, ok string) tea.Cmd {
		return a.action(ok, func() error { return a.c.Decide(p.ID, d) })
	}
	switch s {
	case "j", "down":
		a.permRow = clamp(a.permRow+n, 0, len(a.perms)-1)
	case "k", "up":
		a.permRow = clamp(a.permRow-n, 0, len(a.perms)-1)
	case "y", "enter":
		return decide("allow", "allowed")
	case "A":
		return decide("always", p.Tool+" always allowed in this project")
	case "n", "x":
		return decide("deny", "denied")
	case "o":
		if t, ok := a.todos[p.TodoID]; ok {
			return a.openDetail(t)
		}
	}
	return nil
}

func (a *App) pickerKey(s string, n int) tea.Cmd {
	switch s {
	case "j", "down":
		a.pickRow = clamp(a.pickRow+n, 0, len(a.projects)-1)
	case "k", "up":
		a.pickRow = clamp(a.pickRow-n, 0, len(a.projects)-1)
	case "G":
		a.pickRow = len(a.projects) - 1
	case "enter", "l":
		if a.pickRow < len(a.projects) {
			return a.selectProject(a.pickRow)
		}
	}
	return nil
}

// ---- form

func (a *App) openForm(t *model.Todo) tea.Cmd {
	if a.proj == nil {
		a.setFlash("no project selected — :newproject <name>", true)
		return nil
	}
	a.overlay = ovForm
	a.fTitle.Reset()
	a.fPrompt.Reset()
	a.fGoal, a.fPrio, a.fEditID = false, 0, ""
	if t != nil {
		a.fTitle.SetValue(t.Title)
		a.fPrompt.SetValue(t.Prompt)
		a.fGoal, a.fPrio, a.fEditID = t.Goal, t.Priority, t.ID
	}
	a.fField = 1
	if t == nil {
		a.fField = 0
	}
	a.enterInsert()
	return nil
}

func (a *App) formKey(s string) tea.Cmd {
	switch s {
	case "i", "a", "A", "I", "enter":
		a.enterInsert()
	case "j", "down", "tab":
		a.fField = 1
	case "k", "up", "shift+tab":
		a.fField = 0
	case "*":
		a.fGoal = !a.fGoal
	case "+":
		a.fPrio--
	case "-":
		a.fPrio++
	}
	return nil
}

func (a *App) formDirty() bool {
	return strings.TrimSpace(a.fTitle.Value()) != "" || strings.TrimSpace(a.fPrompt.Value()) != ""
}

func (a *App) saveForm(enqueue bool) tea.Cmd {
	title, prompt := strings.TrimSpace(a.fTitle.Value()), strings.TrimSpace(a.fPrompt.Value())
	if prompt == "" {
		a.setFlash("E: prompt is empty", true)
		return nil
	}
	pid, id, goal, prio := a.proj.ID, a.fEditID, a.fGoal, a.fPrio
	a.overlay = ovNone
	a.mode = modeNormal
	if id != "" {
		return a.action("saved", func() error {
			if err := a.c.UpdateTodo(id, map[string]any{"title": title, "prompt": prompt, "goal": goal, "priority": prio}); err != nil {
				return err
			}
			if enqueue {
				return a.c.TodoAction(id, "enqueue", nil)
			}
			return nil
		})
	}
	ok := "draft saved"
	if enqueue {
		ok = "queued"
	}
	return a.action(ok, func() error {
		_, err := a.c.CreateTodo(pid, NewTodo{Title: title, Prompt: prompt, Goal: goal, Priority: prio, Enqueue: enqueue})
		return err
	})
}

// ---- chat

func (a *App) chatKey(s string, n int) tea.Cmd {
	switch s {
	case "t": // make the last answer a todo
		for i := len(a.chat) - 1; i >= 0; i-- {
			if a.chat[i].role == "assistant" && a.chat[i].text != "" {
				cmd := a.openForm(nil)
				if a.overlay == ovForm {
					a.fPrompt.SetValue(a.chat[i].text)
					a.focusField(0)
				}
				return cmd
			}
		}
		a.setFlash("no answer to turn into a todo yet", true)
		return nil
	case "i", "a", "o", "A", "I":
		a.enterInsert()
		return textarea.Blink
	case "x":
		if a.chatCancel != nil {
			a.chatCancel()
		}
	case "j", "down":
		a.chatOff = max(0, a.chatOff-n)
	case "k", "up":
		a.chatOff += n
	case "ctrl+d":
		a.chatOff = max(0, a.chatOff-a.bodyHeight()/2*n)
	case "ctrl+u":
		a.chatOff += a.bodyHeight() / 2 * n
	case "G":
		a.chatOff = 0
	}
	return nil
}

func (a *App) sendChat() tea.Cmd {
	q := strings.TrimSpace(a.chatInput.Value())
	if q == "" || a.chatCancel != nil {
		return nil
	}
	a.chatInput.Reset()
	a.chatOff = 0
	a.chat = append(a.chat, chatEntry{role: "user", text: q}, chatEntry{role: "assistant"})
	a.chatHist = append(a.chatHist, agent.ChatMessage{Role: "user", Content: q})
	hist := append([]agent.ChatMessage(nil), a.chatHist...)
	ctx, cancel := context.WithCancel(a.ctx)
	a.chatCancel = cancel
	return func() tea.Msg {
		defer cancel()
		err := a.c.ChatTurn(ctx, hist, func(d agent.ChatDelta) { a.prog.Send(chatDeltaMsg(d)) })
		return chatEndMsg{err}
	}
}

// ---- processes

func (a *App) procsKey(s string, n int) tea.Cmd {
	var cur *model.Process
	if a.procRow >= 0 && a.procRow < len(a.procs) {
		cur = &a.procs[a.procRow]
	}
	switch s {
	case "j", "down":
		a.procRow = clamp(a.procRow+n, 0, len(a.procs)-1)
	case "k", "up":
		a.procRow = clamp(a.procRow-n, 0, len(a.procs)-1)
	case "G":
		a.procRow = len(a.procs) - 1
	case "enter":
		if cur != nil {
			return a.attach(*cur)
		}
	case "x":
		if cur != nil && cur.Running {
			id := cur.ID
			return a.action("stopping", func() error { return a.c.StopProcess(id) })
		}
	case "o":
		if cur != nil && cur.URL != "" {
			return a.action("opened "+cur.URL, func() error { return openBrowser(cur.URL) })
		}
	case "R":
		return a.startProc(StartProc{Kind: model.ProcWeb, Preset: "run"})
	case "T":
		return a.startProc(StartProc{Kind: model.ProcTask, Preset: "test"})
	case "D":
		return a.startProc(StartProc{Kind: model.ProcTask, Preset: "deploy"})
	case "!":
		return a.startProc(StartProc{Kind: model.ProcPTY})
	}
	return nil
}

func (a *App) startProc(req StartProc) tea.Cmd {
	if a.proj == nil {
		return nil
	}
	pid := a.proj.ID
	req.Cols, req.Rows = a.w, a.h-1
	return func() tea.Msg {
		p, err := a.c.StartProcess(pid, req)
		return procStartedMsg{p, err}
	}
}

func (a *App) attach(p model.Process) tea.Cmd {
	title := fmt.Sprintf("[%s] %s", p.Kind, p.Command)
	if p.URL != "" {
		title += "  " + p.URL
	}
	return tea.Exec(&attachCmd{client: a.c, procID: p.ID, title: title}, func(err error) tea.Msg {
		return attachEndMsg{err}
	})
}

func openBrowser(url string) error {
	bin := "xdg-open"
	if runtime.GOOS == "darwin" {
		bin = "open"
	}
	return exec.Command(bin, url).Start()
}

// ---- : commands

func (a *App) runCommand(line string) tea.Cmd {
	if line == "" {
		return nil
	}
	if n, err := strconv.Atoi(line); err == nil { // :42 → line 42
		return a.moveTo(n - 1)
	}
	name, arg, _ := strings.Cut(line, " ")
	arg = strings.TrimSpace(arg)

	if a.overlay == ovForm {
		switch name {
		case "w":
			return a.saveForm(false)
		case "wq", "x":
			return a.saveForm(true)
		case "q":
			if a.formDirty() {
				a.setFlash("E37: No write since last change (add ! to override)", true)
				return nil
			}
			a.overlay = ovNone
			return nil
		case "q!":
			a.overlay = ovNone
			return nil
		}
	}

	switch name {
	case "q", "q!", "qa", "qa!", "quit", "wq", "x":
		if a.overlay != ovNone && !strings.HasPrefix(name, "qa") {
			a.overlay = ovNone
			return nil
		}
		a.stop()
		return tea.Quit
	case "h", "help":
		a.overlay = ovHelp
	case "b", "board":
		a.tab, a.overlay = tabBoard, ovNone
	case "c", "chat":
		a.tab, a.overlay = tabChat, ovNone
	case "procs", "ps":
		a.tab, a.overlay = tabProcs, ovNone
	case "activity", "log":
		a.tab, a.overlay = tabActivity, ovNone
	case "p", "project":
		if arg == "" {
			a.overlay = ovPicker
			return nil
		}
		pn, mn, _ := strings.Cut(strings.ToLower(arg), "@") // name@computer
		for i, p := range a.projects {
			if strings.HasPrefix(strings.ToLower(p.Name), pn) && strings.HasPrefix(strings.ToLower(p.Machine), mn) {
				return a.selectProject(i)
			}
		}
		a.setFlash("E: no project matching "+arg, true)
	case "newproject", "np":
		pname, path, _ := strings.Cut(arg, " ")
		if pname == "" {
			a.setFlash("usage: :newproject <name> [path]", true)
			return nil
		}
		a.startPj = pname
		a.proj = nil
		return a.action("project created", func() error {
			_, err := a.c.CreateProject(pname, strings.TrimSpace(path))
			return err
		})
	case "new", "o":
		cmd := a.openForm(nil)
		if arg != "" {
			a.fPrompt.SetValue(arg)
		}
		return cmd
	case "todo":
		if a.proj == nil || arg == "" {
			a.setFlash("usage: :todo <prompt>  (creates and queues)", true)
			return nil
		}
		pid := a.proj.ID
		return a.action("queued", func() error {
			_, err := a.c.CreateTodo(pid, NewTodo{Prompt: arg, Enqueue: true})
			return err
		})
	case "push":
		if a.proj == nil {
			return nil
		}
		pid := a.proj.ID
		return a.action("pushed", func() error { return a.c.PushProject(pid) })
	case "run", "!":
		return a.startProc(StartProc{Kind: model.ProcPTY, Command: arg})
	case "ci", "builds":
		if a.proj == nil || a.proj.RemoteURL == "" {
			a.setFlash("connect a GitHub repository first", true)
			return nil
		}
		a.setFlash("checking builds…", false)
		return a.loadCI(true, true)
	case "fix", "investigate":
		if a.proj == nil {
			return nil
		}
		pid := a.proj.ID
		return func() tea.Msg {
			t, err := a.c.InvestigateCI(pid)
			return resultMsg{ok: "queued: " + t.Title, err: err}
		}
	case "cleanup", "clean":
		if a.proj == nil {
			return nil
		}
		pid := a.proj.ID
		a.setFlash("cleaning up…", false)
		return func() tea.Msg {
			summary, err := a.c.Cleanup(pid)
			return resultMsg{ok: summary, err: err}
		}
	case "noh", "nohlsearch":
		a.query = ""
		a.rebuildCols()
	case "old", "all":
		a.showOld = !a.showOld
		a.rebuildCols()
		if a.showOld {
			a.setFlash("showing merged todos older than a day", false)
		}
	case "copy":
		t, ok := a.selected()
		if ok && arg == "prompt" {
			a.setFlash("copied the prompt", false)
			return tea.SetClipboard(t.Prompt)
		}
		if !ok || t.Worktree == nil {
			a.setFlash("select a todo with a worktree first", true)
			return nil
		}
		text := t.Worktree.Branch
		if arg == "path" {
			text = t.Worktree.Path
		}
		a.setFlash("copied "+text, false)
		return tea.SetClipboard(text)
	case "pause", "resume":
		paused := name == "pause"
		return func() tea.Msg {
			u, err := a.c.SetQueuePaused(paused)
			if err != nil {
				return resultMsg{err: err}
			}
			a.prog.Send(usageMsg{u, nil})
			if paused {
				return resultMsg{ok: "queue paused: no new todos start"}
			}
			return resultMsg{ok: "queue resumed"}
		}
	case "perms", "permissions":
		if len(a.perms) > 0 {
			a.overlay = ovPerms
		} else {
			a.setFlash("no permission requests waiting", false)
		}
	case "clear":
		if a.chatCancel != nil {
			a.chatCancel()
		}
		a.chat, a.chatHist, a.chatOff = nil, nil, 0
		return a.action("chat cleared", a.c.ClearChat)
	case "e", "e!", "refresh":
		return tea.Batch(a.loadProjects(), a.loadTodos(), a.loadProcs())
	default:
		a.setFlash("E492: Not an editor command: "+line, true)
	}
	return nil
}

// ---------------------------------------------------------------- utils

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return max(lo, min(v, hi))
}

func title(t model.Todo) string {
	if t.Title != "" {
		return t.Title
	}
	s, _, _ := strings.Cut(strings.TrimSpace(t.Prompt), "\n")
	return s
}
