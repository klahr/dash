// Package model holds Dash's core domain types. It has no dependencies on
// other Dash packages so every layer can import it.
package model

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TodoStatus is the lifecycle state of a Todo.
//
//	Draft ──enqueue──▶ Queued ──worker──▶ InProgress ──done──▶ Review
//	  ▲                  ▲                    │                  │  │
//	  └──────cancel──────┴──────rework────────┼──────────────────┘  │approve
//	                                          ▼                     ▼
//	                                        Failed ◀──conflict── Merging ──▶ Merged
//
// In "pr" mode a finished task goes through Merging (its pull request is
// opened) to Review, where it waits on GitHub: merged there it is Merged,
// closed there Failed.
type TodoStatus string

const (
	StatusDraft      TodoStatus = "draft"
	StatusQueued     TodoStatus = "queued"
	StatusInProgress TodoStatus = "in_progress"
	StatusReview     TodoStatus = "review"
	StatusMerging    TodoStatus = "merging" // approved, waiting in / being processed by the merge queue
	StatusMerged     TodoStatus = "merged"
	StatusFailed     TodoStatus = "failed"
)

var transitions = map[TodoStatus][]TodoStatus{
	StatusDraft:      {StatusQueued},
	StatusQueued:     {StatusDraft, StatusInProgress},
	StatusInProgress: {StatusReview, StatusFailed, StatusQueued, StatusMerging},              // → merging: auto-merge after a conflict fix
	StatusReview:     {StatusMerging, StatusQueued, StatusDraft, StatusMerged, StatusFailed}, // → merged/failed: its PR was merged/closed on GitHub
	StatusMerging:    {StatusMerged, StatusFailed, StatusReview, StatusQueued},               // → queued: conflict sent back to Claude
	StatusFailed:     {StatusQueued, StatusDraft, StatusReview, StatusMerged, StatusMerging},
	StatusMerged:     {},
}

