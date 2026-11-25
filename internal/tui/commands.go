package tui

import (
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/klahr/dash/internal/model"
)

// exCommand runs one : command. name is the name as typed (so one func can
// serve several names), arg the rest of the line, trimmed.
type exCommand func(a *App, name, arg string) tea.Cmd

// formCommands are the : commands while the task form is open; they win
// over exCommands.
var formCommands = map[string]exCommand{
	"w":  func(a *App, _, _ string) tea.Cmd { return a.saveForm(false) },
	"wq": func(a *App, _, _ string) tea.Cmd { return a.saveForm(true) },
	"x":  func(a *App, _, _ string) tea.Cmd { return a.saveForm(true) },
	"q":  (*App).cmdFormQuit,
	"q!": func(a *App, _, _ string) tea.Cmd { a.overlay = ovNone; return nil },
}

// exCommands are the : commands. Aliases are extra keys for the same func.
var exCommands = map[string]exCommand{
	"q": (*App).cmdQuit, "q!": (*App).cmdQuit, "qa": (*App).cmdQuit, "qa!": (*App).cmdQuit,
	"quit": (*App).cmdQuit, "wq": (*App).cmdQuit, "x": (*App).cmdQuit,

	"h": (*App).cmdHelp, "help": (*App).cmdHelp,
	"b": (*App).cmdBoard, "board": (*App).cmdBoard,
	"c": (*App).cmdChat, "chat": (*App).cmdChat,
	"procs": (*App).cmdProcs, "ps": (*App).cmdProcs,

	"p": (*App).cmdProject, "project": (*App).cmdProject,
	"newproject": (*App).cmdNewProject, "np": (*App).cmdNewProject,
	"addclone": (*App).cmdAddClone, "ac": (*App).cmdAddClone,

	"new": (*App).cmdNew, "o": (*App).cmdNew,
	"todo": (*App).cmdTask, "task": (*App).cmdTask,
	"push": (*App).cmdPush,
	"run":  (*App).cmdRun, "!": (*App).cmdRun,
	"ci": (*App).cmdCI, "builds": (*App).cmdCI,
	"fix": (*App).cmdFix, "investigate": (*App).cmdFix,
	"cleanup": (*App).cmdCleanup, "clean": (*App).cmdCleanup,

	"goal": (*App).cmdGoal,
	"noh":  (*App).cmdNoh, "nohlsearch": (*App).cmdNoh,
	"old": (*App).cmdOld, "all": (*App).cmdOld,
	"copy":  (*App).cmdCopy,
	"pause": (*App).cmdPause, "resume": (*App).cmdPause,
	"perms": (*App).cmdPerms, "permissions": (*App).cmdPerms,
	"clear": (*App).cmdClear,
	"e":     (*App).cmdRefresh, "e!": (*App).cmdRefresh, "refresh": (*App).cmdRefresh,
}

// runCommand runs a line typed on the : prompt.
func (a *App) runCommand(line string) tea.Cmd {
	if line == "" {
		return nil
	}
	if n, err := strconv.Atoi(line); err == nil { // :42 → line 42
		return a.moveTo(n - 1)
	}
	name, arg, _ := strings.Cut(line, " ")
	arg = strings.TrimSpace(arg)
	if f, ok := formCommands[name]; ok && a.overlay == ovForm {
		return f(a, name, arg)
	}
	if f, ok := exCommands[name]; ok {
		return f(a, name, arg)
	}
	a.setFlash("E492: Not an editor command: "+line, true)
	return nil
}

func (a *App) cmdFormQuit(_, _ string) tea.Cmd {
	if a.formDirty() {
		a.setFlash("E37: No write since last change (add ! to override)", true)
		return nil
	}
	a.overlay = ovNone
	return nil
}

// cmdQuit closes the open overlay, or quits; :qa quits from an overlay too.
func (a *App) cmdQuit(name, _ string) tea.Cmd {
	if a.overlay != ovNone && !strings.HasPrefix(name, "qa") {
		a.overlay = ovNone
		return nil
	}
	a.stop()
	return tea.Quit
}

func (a *App) cmdHelp(_, _ string) tea.Cmd {
	a.overlay = ovHelp
	return nil
}

func (a *App) cmdBoard(_, _ string) tea.Cmd {
	a.tab, a.overlay = tabBoard, ovNone
	return nil
}

func (a *App) cmdChat(_, _ string) tea.Cmd {
	a.tab, a.overlay = tabChat, ovNone
	return nil
}

func (a *App) cmdProcs(_, _ string) tea.Cmd {
	a.tab, a.overlay = tabProcs, ovNone
	return nil
}

// cmdProject opens the picker, or selects the first project matching
// "name" or "name@computer" by prefix.
func (a *App) cmdProject(_, arg string) tea.Cmd {
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
	return nil
}

// cmdNewProject: :newproject <name> [path].
func (a *App) cmdNewProject(_, arg string) tea.Cmd {
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
}

