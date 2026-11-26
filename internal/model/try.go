package model

import "time"

// TrySession is a project's "Try" session: a terminal, or (Kind
// PreviewWeb) a commit preview. A terminal session runs the project's preview_command
// (or a shell) in a PTY, like a task's terminal preview, but in a detached
// worktree of the project itself at a commit of its default branch (its tip
// as fetched, or one picked from history). It always runs on a runner, in a
// worktree Dash made from the runner's clone and removes when it stops;
// never in the clone's own checkout. A project has at most one.
type TrySession struct {
	// ProjectID is the hub's id of the project (the runner keeps the
	// session under it, the id its hub knows the project by).
	ProjectID string `json:"project_id"`
	ProcessID string `json:"process_id"`
	// Runner and RunnerID name the runner it runs on (set by the hub).
	Runner   string `json:"runner,omitempty"`
	RunnerID string `json:"runner_id,omitempty"`
	// Ref is what was asked for: "" for the default branch's tip.
	Ref    string `json:"ref,omitempty"`
	Branch string `json:"branch"` // the project's default branch on the runner
	// Commit is the checked-out commit, with its subject and author.
	Commit  string `json:"commit"`
	Subject string `json:"subject"`
	Author  string `json:"author,omitempty"`
	// Kind is PreviewTerminal ("" too: older runners) for a terminal, or
	// PreviewWeb for a commit preview: the project's run_command served
	// through the preview proxy at URL, like a task's web preview.
	Kind  string       `json:"kind,omitempty"`
	Port  int          `json:"port,omitempty"` // a web session's server port, on its runner
	State PreviewState `json:"state"`
	// URL and OpenURL are where a web session is served and the Dash path
	// that signs the browser in to it (set by the hub, with a preview
	// domain), as model.Preview's.
	URL     string `json:"url,omitempty"`
	OpenURL string `json:"open_url,omitempty"`
	// TerminalURL is the WebSocket path that attaches to it (on the hub:
	// the hub's, which goes through to the runner).
	TerminalURL string    `json:"terminal_url,omitempty"`
	ExitCode    *int      `json:"exit_code,omitempty"`
	Reason      string    `json:"reason,omitempty"` // why it stopped
	StartedAt   time.Time `json:"started_at"`
	LastSeen    time.Time `json:"last_seen"` // last keystroke
}

// CommitInfo is one commit in a project's history, for the Try commit picker.
type CommitInfo struct {
	SHA     string    `json:"sha"`
	Short   string    `json:"short"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
}

// Web reports whether s is a commit preview (the run_command served
// through the preview proxy) rather than a terminal.
func (s TrySession) Web() bool { return s.Kind == PreviewWeb }