// CanTransition reports whether a Todo may move from one status to another.
func CanTransition(from, to TodoStatus) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Project is a local Git repository managed by Dash. Path is the user's own
// checkout: Dash merges in a checkout of its own and only moves Path's files
// along when it has DefaultBranch checked out. All AI work happens in worktrees.
type Project struct {
	ID string `json:"id"`
	// OrgID is the organization the project belongs to; its todos, issues,
	// scans and chats belong there with it. Set when it is created, never
	// changed.
	OrgID         int64  `json:"org_id,omitempty"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	DefaultBranch string `json:"default_branch"`
	// WorkBranch is the branch tasks currently start from and land on, when
	// it isn't DefaultBranch (picked with the branch switcher); empty means
	// DefaultBranch. See Base.
	WorkBranch string `json:"work_branch,omitempty"`
	RemoteURL  string `json:"remote_url,omitempty"`
	// CloneURL, on a hub's project, is the URL its runners clone, as it is
	// (a remote that isn't GitHub's, a deploy key's SSH alias). Empty, they
	// clone RemoteURL, a GitHub repository over the runner's own
	// -github-protocol. It is a URL only: never credentials.
	CloneURL string `json:"clone_url,omitempty"`
	AutoPush bool   `json:"auto_push"` // push DefaultBranch after every merge
	// AutoApprove: finished todos whose tests pass skip review. In "pr"
	// mode their pull requests merge without an approving review on GitHub.
	AutoApprove   bool   `json:"auto_approve"`
	RunCommand    string `json:"run_command,omitempty"`    // e.g. "npm run dev" — gets $PORT
	TestCommand   string `json:"test_command,omitempty"`   // e.g. "go test ./..."
	DeployCommand string `json:"deploy_command,omitempty"` // e.g. "./deploy.sh" or "adb install …"
	// PreviewKind is how a task in review is previewed: PreviewWeb ("" too)
	// runs RunCommand and proxies its port; PreviewTerminal opens a terminal
	// in the task's worktree (for CLI tools, which open no port), running
	// PreviewCommand, or a plain shell when that is empty.
	PreviewKind string `json:"preview_kind,omitempty"`
	// PreviewCommand runs in the worktree of a terminal preview, with
	// ./.dash-bin on PATH and $DASH_BIN set to it, e.g.
	// "go build -o ./.dash-bin/tool . && exec $SHELL".
	PreviewCommand string `json:"preview_command,omitempty"`
	// SetupCommand runs once in every new worktree before Claude starts,
	// e.g. "npm ci". CopyFiles are copied from the main checkout first
	// (untracked files such as ".env" that worktrees don't get from git).
	SetupCommand string   `json:"setup_command,omitempty"`
	CopyFiles    []string `json:"copy_files,omitempty"`
	// AllowedTools run without a permission prompt in every todo, e.g. "Bash"
	// or "WebFetch". "Always allow" on a prompt adds to it.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// ChatPermissionMode is passed as --permission-mode to the project's chats
	// in shell mode: "" (ask for everything not allowed), ChatModeAuto or
	// ChatModeAcceptEdits. What the mode doesn't approve still asks the user.
	ChatPermissionMode string `json:"chat_permission_mode,omitempty"`
	// ReviewPrompt is what the task chat's Review button asks Claude; empty
	// means the web UI's default.
	ReviewPrompt string `json:"review_prompt,omitempty"`
	// TaskInstructions are extra instructions for Claude in every task of
	// the project (on this Dash or a runner), passed with
	// --append-system-prompt. Stored by Dash, unlike the repo's CLAUDE.md.
	TaskInstructions string `json:"task_instructions,omitempty"`
	// MaxAutoFixes is how many times Dash sends failing tests or merge
	// conflicts back to Claude on its own before asking a human.
	MaxAutoFixes int `json:"max_auto_fixes"`
	// MergeMode is how finished work lands: "pr" (push the branch and open
	// a GitHub pull request as soon as Claude finishes; it is reviewed and
	// merged by a person on GitHub, never by Dash; Dash marks the todo
	// merged when the PR is) or "local"
	// (approved in Dash, then squashed
	// onto the default branch, then push if AutoPush; on runners always
	// pushed, see LandsOnOrigin). Unless the user chose (MergeModeSet), it
	// is DefaultMergeMode: pull requests for GitHub projects.
	MergeMode string `json:"merge_mode,omitempty"`
	// MergeModeSet records that a user changed MergeMode themselves, so
	// the default (see store.DefaultMergeModes) leaves their choice alone.
	MergeModeSet bool `json:"merge_mode_set,omitempty"`
	// Limits for one Claude run; 0 means the default. Time spent waiting
	// for a human to answer a permission prompt doesn't count.
	MaxRunMinutes int `json:"max_run_minutes,omitempty"`
	MaxTurns      int `json:"max_turns,omitempty"`
	// SyncError is set while the default branch can't be synced with or
	// pushed to origin (e.g. it diverged); cleared on the next success.
	SyncError string `json:"sync_error,omitempty"`
	// Color is an optional "#rrggbb" the UI marks the project with, so it's
	// clear at a glance which one is open.
	Color string `json:"color,omitempty"`
	// Icon is an optional image upload ("<upload id>/<name>") shown in place
	// of the project's initial; served at /api/uploads/<Icon>.
	Icon string `json:"icon,omitempty"`
	// InvestigateCI queues a todo for Claude when a GitHub Actions build on
	// the default branch fails (nil means on). CIHandledRun is the newest
	// run already looked at, so a failure is only investigated once.
	InvestigateCI *bool     `json:"investigate_ci,omitempty"`
	CIHandledRun  int64     `json:"ci_handled_run,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	// TagFormat names release tags: X, Y and Z stand for the major, minor
	// and patch version, anything else is kept, e.g. "vX.Y.Z". Empty means
	// "X.Y.Z" (see internal/release).
	TagFormat string `json:"tag_format,omitempty"`

	// Runners are the ids of the runners (`dash runner`) this project's
	// tasks are sent to, on a hub. A project with runners needs a git
	// remote: each runner keeps its own clone and lands approved work
	// through a pull request ("pr", the default on GitHub), or by squashing
	// it onto the default branch there and pushing it to origin ("local");
	// the hub never merges in a checkout of its own.
	Runners []string `json:"runners,omitempty"`
	// NotesPending are, on a hub, removals of the project's shared notes
	// asked while none of its runners was online: the commands only, sent
	// to the first runner of the project that connects.
	NotesPending []NotesRemoval `json:"notes_pending,omitempty"`
	// HubProjectID is set on a runner for its clone of a hub's project
	// linked to it: the hub's id for that project. The runner takes tasks
	// for linked projects only, found by this id (never by where they are).
	HubProjectID string `json:"hub_project_id,omitempty"`
	// HubCheckout is how the runner came by the checkout of a hub's
	// project: HubCloned (it cloned it into its projects folder),
	// HubAdopted (a clone of the same remote was already there; its owner
	// may also work in it by hand) or HubLegacy (cloned under
	// <projects>/hub/ before hub projects were found by id).
	HubCheckout string `json:"hub_checkout,omitempty"`
	// HubOrgFolder is, on a runner, the folder under its projects root
	// that its hub's clones of this project's org go in (dispatch.Link's
	// OrgFolder, or org-<id> from older hubs), as of the hub's last link.
	// A clone made before clones went there is moved into it once idle
	// (see api.MoveHubClones).
	HubOrgFolder string `json:"hub_org_folder,omitempty"`
	// LocalOnly marks a runner's own project made as a new repository with
	// no remote (the hub's Add project → Local only), for throwaway work:
	// deleting the project deletes its folder too.
	LocalOnly bool `json:"local_only,omitempty"`

	// Machine is the computer the project lives on; filled in by clients
	// that show several computers in one dashboard, never stored.
	Machine string `json:"machine,omitempty"`
}

