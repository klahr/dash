package mcptools

import (
	"errors"
	"fmt"
	"strings"

	"github.com/klahr/dash/internal/checklist"
	"github.com/klahr/dash/internal/model"
)

// Checklist is a project's TODO.md (at the repository root) as a task list:
// what GET /api/projects/{id}/checklist answers.
type Checklist struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Local   bool   `json:"local,omitempty"` // a file git doesn't track, in the checkout
	Branch  string `json:"branch"`
	Version string `json:"version"` // its blob on Branch, or the hash of the local file ("" when it doesn't exist)
	// Via is how changes are saved: "commit" (straight onto Branch), "local"
	// (written in the checkout) or "task" (a task commits them: the project
	// lands changes through pull requests).
	Via      string              `json:"via"`
	Sections []checklist.Section `json:"sections"`
	Content  string              `json:"content"`
}

// ChecklistChange is a batch of changes, all made to the file at Version
// (when set; else the refs alone must still match).
type ChecklistChange struct {
	Version *string        `json:"version,omitempty"`
	Ops     []checklist.Op `json:"ops"`
	Message string         `json:"message,omitempty"` // the commit message (default: from the ops)
}

// ChecklistSaved is the checklist after a change.
type ChecklistSaved struct {
	Checklist Checklist `json:"checklist"`
	// Refs maps each item's reference before the change to its new one.
	Refs   map[string]string `json:"refs"`
	Commit string            `json:"commit,omitempty"`
	// Task is the task that commits the change, when Via is "task".
	Task *model.Todo `json:"task,omitempty"`
	Note string      `json:"note,omitempty"`
}

// ChecklistMove makes a task of an item.
type ChecklistMove struct {
	Ref       string   `json:"ref"`
	Version   *string  `json:"version,omitempty"`
	Queue     bool     `json:"queue"`
	Priority  *int     `json:"priority,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`
}

// ChecklistMoved is the task made of an item, and whether the item left
// TODO.md: when removing it failed the task stays and Error says why.
type ChecklistMoved struct {
	Task    model.Todo      `json:"task"`
	Removed bool            `json:"removed"`
	Error   string          `json:"error,omitempty"`
	Saved   *ChecklistSaved `json:"saved,omitempty"`
}

// ChecklistToolNames are the checklist tools; read_checklist only reads.
var ChecklistToolNames = []string{"read_checklist", "add_checklist_item", "check_checklist_item", "edit_checklist_item", "delete_checklist_item", "reorder_checklist_item", "move_checklist_item_to_task"}

// stale adds what to do about a checklist that changed.
func stale(err error) error {
	if err != nil && (errors.Is(err, checklist.ErrStale) || strings.Contains(err.Error(), "since it was read") || strings.Contains(err.Error(), "changed on")) {
		return fmt.Errorf("%w (call read_checklist again and use its new refs)", err)
	}
	return err
}

