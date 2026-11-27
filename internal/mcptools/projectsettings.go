package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/klahr/dash/internal/model"
)

// UpdateProjectTool changes a project's settings. In the chat it asks the
// user every time, itself (SelfAskTools): several settings run code
// (test_command, setup_command, run_command, the deploy commands) or change
// what Claude may do in every task (allowed_tools, task_instructions), and
// the chat reads untrusted text.
const UpdateProjectTool = "update_project"

// SelfAskTools are board tools that ask the user themselves, through the
// chat's approver (Options.Ask), on every call: no allow list, permission
// mode or "always allow" skips them. Claude Code is told to allow them, so
// the user isn't asked twice.
var SelfAskTools = []string{UpdateProjectTool}

// Approver asks the user whether a tool call may run, with input as the
// approval card shows it, and waits for the answer: allow, or why not.
type Approver func(ctx context.Context, tool string, input json.RawMessage) (allow bool, message string)

// SettingsBoard is a Board that can change a project's settings, for the
// chat's get_project and update_project.
type SettingsBoard interface {
	// Project finds a project by id or name, as it is now.
	Project(ref string) (model.Project, error)
	// CheckProjectUpdate is nil when the caller may make changes
	// (ProjectSettings) to project ref, as UpdateProject checks them.
	CheckProjectUpdate(ref string, changes map[string]any) error
	// UpdateProject changes a project's settings (ProjectSettings).
	UpdateProject(ref string, changes map[string]any) (model.Project, error)
}

// SettingChange is one setting update_project changes, as the approval
// card shows it.
type SettingChange struct {
	Setting string `json:"setting"`
	Before  any    `json:"before"`
	After   any    `json:"after"`
}

// UpdateProjectInput is the approval card's input for update_project.
type UpdateProjectInput struct {
	Project string          `json:"project"`
	Changes []SettingChange `json:"changes"`
}

// settingValues are p's settings by their ProjectSettings names.
func settingValues(p model.Project) map[string]any {
	return map[string]any{
		"name": p.Name, "run_command": p.RunCommand, "test_command": p.TestCommand, "test_timeout_minutes": p.TestTimeoutMinutes, "environments": p.Environments,
		"preview_kind": p.PreviewKind, "preview_command": p.PreviewCommand, "setup_command": p.SetupCommand,
		"copy_files": p.CopyFiles, "allowed_tools": p.AllowedTools, "chat_permission_mode": p.ChatPermissionMode,
		"review_prompt": p.ReviewPrompt, "task_instructions": p.TaskInstructions, "max_auto_fixes": p.MaxAutoFixes,
		"auto_push": p.AutoPush, "auto_approve": p.AutoApprove, "auto_approve_rules": p.Rules(), "merge_mode": p.MergeMode, "investigate_ci": p.InvestigatesCI(),
		"commit_previews": p.CommitPreviewsOn(),
		"tag_format":      p.TagFormat, "max_run_minutes": p.MaxRunMinutes, "max_turns": p.MaxTurns, "max_parallel": p.MaxParallel,
		"color": p.Color, "color_buttons": p.UsesColorForButtons(),
	}
}

// plain is v as JSON decodes it (strings, float64s, []any, map[string]any),
// with an empty list for a nil one, so values compare and show alike.
func plain(v any) any {
	b, err := json.Marshal(v)
	if err != nil || bytes.Equal(b, []byte("null")) {
		if _, list := v.([]string); list {
			return []any{}
		}
		if _, list := v.([]model.Environment); list {
			return []any{}
		}
		return v
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return v
	}
	return out
}

// SettingChanges are the settings changes would change on p, with their
// values before and after, in ProjectSettings' order. Settings given their
// current value are left out.
func SettingChanges(p model.Project, changes map[string]any) []SettingChange {
	cur := settingValues(p)
	out := []SettingChange{}
	for _, s := range ProjectSettings {
		after, ok := changes[s.Name]
		if !ok {
			continue
		}
		before, after := plain(cur[s.Name]), plain(after)
		b1, _ := json.Marshal(before)
		b2, _ := json.Marshal(after)
		if bytes.Equal(b1, b2) {
			continue
		}
		out = append(out, SettingChange{Setting: s.Name, Before: before, After: after})
	}
	return out
}