// NotesRemoval is a removal of a hub project's shared notes asked in the
// UI (notes.Removal, as the hub keeps it): what to remove, never a note.
type NotesRemoval struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`             // notes.RemoveAll, …
	Task   string    `json:"task,omitempty"`   // the hub's task id
	Runner string    `json:"runner,omitempty"` // a runner id
	Before time.Time `json:"before"`           // when it was asked
	By     int64     `json:"by,omitempty"`     // the user who asked
}

// Base is the branch tasks start from and land on: WorkBranch when one is
// picked, else DefaultBranch.
func (p Project) Base() string {
	if p.WorkBranch != "" {
		return p.WorkBranch
	}
	return p.DefaultBranch
}

// OnWorkBranch reports whether tasks land on a branch other than the default one.
func (p Project) OnWorkBranch() bool { return p.Base() != p.DefaultBranch }

// For returns p as seen by a todo: one that started on another branch
// (before the branch was switched) keeps landing on the branch it started
// from. A worktree from before branches could be switched is on DefaultBranch.
func (p Project) For(t Todo) Project {
	if t.Worktree != nil {
		p.WorkBranch = t.Worktree.BaseBranch
	}
	return p
}

// OnRunners reports whether the project's tasks run on runners rather than
// on this Dash.
func (p Project) OnRunners() bool { return len(p.Runners) > 0 }

// InvestigatesCI reports whether failed builds get a todo automatically.
func (p Project) InvestigatesCI() bool { return p.InvestigateCI == nil || *p.InvestigateCI }

const (
	DefaultMaxAutoFixes  = 2
	DefaultMaxRunMinutes = 60
	DefaultMaxTurns      = 200

	MergeLocal = "local"
	MergePR    = "pr"

	ChatModeAuto        = "auto"
	ChatModeAcceptEdits = "acceptEdits"
)

// UsesPR reports whether approved todos are landed through GitHub pull requests.
func (p Project) UsesPR() bool { return p.MergeMode == MergePR }

// DefaultMergeMode is how p lands work when its user didn't choose: through
// pull requests when its remote is on GitHub and gh can open them, else
// "local". gh is whether this Dash has gh; for a project on runners it
// doesn't matter here, as the runners land the work (a runner without gh
// merges instead, see dispatch.Link.MergeModeSet).
func (p Project) DefaultMergeMode(gh bool) string {
	if IsGitHub(p.RemoteURL) && (gh || p.OnRunners()) {
		return MergePR
	}
	return MergeLocal
}

// IsGitHub reports whether remote is a repository on GitHub.
func IsGitHub(remote string) bool {
	r := strings.ToLower(strings.TrimSpace(remote))
	if i := strings.Index(r, "://"); i >= 0 {
		r = r[i+3:]
	}
	if i := strings.IndexByte(r, '@'); i >= 0 && i < strings.IndexByte(r+"/", '/') {
		r = r[i+1:]
	}
	host, _, _ := strings.Cut(r, "/")
	host, _, _ = strings.Cut(host, ":")
	return host == "github.com" || strings.HasSuffix(host, ".github.com")
}

// IsGitHubWebURL reports whether u is an https:// page on GitHub (a pull
// request, a check, a workflow run, a release), the only links a runner may
// report: a runner isn't trusted, and a link it reports is shown on the
// hub's dashboard, where it must not lead back into the hub itself.
func IsGitHubWebURL(u string) bool {
	x, err := url.Parse(u)
	if err != nil || x.Scheme != "https" || x.User != nil || x.Opaque != "" {
		return false
	}
	host := strings.ToLower(x.Hostname())
	return x.Port() == "" && (host == "github.com" || strings.HasSuffix(host, ".github.com"))
}

// GitHubWebURL is u if it is a page on GitHub (see IsGitHubWebURL), else "".
func GitHubWebURL(u string) string {
	if IsGitHubWebURL(u) {
		return u
	}
	return ""
}

// SyncsRemote reports whether the default branch is kept in step with origin
// before merging: whenever Dash pushes it or opens pull requests against it.
func (p Project) SyncsRemote() bool {
	return p.RemoteURL != "" && (p.AutoPush || p.UsesPR() || p.Shared())
}

// Shared reports whether the project's work is shared through origin by
// several Dashes: a hub's project with runners, or a runner's clone of one.
func (p Project) Shared() bool { return p.OnRunners() || p.HubProjectID != "" }

// How a runner came by its checkout of a hub's project (HubCheckout).
const (
	HubCloned  = "cloned"
	HubAdopted = "adopted"
	HubLegacy  = "legacy"
)

// GuardsCheckout reports whether landing work must wait while the
// project's folder has uncommitted changes to tracked files, not only ones
// the landing would overwrite: a runner's checkout of a hub's project,
// which its owner may be working in by hand (see queue.MergeQueue.land).
func (p Project) GuardsCheckout() bool { return p.LinkedFromHub() }

// LinkedFromHub reports whether p is a runner's checkout of its hub's
// project: the hub linked it (HubProjectID) and the runner placed or
// adopted it in its projects folder (HubCheckout). The hub reaches only
// those, by their HubProjectID.
func (p Project) LinkedFromHub() bool { return p.HubProjectID != "" && p.HubCheckout != "" }

// DashOwnsCheckout reports whether nobody but Dash works in p's folder: a
// runner's checkout of a hub's project that the runner cloned itself
// (HubCloned, HubLegacy). Dash keeps it in step with origin whatever is in
// the way, saving that first (see queue.RemoteSync); an adopted clone and
// anyone's own project only move when nothing local is in the way.
func (p Project) DashOwnsCheckout() bool {
	return p.HubProjectID != "" && (p.HubCheckout == HubCloned || p.HubCheckout == HubLegacy)
}

// LandsOnOrigin reports whether a merge counts only once it is pushed: a
// shared project in "local" mode squashes onto the default branch and
// pushes it, and when origin moved meanwhile merges again on top of it.
// Nobody else would ever see a commit that stayed in one runner's clone.
func (p Project) LandsOnOrigin() bool { return p.RemoteURL != "" && !p.UsesPR() && p.Shared() }

// RunMinutes is the time limit for one Claude run.
func (p Project) RunMinutes() int {
	if p.MaxRunMinutes > 0 {
		return p.MaxRunMinutes
	}
	return DefaultMaxRunMinutes
}

// TurnLimit is the --max-turns for one Claude run.
func (p Project) TurnLimit() int {
	if p.MaxTurns > 0 {
		return p.MaxTurns
	}
	return DefaultMaxTurns
}

// Worktree is the isolated checkout a Todo is executed in.
type Worktree struct {
	Name       string `json:"name"`        // e.g. "todo-3f9a1c"
	Path       string `json:"path"`        // absolute path on disk
	Branch     string `json:"branch"`      // e.g. "dash/todo-3f9a1c"
	BaseCommit string `json:"base_commit"` // BaseBranch HEAD when created
	// BaseBranch is the project's Base when the worktree was made: the
	// branch the todo is compared with and lands on. Empty in worktrees made
	// before branches could be switched (the default branch).
	BaseBranch string `json:"base_branch,omitempty"`
}

// DeletedUser is the CreatedBy of tasks and chat messages whose author's
// account was deleted. It is no user's id, so such a task never runs as
// anyone (a CreatedBy of 0 would act as the env account), and an id the
// database hands to a later account never makes them its author.
const DeletedUser int64 = -1

// Todo is a unit of AI work.
type Todo struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	// CreatedBy is the user who created the task (0: Dash itself, from
	// before users, or made on a runner by its hub; DeletedUser: an
	// account that was deleted since). Its runs act as them.
	CreatedBy int64  `json:"created_by,omitempty"`
	Title     string `json:"title"`
	Prompt    string `json:"prompt"`
	// Priority orders the queue: prioritised todos run first, lower before
	// higher, then every unprioritised (nil) one, oldest first. Ties are
	// broken by CreatedAt. 0 is a real priority, not "unset".
	Priority *int `json:"priority"`
	// DependsOn are ids of other todos in the same project that must be
	// merged before this one starts: its worktree branches from the default
	// branch then, so it builds on their code. Until then it waits queued.
	DependsOn []string `json:"depends_on"`
	// Model is the Claude model the task runs on (claude --model): an alias
	// such as "opus", "sonnet" or "haiku", or a full model id. Empty: Dash's
	// default (-claude-args, else Claude Code's own default).
	Model    string     `json:"model,omitempty"`
	Status   TodoStatus `json:"status"`
	Worktree *Worktree  `json:"worktree,omitempty"`
	// Feedback holds notes for Claude: reviewer rework notes and automatic
	// ones (failing tests, merge conflicts). FeedbackSent counts how many
	// Claude has already seen, so a resumed session only gets the new ones.
	Feedback     []string `json:"feedback,omitempty"`
	FeedbackSent int      `json:"feedback_sent,omitempty"`
	// SessionID is Claude Code's session for this todo; later runs resume it.
	SessionID string  `json:"session_id,omitempty"`
	Checks    *Checks `json:"checks,omitempty"` // latest test run
	AutoFixes int     `json:"auto_fixes,omitempty"`
	// MergeMain asks the next run to merge the default branch into the
	// worktree first (set when a merge conflicted). AutoMerge sends the todo
	// straight back to the merge queue once it is fixed and tests pass,
	// since a human already approved it.
	MergeMain bool `json:"merge_main,omitempty"`
	AutoMerge bool `json:"auto_merge,omitempty"`
	// SentBack is why the merge queue last returned the todo to Claude
	// (a conflict or failing tests), shown on the card while it is fixed.
	SentBack string `json:"sent_back,omitempty"`
	// Paused holds a queued todo back from the dispatcher. Pausing a running
	// todo stops Claude and requeues it paused, keeping its worktree and
	// session so resuming continues where it stopped.
	Paused bool `json:"paused,omitempty"`
	// Reviewing is set while Claude reviews the todo from its chat; its
	// status can't change until the review finishes.
	Reviewing bool `json:"reviewing,omitempty"`
	// Size of the change against the default branch, after the latest run.
	DiffFiles int     `json:"diff_files,omitempty"`
	DiffAdd   int     `json:"diff_add,omitempty"`
	DiffDel   int     `json:"diff_del,omitempty"`
	CostUSD   float64 `json:"cost_usd,omitempty"` // API-equivalent cost reported by Claude Code
	Runs      int     `json:"runs,omitempty"`
	// Summary is one sentence on what Claude did, from its latest run.
	Summary string `json:"summary,omitempty"`
	// Attachments are files (usually screenshots) given to Claude with the task.
	Attachments []Attachment `json:"attachments,omitempty"`
	// Edits are file changes made in the web editor. The first run commits
	// them in the worktree instead of running Claude and sets EditsApplied;
	// later runs (rework, failing tests) are Claude's as usual.
	Edits        []FileEdit `json:"edits,omitempty"`
	EditsApplied bool       `json:"edits_applied,omitempty"`
	// MergedCommit is the commit that landed the todo on the default branch.
	MergedCommit string       `json:"merged_commit,omitempty"`
	PR           *PullRequest `json:"pr,omitempty"`     // in "pr" merge mode
	Revert       *Revert      `json:"revert,omitempty"` // set once a merged todo is reverted
	// RevertConflict lists the files a revert of the todo conflicted in, on
	// a runner, so its hub queues a task for Claude to undo it by hand.
	RevertConflict []string `json:"revert_conflict,omitempty"`
	CIRun          int64    `json:"ci_run,omitempty"` // the failed GitHub Actions run this todo investigates
	Error          string   `json:"error,omitempty"`

	// Runner is the id of the runner a hub sent the todo to (empty: it
	// runs here), and RemoteID the runner's id for its copy. Worktree,
	// summary, checks, PR, diff size, merged commit and revert are then the
	// runner's, as it reports them; Worktree.Path is empty, the files being on the runner.
	Runner   string `json:"runner,omitempty"`
	RemoteID string `json:"remote_id,omitempty"`
	// Waiting is why a queued todo isn't starting, e.g. all of its
	// project's runners are offline.
	Waiting string `json:"waiting,omitempty"`
	// Resumable: a todo that failed on a runner still has its worktree and
	// Claude session there, so queueing it again continues where it was.
	Resumable bool `json:"resumable,omitempty"`
	// HubTodoID is set on a runner for a todo its hub sent: the hub's id
	// for it. HubNotes counts the hub's feedback notes already added.
	HubTodoID string `json:"hub_todo_id,omitempty"`
	HubNotes  int    `json:"hub_notes,omitempty"`

	// ExternalRef is the card standing for the todo in its project's
	// external tracker (nil: none, e.g. a project on Dash's own board). The
	// tracker owns the todo's intent (title, prompt, priority, dependencies,
	// status column); everything else here stays Dash's (internal/tracker).
	ExternalRef *ExternalRef `json:"external_ref,omitempty"`

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// TodoModels are the model aliases offered for a task; any other Claude
// model id (e.g. "claude-sonnet-5-5") is accepted too.
var TodoModels = []string{"opus", "sonnet", "haiku"}

// CleanModel trims and checks a task's model: empty (the default), an alias
// or a model id, one word of letters, digits and ".-_[]:" starting with a
// letter, so it can never pass as a flag of claude.
func CleanModel(m string) (string, error) {
	m = strings.TrimSpace(m)
	if m == "" {
		return "", nil
	}
	ok := len(m) <= 100 && (m[0] >= 'a' && m[0] <= 'z' || m[0] >= 'A' && m[0] <= 'Z')
	for _, r := range m {
		if !ok {
			break
		}
		ok = r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".-_[]:", r)
	}
	if !ok {
		return "", fmt.Errorf("invalid model %q: use an alias (%s) or a model id", m, strings.Join(TodoModels, ", "))
	}
	return m, nil
}

// OnRunner reports whether a hub sent the todo to a runner.
func (t Todo) OnRunner() bool { return t.Runner != "" }

// QueueBefore reports whether a runs before b: prioritised todos first, by
// priority ascending, then oldest first; unprioritised ones after all of
// them, oldest first.
func QueueBefore(a, b Todo) bool {
	switch {
	case a.Priority != nil && b.Priority == nil:
		return true
	case a.Priority == nil && b.Priority != nil:
		return false
	case a.Priority != nil && *a.Priority != *b.Priority:
		return *a.Priority < *b.Priority
	}
	return a.CreatedAt.Before(b.CreatedAt)
}

// Blockers returns the todos among all that t depends on and that aren't
// merged yet, in t.DependsOn order. Ids not in all (deleted) don't block.
func (t Todo) Blockers(all []Todo) []Todo {
	if len(t.DependsOn) == 0 {
		return nil
	}
	return t.BlockersIn(TodosByID(all))
}

// TodosByID indexes todos by id, for BlockersIn over many todos.
func TodosByID(all []Todo) map[string]Todo {
	byID := make(map[string]Todo, len(all))
	for _, x := range all {
		byID[x.ID] = x
	}
	return byID
}

// BlockersIn is Blockers with the todos already indexed by TodosByID.
func (t Todo) BlockersIn(byID map[string]Todo) []Todo {
	if len(t.DependsOn) == 0 {
		return nil
	}
	var out []Todo
	for _, id := range t.DependsOn {
		if d, ok := byID[id]; ok && d.Status != StatusMerged {
			out = append(out, d)
		}
	}
	return out
}

// Checks is the outcome of running the project's test command in a worktree.
type Checks struct {
	Command  string    `json:"command"`
	Passed   bool      `json:"passed"`
	ExitCode int       `json:"exit_code"`
	Output   string    `json:"output"` // tail of combined stdout/stderr
	Stage    string    `json:"stage"`  // "review" (before review) | "merge" (in the merge queue, before landing) | "manual"
	At       time.Time `json:"at"`
	Duration float64   `json:"duration_s"`
	// Commit is the worktree's commit the checks ran on, when it had no
	// uncommitted changes to tracked files ("" otherwise). The merge queue
	// runs the checks again unless they passed on the commit it lands.
	Commit string `json:"commit,omitempty"`
}

// Attachment is an uploaded file attached to a todo or a rework note.
type Attachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	Path string `json:"path"` // absolute path Claude reads it from
}

// FileEdit is a file's new content, saved in the web editor. Blob is the git
// object the editor started from, so a file changed since isn't overwritten.
type FileEdit struct {
	Path    string `json:"path"`
	Blob    string `json:"blob"`
	Content string `json:"content"`
}

// PullRequest is the GitHub PR a todo was landed through.
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"` // OPEN | MERGED | CLOSED
	// HandledSHA is the head commit whose conflicts or failing checks were
	// already sent back to Claude, so the same failure isn't sent twice.
	HandledSHA string `json:"handled_sha,omitempty"`
	// Checks are GitHub's checks on the head, and Status one line on what the
	// merge waits for, both as last seen while Dash followed it.
	Checks []PRCheck `json:"checks,omitempty"`
	Status string    `json:"status,omitempty"`
	// Draft is set while it is a draft: Dash opens a task's pull request as
	// one and marks it ready for review once its checks pass.
	Draft bool `json:"draft,omitempty"`
	// Review is where its review on GitHub stands: PRApproved,
	// PRChangesRequested or PRReviewPending.
	Review string `json:"review,omitempty"`
	// ReviewID is the newest GitHub review whose comments were sent back to
	// Claude, so the same review isn't sent twice.
	ReviewID int64 `json:"review_id,omitempty"`
	// CommentID is the newest comment in its conversation tab that was sent
	// back to Claude, or seen: 1 once followed with none sent yet, 0 for a
	// pull request opened before Dash followed those comments.
	CommentID int64 `json:"comment_id,omitempty"`
	// Waits is what a person has to do on GitHub for it to go on (PRWaitReview,
	// PRWaitMerge, PRWaitBlocked), "" while it waits on Dash, Claude or its
	// checks instead; WaitsSince is when that started. Both as last seen
	// while Dash followed it.
	Waits      string     `json:"waits,omitempty"`
	WaitsSince *time.Time `json:"waits_since,omitempty"`
	// Reply is the answer owed on GitHub to the reviews last sent back to
	// Claude, until it is posted once Claude's fix is pushed (or the fix
	// fails). RepliedID is the newest review answered, so a reply is never
	// posted twice.
	Reply     *PRReply `json:"reply,omitempty"`
	RepliedID int64    `json:"replied_id,omitempty"`
}

