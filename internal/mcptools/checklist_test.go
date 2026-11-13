package mcptools

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/checklist"
	"github.com/klahr/dash/internal/model"
)

// listBoard keeps one project's TODO.md in memory; the board methods the
// checklist tools don't use panic (nil Board).
type listBoard struct {
	Board
	content string
	commits []string
	tasks   []model.Todo
	failRm  bool // removing an item fails, as when TODO.md changed meanwhile
}

func (b *listBoard) Project(ref string) (model.Project, error) {
	if ref != "web" {
		return model.Project{}, fmt.Errorf("no project %q", ref)
	}
	return model.Project{ID: "web", Name: "web"}, nil
}

func (b *listBoard) ReadChecklist(string) (Checklist, error) {
	return Checklist{Path: checklist.Name, Exists: b.content != "", Branch: "main", Version: fmt.Sprint(len(b.commits)), Via: "commit",
		Sections: checklist.Parse(b.content).Sections, Content: b.content}, nil
}

func (b *listBoard) ChangeChecklist(_ string, c ChecklistChange) (ChecklistSaved, error) {
	if b.failRm {
		return ChecklistSaved{}, fmt.Errorf("conflict: %w", checklist.ErrStale)
	}
	res, err := checklist.Apply(b.content, c.Ops)
	if err != nil {
		return ChecklistSaved{}, err
	}
	b.content = res.Content
	msg := c.Message
	if msg == "" {
		msg = res.Message()
	}
	b.commits = append(b.commits, msg)
	cl, _ := b.ReadChecklist("")
	return ChecklistSaved{Checklist: cl, Refs: res.Refs, Commit: fmt.Sprintf("c%d", len(b.commits))}, nil
}

func (b *listBoard) MoveChecklistItem(_ string, m ChecklistMove) (ChecklistMoved, error) {
	it, err := checklist.Parse(b.content).Item(m.Ref)
	if err != nil {
		return ChecklistMoved{}, err
	}
	st := model.StatusDraft
	if m.Queue {
		st = model.StatusQueued
	}
	t := model.Todo{ID: fmt.Sprintf("t%d", len(b.tasks)+1), Title: it.Text, Prompt: checklist.Prompt(b.content, *it), Status: st, Priority: m.Priority}
	b.tasks = append(b.tasks, t)
	s, err := b.ChangeChecklist("", ChecklistChange{Ops: []checklist.Op{{Op: "delete", Ref: m.Ref}}, Message: "TODO.md: move " + it.Text + " to task " + t.ID})
	if err != nil {
		return ChecklistMoved{Task: t, Error: err.Error()}, nil
	}
	return ChecklistMoved{Task: t, Removed: true, Saved: &s}, nil
}

func call(t *testing.T, tools []Tool, name string, args string) (string, error) {
	t.Helper()
	for _, tl := range tools {
		if tl.Name == name {
			var a Args
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				t.Fatal(err)
			}
			res, err := tl.Run(a)
			if err != nil {
				return "", err
			}
			if s, ok := res.(string); ok {
				return s, nil
			}
			b, _ := json.Marshal(res)
			return string(b), nil
		}
	}
	t.Fatalf("no tool %s", name)
	return "", nil
}

// checklistToolNames are the names of the checklist tools, taken from their
// definitions; read_checklist, which only reads, comes first.
func checklistToolNames(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, tl := range checklistTools(&listBoard{}, Options{}, func(Args) (model.Project, error) { return model.Project{}, nil }, Param{}) {
		names = append(names, tl.Name)
	}
	if len(names) == 0 || names[0] != "read_checklist" {
		t.Fatalf("checklist tools: %v", names)
	}
	return names
}

const todoMD = "# TODO\n\nMost important first.\n\n## Next\n\n- [ ] Ship it\n      soon\n- [x] Write it\n"