// cmdAddClone: :addclone <path> [name].
func (a *App) cmdAddClone(_, arg string) tea.Cmd {
	path, pname, _ := strings.Cut(arg, " ")
	if path == "" {
		a.setFlash("usage: :addclone <path> [name]", true)
		return nil
	}
	pname = strings.TrimSpace(pname)
	a.startPj = pname
	if pname == "" {
		a.startPj = filepath.Base(path)
	}
	a.proj = nil
	return a.action("clone added", func() error {
		_, err := a.c.AddClone(path, pname)
		return err
	})
}

// cmdNew opens the task form, with arg as the prompt.
func (a *App) cmdNew(_, arg string) tea.Cmd {
	cmd := a.openForm(nil)
	if arg != "" {
		a.fPrompt.SetValue(arg)
	}
	return cmd
}

// cmdTask creates a task from arg and queues it.
func (a *App) cmdTask(_, arg string) tea.Cmd {
	if a.proj == nil || arg == "" {
		a.setFlash("usage: :task <prompt>  (creates and queues)", true)
		return nil
	}
	pid := a.proj.ID
	return a.action("queued", func() error {
		_, err := a.c.CreateTodo(pid, NewTodo{Prompt: arg, Enqueue: true})
		return err
	})
}

func (a *App) cmdPush(_, _ string) tea.Cmd {
	if a.proj == nil {
		return nil
	}
	pid := a.proj.ID
	return a.action("pushed", func() error { return a.c.PushProject(pid) })
}

func (a *App) cmdRun(_, arg string) tea.Cmd {
	return a.startProc(StartProc{Kind: model.ProcPTY, Command: arg})
}

func (a *App) cmdCI(_, _ string) tea.Cmd {
	if a.proj == nil || a.proj.RemoteURL == "" {
		a.setFlash("connect a GitHub repository first", true)
		return nil
	}
	a.setFlash("checking builds…", false)
	return a.loadCI(true, true)
}

// cmdFix queues a task that investigates the failing build.
func (a *App) cmdFix(_, _ string) tea.Cmd {
	if a.proj == nil {
		return nil
	}
	pid := a.proj.ID
	return func() tea.Msg {
		t, err := a.c.InvestigateCI(pid)
		return resultMsg{ok: "queued: " + t.Title, err: err}
	}
}

func (a *App) cmdCleanup(_, _ string) tea.Cmd {
	if a.proj == nil {
		return nil
	}
	pid := a.proj.ID
	a.setFlash("cleaning up…", false)
	return func() tea.Msg {
		summary, err := a.c.Cleanup(pid)
		return resultMsg{ok: summary, err: err}
	}
}

// cmdGoal filters the board by the goal matching arg, or clears the filter.
func (a *App) cmdGoal(_, arg string) tea.Cmd {
	if arg == "" {
		a.filterGoal("")
		return nil
	}
	if g, ok := a.goalNamed(arg); ok {
		a.filterGoal(g.ID)
	} else {
		a.setFlash("E: no goal matching "+arg, true)
	}
	return nil
}

func (a *App) cmdNoh(_, _ string) tea.Cmd {
	a.query = ""
	a.rebuildCols()
	return nil
}

func (a *App) cmdOld(_, _ string) tea.Cmd {
	a.showOld = !a.showOld
	a.rebuildCols()
	if a.showOld {
		a.setFlash("showing merged tasks older than a day", false)
	}
	return nil
}

// cmdCopy copies the selected task's branch, or with "path" its worktree
// path, or with "prompt" its prompt.
func (a *App) cmdCopy(_, arg string) tea.Cmd {
	t, ok := a.selected()
	if ok && arg == "prompt" {
		a.setFlash("copied the prompt", false)
		return tea.SetClipboard(t.Prompt)
	}
	if !ok || t.Worktree == nil {
		a.setFlash("select a task with a worktree first", true)
		return nil
	}
	text := t.Worktree.Branch
	if arg == "path" {
		text = t.Worktree.Path
	}
	a.setFlash("copied "+text, false)
	return tea.SetClipboard(text)
}

// cmdPause pauses the queue for :pause and resumes it for :resume.
func (a *App) cmdPause(name, _ string) tea.Cmd {
	paused := name == "pause"
	return func() tea.Msg {
		u, err := a.c.SetQueuePaused(paused)
		if err != nil {
			return resultMsg{err: err}
		}
		a.prog.Send(usageMsg{u, nil})
		if paused {
			return resultMsg{ok: "queue paused: no new tasks start"}
		}
		return resultMsg{ok: "queue resumed"}
	}
}

func (a *App) cmdPerms(_, _ string) tea.Cmd {
	if len(a.perms) > 0 {
		a.overlay = ovPerms
	} else {
		a.setFlash("no permission requests waiting", false)
	}
	return nil
}

func (a *App) cmdClear(_, _ string) tea.Cmd {
	if a.chatCancel != nil {
		a.chatCancel()
	}
	a.chat, a.chatHist, a.chatOff = nil, nil, 0
	return a.action("chat cleared", a.c.ClearChat)
}

func (a *App) cmdRefresh(_, _ string) tea.Cmd {
	return tea.Batch(a.loadProjects(), a.loadTodos(), a.loadGoals(), a.loadProcs())
}