// PRReply is what Dash answers on GitHub to reviews sent back to Claude.
type PRReply struct {
	ReviewID int64 `json:"review_id"` // the newest of the reviews sent
	// CommentID is the newest of the conversation comments sent, 0 for none.
	CommentID int64 `json:"comment_id,omitempty"`
	// Reviewers are those who requested changes: asked to review again.
	Reviewers []string `json:"reviewers,omitempty"`
	// Items are what Claude was asked to answer, numbered as in its note.
	Items []PRReplyItem `json:"items,omitempty"`
	// Report is Claude's final message on the fix.
	Report string `json:"report,omitempty"`
}

// PRReplyItem is one review's text or one line comment, as numbered for
// Claude.
type PRReplyItem struct {
	Ref    int    `json:"ref"`
	Author string `json:"author,omitempty"`
	// Thread is the REST ID of the first comment of a line comment's thread
	// (0 for a review's text): replies go there.
	Thread int64  `json:"thread,omitempty"`
	Where  string `json:"where,omitempty"` // file:line of a line comment
}

// Where a pull request's review on GitHub stands (PullRequest.Review).
const (
	PRApproved         = "approved"
	PRChangesRequested = "changes_requested"
	PRReviewPending    = "pending"
)

// What a person has to do for a pull request to go on (PullRequest.Waits).
const (
	PRWaitReview  = "review"  // ready for review, and nobody has approved it yet
	PRWaitMerge   = "merge"   // approved and green, for a person to merge on GitHub
	PRWaitBlocked = "blocked" // a failing check no code change fixes (e.g. an unsigned CLA)
)