func TestChecklistTools(t *testing.T) {
	b := &listBoard{content: todoMD}
	tools := Tools(b, Options{Project: "web", Draft: true})
	for _, name := range checklistToolNames(t) {
		if !slices.Contains(BoardTools, name) {
			t.Errorf("%s isn't in BoardTools", name)
		}
	}

	out, err := call(t, tools, "read_checklist", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var read struct {
		Version  string
		Sections []struct {
			Title string
			Items []struct {
				Ref     string
				Checked bool
				Text    string
				More    string
			}
			Notes []string
		}
	}
	if err := json.Unmarshal([]byte(out), &read); err != nil {
		t.Fatal(err)
	}
	if len(read.Sections) != 2 || read.Sections[0].Notes[0] != "Most important first." || len(read.Sections[1].Items) != 2 {
		t.Fatalf("read: %s", out)
	}
	ship := read.Sections[1].Items[0]
	if ship.Text != "Ship it" || ship.More != "soon" || ship.Checked {
		t.Fatalf("item: %+v", ship)
	}

	if _, err := call(t, tools, "check_checklist_item", `{"ref":"`+ship.Ref+`"}`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.content, "- [x] Ship it\n      soon\n") || b.commits[0] != "TODO.md: check off Ship it" {
		t.Fatalf("checked: %q %q", b.content, b.commits)
	}
	// The ref from before the change is stale now: the error says to read again.
	if _, err := call(t, tools, "check_checklist_item", `{"ref":"`+ship.Ref+`","checked":false}`); err == nil || !strings.Contains(err.Error(), "read_checklist again") {
		t.Fatalf("stale ref: %v", err)
	}

	out, err = call(t, tools, "add_checklist_item", `{"text":"Docs","section":"Next"}`)
	if err != nil || !strings.Contains(b.content, "- [x] Write it\n- [ ] Docs\n") {
		t.Fatalf("add: %v %q", err, b.content)
	}
	var added struct{ Ref string }
	_ = json.Unmarshal([]byte(out), &added)
	if it, err := checklist.Parse(b.content).Item(added.Ref); err != nil || it.Text != "Docs" {
		t.Fatalf("added ref %q: %v", added.Ref, err)
	}

	if _, err := call(t, tools, "edit_checklist_item", `{"ref":"`+added.Ref+`","text":"Write docs"}`); err != nil || !strings.Contains(b.content, "- [ ] Write docs\n") {
		t.Fatalf("edit: %v %q", err, b.content)
	}
	items := checklist.Parse(b.content).Items()
	if _, err := call(t, tools, "delete_checklist_item", `{"ref":"`+items[1].Ref+`"}`); err != nil || strings.Contains(b.content, "Write it") {
		t.Fatalf("delete: %v %q", err, b.content)
	}

	// Moving makes a draft unless asked to queue it.
	items = checklist.Parse(b.content).Items()
	out, err = call(t, tools, "move_checklist_item_to_task", `{"ref":"`+items[1].Ref+`"}`)
	if err != nil || !strings.Contains(out, "removed the item") || b.tasks[0].Status != model.StatusDraft || strings.Contains(b.content, "Write docs") {
		t.Fatalf("move: %v %s %q", err, out, b.content)
	}
	items = checklist.Parse(b.content).Items()
	if _, err := call(t, tools, "move_checklist_item_to_task", `{"ref":"`+items[0].Ref+`","queue":true,"priority":2}`); err != nil {
		t.Fatal(err)
	}
	if tk := b.tasks[1]; tk.Status != model.StatusQueued || *tk.Priority != 2 || tk.Title != "Ship it" || !strings.Contains(tk.Prompt, "Ship it\nsoon") {
		t.Fatalf("queued task: %+v", tk)
	}

	// When the item can't be removed, the task stays and the answer says so.
	b.content = todoMD
	b.failRm = true
	ref := checklist.Parse(b.content).Items()[0].Ref
	out, err = call(t, tools, "move_checklist_item_to_task", `{"ref":"`+ref+`"}`)
	if err != nil || !strings.Contains(out, "still in TODO.md") || len(b.tasks) != 3 {
		t.Fatalf("failed removal: %v %s", err, out)
	}
	if !strings.Contains(moveDescOf(tools), "queue only when the user asks") {
		t.Error("move's description doesn't say when to queue")
	}
}

func moveDescOf(tools []Tool) string {
	for _, tl := range tools {
		if tl.Name == "move_checklist_item_to_task" {
			return tl.Desc
		}
	}
	return ""
}

func TestChecklistToolsReadOnly(t *testing.T) {
	b := &listBoard{content: todoMD}
	errRO := errors.New("read-only")
	for name, tools := range map[string][]Tool{
		"board": ReadOnly(Tools(b, Options{Project: "web"}), ReadTools, errRO),
		"fleet": ReadOnly(FleetTools(Fleet{Computers: func() ([]Machine, error) { return nil, nil }}), FleetReadTools, errRO),
	} {
		ref := checklist.Parse(b.content).Items()[0].Ref
		for _, tool := range checklistToolNames(t)[1:] {
			if _, err := call(t, tools, tool, `{"project":"web","ref":"`+ref+`","text":"x"}`); !errors.Is(err, errRO) {
				t.Errorf("%s %s: %v", name, tool, err)
			}
		}
		if name == "board" {
			if _, err := call(t, tools, "read_checklist", `{}`); err != nil {
				t.Errorf("read: %v", err)
			}
		}
	}
	if b.content != todoMD {
		t.Errorf("changed: %q", b.content)
	}
}

func TestFleetChecklistOverREST(t *testing.T) {
	api := &checklistAPI{}
	f := Fleet{API: api}
	tools := FleetTools(f)
	if _, err := call(t, tools, "read_checklist", `{"project":"web"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tools, "check_checklist_item", `{"project":"web","ref":"7:abc"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tools, "move_checklist_item_to_task", `{"project":"web","ref":"7:abc","queue":true}`); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`GET /api/projects/web/checklist null`,
		`POST /api/projects/web/checklist {"ops":[{"op":"check","ref":"7:abc","checked":true}]}`,
		`POST /api/projects/web/checklist/move {"ref":"7:abc","queue":true}`,
	}
	if !slices.Equal(api.sent, want) {
		t.Errorf("sent\n%s\nwant\n%s", strings.Join(api.sent, "\n"), strings.Join(want, "\n"))
	}
}

// checklistAPI is one computer with project "web".
type checklistAPI struct{ sent []string }

func (a *checklistAPI) Call(method, path string, body, out any) error {
	switch {
	case path == "/api/machines":
		*out.(*[]machine) = []machine{{Name: "here", OK: true}}
	case path == "/api/projects":
		*out.(*[]model.Project) = []model.Project{{ID: "web", Name: "web"}}
	default:
		b, _ := json.Marshal(body)
		a.sent = append(a.sent, method+" "+path+" "+string(b))
		if m, ok := out.(*ChecklistMoved); ok {
			m.Removed = true
		}
	}
	return nil
}