// settingArgs are the ProjectSettings in a.
func settingArgs(a Args) map[string]any {
	changes := map[string]any{}
	for _, p := range ProjectSettings {
		if v, ok := a[p.Name]; ok {
			changes[p.Name] = v
		}
	}
	return changes
}

// errNoApprover is update_project's answer where nobody can approve it.
var errNoApprover = errors.New("update_project needs the user's approval, and this chat can't ask for it: change the settings in Project settings")

// ChatProjectTools returns the chat's get_project and update_project.
// update_project asks the user with o.Ask before every change, showing each
// changed setting before and after; without o.Ask it refuses.
func ChatProjectTools(b SettingsBoard, o Options) []Tool {
	pProject := Param{"project", "string", "Project name or id (see list_projects).", true}
	if o.Project != "" {
		pProject = Param{"project", "string", "Project name or id; defaults to this one.", false}
	}
	project := func(a Args) (model.Project, error) {
		ref := a.Str("project")
		if ref == "" {
			ref = o.Project
		}
		return b.Project(ref)
	}
	return []Tool{
		{"get_project", "Show a project's settings.", []Param{pProject},
			func(a Args) (any, error) { return project(a) }},
		{UpdateProjectTool, "Change a project's settings; only the given ones change. The user approves each change in the chat first, " +
			"seeing every setting before and after; if they deny it, nothing changes: don't retry, ask what to do instead. Returns the project afterwards.",
			append([]Param{pProject}, ProjectSettings...),
			func(a Args) (any, error) {
				p, err := project(a)
				if err != nil {
					return nil, err
				}
				changes := settingArgs(a)
				if len(changes) == 0 {
					return nil, errors.New("no settings to change")
				}
				diff := SettingChanges(p, changes)
				if len(diff) == 0 {
					return fmt.Sprintf("Nothing to change: %s has these settings already.", p.Name), nil
				}
				if err := b.CheckProjectUpdate(p.ID, changes); err != nil {
					return nil, err
				}
				if o.Ask == nil {
					return nil, errNoApprover
				}
				input, err := json.Marshal(UpdateProjectInput{Project: p.Name, Changes: diff})
				if err != nil {
					return nil, err
				}
				if ok, msg := o.Ask(context.Background(), ToolName(ServerName, UpdateProjectTool), input); !ok {
					if msg == "" {
						msg = "The user denied this change."
					}
					return nil, fmt.Errorf("%s The project's settings are unchanged", msg)
				}
				// Only what the user saw changes.
				approved := map[string]any{}
				for _, c := range diff {
					approved[c.Setting] = changes[c.Setting]
				}
				return b.UpdateProject(p.ID, approved)
			}},
	}
}

// Combiner is a Board that combines drafts, for combine_todos.
type Combiner interface {
	// CombineTodos combines drafts of one project into the first, like the
	// board's Combine: their issues stay linked to it.
	CombineTodos(ids []string) (model.Todo, error)
}

// CombineTool returns combine_todos.
func CombineTool(c Combiner) Tool {
	return Tool{"combine_todos", "Combine two or more draft tasks of one project into one, like Combine on the board: " +
		"the first keeps its id and gets the others' prompts, tags, dependencies and reported problems (scan findings stay linked to it); " +
		"the others are removed. Use it to group drafts, instead of copying prompts and deleting drafts. Returns the combined task.",
		[]Param{{"todos", "array", "Ids of the drafts to combine, the one to keep first.", true}},
		func(a Args) (any, error) {
			ids := a.List("todos")
			if len(ids) < 2 {
				return nil, errors.New("give at least two draft ids")
			}
			return c.CombineTodos(ids)
		}}
}