// WaitsOnGitHub reports a task whose pull request is open on GitHub: it is
// reviewed, and merged or closed, there rather than approved in Dash.
func (t Todo) WaitsOnGitHub() bool { return t.PR != nil && t.PR.State == "OPEN" }

// PRCheck is one GitHub check on a pull request's head.
type PRCheck struct {
	Name  string `json:"name"`
	State string `json:"state"` // pending | passed | failed
	URL   string `json:"url,omitempty"`
}

// Revert records how a merged todo was undone.
type Revert struct {
	Commit string    `json:"commit,omitempty"` // revert commit on the default branch
	PRURL  string    `json:"pr_url,omitempty"` // revert pull request, in "pr" merge mode
	At     time.Time `json:"at"`
}

// MergeJob is a request to land a reviewed Todo's branch on the default branch.
type MergeJob struct {
	TodoID    string
	ProjectID string
	Revert    bool // undo the merged todo instead
}

// ProcessKind distinguishes how a child process is attached.
type ProcessKind string

const (
	ProcWeb  ProcessKind = "web"  // long-running server, piped output, exposes URL
	ProcPTY  ProcessKind = "pty"  // interactive CLI over xterm.js
	ProcTask ProcessKind = "task" // one-shot command (tests, deploy), piped output
)

// Process is a child process started by Dash on behalf of a project.
type Process struct {
	ID        string      `json:"id"`
	ProjectID string      `json:"project_id"`
	Kind      ProcessKind `json:"kind"`
	Command   string      `json:"command"`
	Dir       string      `json:"dir"`
	PID       int         `json:"pid"`
	URL       string      `json:"url,omitempty"`
	Port      int         `json:"port,omitempty"`    // web servers: their $PORT
	TodoID    string      `json:"todo_id,omitempty"` // set for a todo's live preview
	// Preset is the project's command it runs (run | test | deploy), when
	// started from one rather than with a command of its own.
	Preset string `json:"preset,omitempty"`
	// Main says it runs in the project's own checkout, not a task's
	// worktree. With Preset, it is one of the project's quick actions; a
	// running Main terminal is the project's shell. The dashboard goes by
	// these rather than Dir, which on a hub is the runner's clone.
	Main      bool      `json:"main,omitempty"`
	Running   bool      `json:"running"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// PreviewState is where a todo's live preview is in its life.
type PreviewState string

const (
	PreviewStarting PreviewState = "starting" // server started, its port doesn't answer yet
	PreviewLive     PreviewState = "live"     // the port answers; the proxy serves it
	PreviewCrashed  PreviewState = "crashed"  // the server exited on its own
	PreviewStopped  PreviewState = "stopped"  // stopped (by hand, idle, or the todo left review); only in events
)

// Preview kinds (Project.PreviewKind).
const (
	PreviewWeb      = "web"
	PreviewTerminal = "terminal"
)

// TerminalPreview reports whether p's tasks are previewed in a terminal.
func (p Project) TerminalPreview() bool { return p.PreviewKind == PreviewTerminal }

// Preview is a todo's project run_command running in its worktree, served
// at URL through Dash's reverse proxy (a subdomain per todo); or, for a
// terminal preview (Kind PreviewTerminal), a PTY in the worktree that the UI
// attaches to at /ws/todos/{todo}/preview (no port, no URL).
type Preview struct {
	TodoID    string       `json:"todo_id"`
	ProjectID string       `json:"project_id"`
	ProcessID string       `json:"process_id"`
	Title     string       `json:"title"` // the todo's, for lists of previews
	Kind      string       `json:"kind"`  // PreviewWeb or PreviewTerminal
	Port      int          `json:"port"`
	State     PreviewState `json:"state"`
	// TerminalURL is the WebSocket path (on the Dash serving this preview)
	// that attaches to a terminal preview, as procWS's protocol.
	TerminalURL string `json:"terminal_url,omitempty"`
	// URL is the proxied address, e.g. https://<todo>.preview.example.com/;
	// empty when no preview domain is configured. LocalURL is the server
	// itself, reachable from this computer only. OpenURL is a path on Dash
	// that signs the browser in to the preview and redirects there.
	URL       string    `json:"url,omitempty"`
	LocalURL  string    `json:"local_url,omitempty"`
	OpenURL   string    `json:"open_url,omitempty"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	Reason    string    `json:"reason,omitempty"` // why it stopped (events only)
	StartedAt time.Time `json:"started_at"`
	LastSeen  time.Time `json:"last_seen"` // last proxied request or WebSocket traffic
}