func checklistTools(b Board, o Options, project func(Args) (model.Project, error), pProject Param) []Tool {
	pRef := Param{"ref", "string", "The item's ref from read_checklist (its line and a hash of its text; a stale ref fails rather than change the wrong line).", true}
	change := func(a Args, op checklist.Op) (any, error) {
		p, err := project(a)
		if err != nil {
			return nil, err
		}
		s, err := b.ChangeChecklist(p.ID, ChecklistChange{Ops: []checklist.Op{op}})
		if err != nil {
			return nil, stale(err)
		}
		out := map[string]any{"version": s.Checklist.Version, "via": s.Checklist.Via}
		if s.Commit != "" {
			out["commit"] = s.Commit
		}
		if s.Task != nil {
			out["task"] = s.Task.ID
		}
		if s.Note != "" {
			out["note"] = s.Note
		}
		if op.Op == "add" {
			old := map[string]bool{}
			for _, r := range s.Refs {
				old[r] = true
			}
			for _, it := range (checklist.Doc{Sections: s.Checklist.Sections}).Items() {
				if !old[it.Ref] {
					out["ref"] = it.Ref
				}
			}
		} else if ref, ok := s.Refs[op.Ref]; ok {
			out["ref"] = ref
		}
		return out, nil
	}
	moveDesc := "Move an item of the checklist (TODO.md) onto the board as a task, in one step: the task gets the item's text as its title " +
		"and the text with its continuation lines (and the section it was in) as its prompt, and the item leaves TODO.md. " +
		"It stays a draft unless queue is true; queue only when the user asks for it to run."
	return []Tool{
		{"read_checklist", "Read the project's checklist: TODO.md at the repository root, as on the default branch (or the checkout's copy when git doesn't track it). " +
			"Returns its sections, each item's ref, checked state, text and indentation, the section's other text, and the file version. " +
			"This is the user's to-do list in the repository, not the task board.", []Param{pProject},
			func(a Args) (any, error) {
				p, err := project(a)
				if err != nil {
					return nil, err
				}
				c, err := b.ReadChecklist(p.ID)
				if err != nil {
					return nil, err
				}
				type item struct {
					Ref     string `json:"ref"`
					Checked bool   `json:"checked"`
					Text    string `json:"text"`
					More    string `json:"more,omitempty"`
					Indent  int    `json:"indent,omitempty"`
				}
				type section struct {
					Title string   `json:"title"`
					Items []item   `json:"items"`
					Notes []string `json:"notes,omitempty"`
				}
				out := struct {
					Path     string    `json:"path"`
					Exists   bool      `json:"exists"`
					Branch   string    `json:"branch"`
					Version  string    `json:"version"`
					Via      string    `json:"via"`
					Sections []section `json:"sections"`
				}{c.Path, c.Exists, c.Branch, c.Version, c.Via, []section{}}
				for _, s := range c.Sections {
					sec := section{Title: s.Title, Items: []item{}}
					for _, e := range s.Entries {
						if e.Item != nil {
							sec.Items = append(sec.Items, item{e.Item.Ref, e.Item.Checked, e.Item.Text, e.Item.More, e.Item.Indent})
						} else {
							sec.Notes = append(sec.Notes, e.Text)
						}
					}
					out.Sections = append(out.Sections, sec)
				}
				return out, nil
			}},
		{"add_checklist_item", "Add an item to the checklist (TODO.md), at the end of a section or of the file; creates TODO.md if there is none. " +
			"Use it when the user asks to add something to the to-do list or TODO.md; that is not a board task (create_todo).", []Param{pProject,
			{"text", "string", "The item's text; more lines become its continuation lines.", true},
			{"section", "string", "Title of the section to add it to (a new section is made at the end when there is none by that name); default: the end of the file.", false}},
			func(a Args) (any, error) {
				if a.Str("text") == "" {
					return nil, errors.New("text is required")
				}
				return change(a, checklist.Op{Op: "add", Text: a.Str("text"), Section: a.Str("section")})
			}},
		{"check_checklist_item", "Check off (or uncheck) an item of the checklist (TODO.md).", []Param{pProject, pRef,
			{"checked", "boolean", "true checks it off, false unchecks it (default true).", false}},
			func(a Args) (any, error) {
				if a.Str("ref") == "" {
					return nil, errors.New("ref is required")
				}
				return change(a, checklist.Op{Op: "check", Ref: a.Str("ref"), Checked: a.FlagOr("checked", true)})
			}},
		{"edit_checklist_item", "Change the text of an item of the checklist (TODO.md); its checkbox stays as it is.", []Param{pProject, pRef,
			{"text", "string", "The new text of the item's first line.", true},
			{"more", "string", "Replaces its continuation lines (\"\" removes them); leave it out to keep them.", false}},
			func(a Args) (any, error) {
				if a.Str("ref") == "" || a.Str("text") == "" {
					return nil, errors.New("ref and text are required")
				}
				return change(a, checklist.Op{Op: "edit", Ref: a.Str("ref"), Text: a.Str("text"), More: a.OptStr("more")})
			}},
		{"delete_checklist_item", "Delete an item of the checklist (TODO.md), with its continuation lines and the items nested under it; only when the user asks.", []Param{pProject, pRef},
			func(a Args) (any, error) {
				if a.Str("ref") == "" {
					return nil, errors.New("ref is required")
				}
				return change(a, checklist.Op{Op: "delete", Ref: a.Str("ref")})
			}},
		{"reorder_checklist_item", "Move an item of the checklist (TODO.md) to another place in it, with its continuation lines and nested items, " +
			"to reprioritise it: just before or just after another item (taking that item's indentation), or else to the end of a section.", []Param{pProject, pRef,
			{"before", "string", "Ref of the item to put it just before.", false},
			{"after", "string", "Ref of the item to put it just after (after that item's nested items).", false},
			{"section", "string", "Title of the section to put it at the end of, when neither before nor after is given; default: the last section.", false}},
			func(a Args) (any, error) {
				if a.Str("ref") == "" {
					return nil, errors.New("ref is required")
				}
				if a.Str("before") != "" && a.Str("after") != "" {
					return nil, errors.New("give before or after, not both")
				}
				return change(a, checklist.Op{Op: "reorder", Ref: a.Str("ref"), Before: a.Str("before"), After: a.Str("after"), Section: a.Str("section")})
			}},
		{"move_checklist_item_to_task", moveDesc, []Param{pProject, pRef,
			{"queue", "boolean", "Queue the task to run instead of keeping a draft; only when the user asks for it to run (default false).", false},
			{"priority", "integer", priorityDesc + " Omit it to leave the task unprioritised.", false},
			{"depends_on", "array", "Ids of tasks in the same project that must be merged before this one starts.", false}},
			func(a Args) (any, error) {
				if a.Str("ref") == "" {
					return nil, errors.New("ref is required")
				}
				p, err := project(a)
				if err != nil {
					return nil, err
				}
				m, err := b.MoveChecklistItem(p.ID, ChecklistMove{Ref: a.Str("ref"), Queue: a.Flag("queue"), Priority: a.OptNum("priority"), DependsOn: a.List("depends_on")})
				if err != nil {
					return nil, stale(err)
				}
				if !m.Removed {
					return fmt.Sprintf("Created task %s (%s, %s), but the item is still in TODO.md: %s. Don't create the task again; "+
						"read_checklist and delete the item when the user wants it gone.", m.Task.ID, m.Task.Title, m.Task.Status, m.Error), nil
				}
				msg := fmt.Sprintf("Created task %s (%s, %s) and removed the item from TODO.md.", m.Task.ID, m.Task.Title, m.Task.Status)
				if m.Saved != nil && m.Saved.Note != "" {
					msg += " " + m.Saved.Note
				}
				return msg, nil
			}},
	}
}