// ChatMessage is one turn of the general chat.
type ChatMessage struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content string `json:"content"`
	// Attachments are files dropped or pasted into a user message.
	Attachments []Attachment `json:"attachments,omitempty"`
	// At is when the message was sent, to place it in a task's timeline.
	At *time.Time `json:"at,omitempty"`
	// CreatedBy is the user who wrote a user message (0 for Claude's
	// answers and messages from before users; DeletedUser once their
	// account is deleted).
	CreatedBy int64 `json:"created_by,omitempty"`
	// Usage is what Claude's answer took (assistant messages only).
	Usage *ChatUsage `json:"usage,omitempty"`
	// Shell is set on a user message that was a shell command the user ran
	// from the chat with a leading "!" (Content is the "!command" as typed):
	// what it printed and how it ended. Claude didn't run it, but sees it on
	// the next turn.
	Shell *ChatShell `json:"shell,omitempty"`
}

// ChatShell is a "!" command run from a chat, and its result.
type ChatShell struct {
	Command string `json:"command"`
	// Dir is where it ran (on Runner, when that's not this Dash).
	Dir    string `json:"dir,omitempty"`
	Runner string `json:"runner,omitempty"`
	// Output is its stdout and stderr together, capped: a long one keeps its
	// start and end (Truncated says so, and the output marks the cut).
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitempty"`
	// ExitCode is its exit status; -1 when it didn't exit on its own (Error
	// says why: timed out, stopped, couldn't start).
	ExitCode   int    `json:"exit_code"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// ChatUsage is the tokens and time one chat answer took.
type ChatUsage struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int64 `json:"cache_creation_tokens,omitempty"`
	// ThinkingMS is the time spent in thinking blocks; DurationMS the whole answer.
	ThinkingMS int64 `json:"thinking_ms,omitempty"`
	DurationMS int64 `json:"duration_ms,omitempty"`
}

// RunEvent is one step of a todo's worker: a Claude run's text, tool calls
// and results, its boundaries, and Dash's own progress notes.
type RunEvent struct {
	// "run_start" | "run_end" | "prompt" (notes sent to Claude) | "text" |
	// "tool_use" | "tool_result" | "result" | "status" (a Dash note)
	Kind    string          `json:"kind"`
	Text    string          `json:"text,omitempty"`
	ID      string          `json:"id,omitempty"` // tool_use id (tool_use, tool_result)
	Tool    string          `json:"tool,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	Turns   int             `json:"turns,omitempty"`    // result
	CostUSD float64         `json:"cost_usd,omitempty"` // result
	At      time.Time       `json:"at"`
}

// EventType names a WebSocket event broadcast to dashboards.
type EventType string

const (
	EvProjectUpdated EventType = "project.updated"
	EvProjectDeleted EventType = "project.deleted"
	EvTodoUpdated    EventType = "todo.updated"
	EvTodoDeleted    EventType = "todo.deleted"
	EvTodoOutput     EventType = "todo.output" // agent progress line
	EvTodoEvent      EventType = "todo.event"  // a structured RunEvent of the worker
	EvGitOp          EventType = "git.op"      // a git/gh command ran
	EvMergeQueue     EventType = "merge.queue" // merge queue depth / current job
	EvProcUpdated    EventType = "process.updated"
	EvPermission     EventType = "permission.updated" // Claude asked to use a tool, or a request was answered
	EvUsage          EventType = "usage.updated"      // subscription rate-limit usage / queue pause state
	EvCI             EventType = "ci.updated"         // GitHub Actions status of a project's default branch
	EvRemote         EventType = "remote.updated"     // how the default branch compares with origin's, after a fetch
	EvChecklist      EventType = "checklist.updated"  // the project's TODO.md changed (on the default branch, or in the checkout when git doesn't track it)
	EvCheckouts      EventType = "checkouts.updated"  // a hub's project: the runners whose checkout is behind origin; Data is []Checkout
	EvRunnerDisk     EventType = "runner.disk"        // a hub's runner's free disk space fell below its threshold, or is back above it; Data is RunnerDisk
	EvPreview        EventType = "preview.updated"    // a todo's live preview started, came up, crashed or stopped
	EvIssueUpdated   EventType = "issue.updated"      // an issue was added or changed; Data is the Issue
	EvIssueDeleted   EventType = "issue.deleted"      // Data is {"id": …}
	EvScanRun        EventType = "scan.run"           // a scan started, progressed or finished; Data is the ScanRun
	EvScanSettings   EventType = "scan.settings"      // a scan's schedule was turned on/off or edited; Data is the ScanSetting
	EvScanTemplate   EventType = "scan.template"      // a Dash scan template was created, edited or deleted; Data is {"name": …, "deleted": bool}
)

// RunnerDisk is a runner's disk space crossing its low-space threshold.
type RunnerDisk struct {
	RunnerID   string `json:"runner_id"`
	RunnerName string `json:"runner_name"`
	OrgID      int64  `json:"-"`
	// Low: it fell below the threshold (false: it is back above it).
	Low bool `json:"low"`
	// Message says how much is left and what takes the most space.
	Message string `json:"message,omitempty"`
	// Projects are the hub's projects linked to the runner: their webhooks
	// are told.
	Projects []string `json:"projects,omitempty"`
}

// Checkout is a runner's main checkout of a hub's project that is behind
// origin: another runner (or anyone) pushed, and the runner couldn't, or
// hasn't yet, fast-forwarded it. A hub keeps the latest of these its runners
// reported; a Dash without runners has at most one per project, its own.
type Checkout struct {
	Runner     string    `json:"runner,omitempty"` // the runner's id ("" = this Dash's own checkout)
	RunnerName string    `json:"runner_name,omitempty"`
	Branch     string    `json:"branch"`
	Behind     int       `json:"behind"` // origin's commits the checkout's branch doesn't have
	Ahead      int       `json:"ahead,omitempty"`
	Stuck      string    `json:"stuck,omitempty"` // why it was left behind ("" = it may still fast-forward)
	Since      time.Time `json:"since,omitzero"`  // when it was first reported behind
	// DashOwned: the runner cloned it and keeps it in sync on its own
	// (Project.DashOwnsCheckout), so there is nothing to pull by hand; Stuck
	// is then a failure (no network, permissions).
	DashOwned bool `json:"dash_owned,omitempty"`
}

// Event is the envelope for everything pushed over /ws/events.
type Event struct {
	Type      EventType `json:"type"`
	ProjectID string    `json:"project_id,omitempty"`
	TodoID    string    `json:"todo_id,omitempty"`
	Data      any       `json:"data,omitempty"`
	Time      time.Time `json:"time"`
	// OrgID is the organization the event belongs to, when the publisher
	// knows it; otherwise the event hub finds it from ProjectID. Events with
	// neither are about Dash as a whole (usage, the merge queue's depth,
	// runners). Never sent to clients.
	OrgID int64 `json:"-"`
}

// ErrInvalidTransition is returned when a status change is not allowed.
type ErrInvalidTransition struct{ From, To TodoStatus }

func (e ErrInvalidTransition) Error() string {
	return fmt.Sprintf("invalid status transition %s → %s", e.From, e.To)
}

// Owner is who a credential acts as: a user, in one org. Both are required.
type Owner struct {
	User int64 `json:"user_id"`
	Org  int64 `json:"org_id"`
}

// Valid reports whether o names both a user and an org.
func (o Owner) Valid() bool { return o.User > 0 && o.Org > 0 }
