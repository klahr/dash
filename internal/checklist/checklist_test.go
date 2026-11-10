package checklist

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// The repository's own TODO.md.
func fixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../TODO.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseRepoTodo(t *testing.T) {
	src := fixture(t)
	d := Parse(src)
	if len(d.Sections) < 3 || d.Sections[0].Title != "TODO" || d.Sections[1].Title != "Next" {
		t.Fatalf("sections: %+v", d.Sections)
	}
	if e := d.Sections[0].Entries; len(e) != 1 || e[0].Text != "Most important first within each section." {
		t.Errorf("intro: %+v", e)
	}
	first := d.Sections[1].Entries[0].Item
	if first == nil || first.Checked || first.Section != "Next" || first.Line != 7 ||
		!strings.HasPrefix(first.Text, "Use Dash for a week") || !strings.HasPrefix(first.More, "Only stand-ins") ||
		strings.Count(first.More, "\n") != 1 {
		t.Fatalf("first item: %+v", first)
	}
	var checked int
	for _, it := range d.Items() {
		if it.Checked {
			checked++
		}
		if it.Ref == "" || it.Text == "" {
			t.Errorf("item %+v", it)
		}
	}
	if checked == 0 {
		t.Error("no checked items")
	}
	// The same text parses to the same refs.
	again := Parse(src).Items()
	for i, it := range d.Items() {
		if again[i].Ref != it.Ref {
			t.Errorf("ref %d: %s then %s", i, it.Ref, again[i].Ref)
		}
	}
}

func TestToggleChangesOneCharacter(t *testing.T) {
	src := fixture(t)
	it := Parse(src).Items()[0]
	res, err := Apply(src, []Op{{Op: "check", Ref: it.Ref, Checked: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != len(src) {
		t.Fatalf("length %d, was %d", len(res.Content), len(src))
	}
	var diff []int
	for i := range src {
		if src[i] != res.Content[i] {
			diff = append(diff, i)
		}
	}
	if len(diff) != 1 || res.Content[diff[0]] != 'x' || src[diff[0]] != ' ' {
		t.Fatalf("changed bytes at %v", diff)
	}
	if res.Message() != "TODO.md: check off "+Short(it.Text, 60) {
		t.Errorf("message %q", res.Message())
	}
	// And back.
	now := Parse(res.Content).Items()[0]
	if !now.Checked || res.Refs[it.Ref] != now.Ref {
		t.Fatalf("after: %+v, refs %v", now, res.Refs)
	}
	back, err := Apply(res.Content, []Op{{Op: "check", Ref: now.Ref}})
	if err != nil || back.Content != src {
		t.Fatalf("unchecking: %v\n%s", err, back.Content)
	}
}

const nested = "# List\r\n" +
	"\r\n" +
	"* [ ] Parent item\r\n" +
	"      that wraps\r\n" +
	"  * [X] Child one\r\n" +
	"  * [ ] Child two\r\n" +
	"        child more\r\n" +
	"* [ ] Sibling\r\n" +
	"\r\n" +
	"Some closing words.\r\n" +
	"\r\n" +
	"## Later\r\n" +
	"\r\n" +
	"- [ ] Last one"

func TestNestingAndContinuation(t *testing.T) {
	d := Parse(nested)
	items := d.Items()
	if len(items) != 5 {
		t.Fatalf("items: %d", len(items))
	}
	p, c1, c2, sib, last := items[0], items[1], items[2], items[3], items[4]
	if p.Text != "Parent item" || p.More != "that wraps" || p.Indent != 0 {
		t.Errorf("parent %+v", p)
	}
	if !c1.Checked || c1.Indent != 2 || c2.More != "child more" || sib.Indent != 0 || last.Section != "Later" {
		t.Errorf("children %+v %+v %+v %+v", c1, c2, sib, last)
	}
	if e := d.Sections[0].Entries; e[len(e)-1].Text != "Some closing words." {
		t.Errorf("text: %+v", e)
	}

	// Checking a child keeps every other byte, CRLF included.
	res, err := Apply(nested, []Op{{Op: "check", Ref: c2.Ref, Checked: true}, {Op: "check", Ref: c1.Ref}})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(strings.Replace(nested, "[X] Child one", "[ ] Child one", 1), "[ ] Child two", "[x] Child two", 1)
	if res.Content != want {
		t.Fatalf("got %q", res.Content)
	}

	// Deleting the parent takes its continuation and nested items.
	res, err = Apply(nested, []Op{{Op: "delete", Ref: p.Ref}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "# List\r\n\r\n* [ ] Sibling\r\n\r\nSome closing words.\r\n\r\n## Later\r\n\r\n- [ ] Last one"; res.Content != want {
		t.Fatalf("got %q", res.Content)
	}
	if res.Refs[sib.Ref] == "" || res.Refs[sib.Ref] == sib.Ref || res.Refs[p.Ref] != "" {
		t.Errorf("refs %v", res.Refs)
	}

	// Adding to the end of a section and of the file, with CRLF and no
	// final newline kept.
	res, err = Apply(nested, []Op{{Op: "add", Text: "New one\nwith more", Section: "list"}, {Op: "add", Text: "At the end"}})
	if err != nil {
		t.Fatal(err)
	}
	want = strings.Replace(nested, "* [ ] Sibling\r\n", "* [ ] Sibling\r\n* [ ] New one\r\n      with more\r\n", 1) + "\r\n* [ ] At the end"
	if res.Content != want {
		t.Fatalf("got %q\nwant %q", res.Content, want)
	}

	// Editing the text keeps the checkbox and the continuation unless it changes.
	res, err = Apply(nested, []Op{{Op: "edit", Ref: p.Ref, Text: "Parent, renamed"}})
	if err != nil || res.Content != strings.Replace(nested, "Parent item", "Parent, renamed", 1) {
		t.Fatalf("edit: %v %q", err, res.Content)
	}
	more := "wraps differently"
	res, err = Apply(nested, []Op{{Op: "edit", Ref: p.Ref, Text: "Parent item", More: &more}})
	if err != nil || res.Content != strings.Replace(nested, "that wraps", "wraps differently", 1) {
		t.Fatalf("edit more: %v %q", err, res.Content)
	}
}

func TestStaleRefs(t *testing.T) {
	d := Parse(nested)
	sib := d.Items()[3]
	// The file changed above the item: its line moved.
	moved := strings.Replace(nested, "# List\r\n", "# List\r\nIntro\r\n", 1)
	if _, err := Apply(moved, []Op{{Op: "check", Ref: sib.Ref, Checked: true}}); !errors.Is(err, ErrStale) {
		t.Errorf("moved line: %v", err)
	}
	// The item's text changed.
	changed := strings.Replace(nested, "Sibling", "Sibling!", 1)
	if _, err := Apply(changed, []Op{{Op: "delete", Ref: sib.Ref}}); !errors.Is(err, ErrStale) {
		t.Errorf("changed text: %v", err)
	}
	// Removed in the same batch.
	if _, err := Apply(nested, []Op{{Op: "delete", Ref: sib.Ref}, {Op: "check", Ref: sib.Ref}}); !errors.Is(err, ErrStale) {
		t.Errorf("deleted twice: %v", err)
	}
	if _, err := Apply(nested, []Op{{Op: "check", Ref: "nonsense"}}); err == nil || errors.Is(err, ErrStale) {
		t.Errorf("bad ref: %v", err)
	}
}

func TestNewFileAndSections(t *testing.T) {
	res, err := Apply("", []Op{{Op: "add", Text: "First"}})
	if err != nil || res.Content != "# TODO\n\n- [ ] First\n" {
		t.Fatalf("new file: %v %q", err, res.Content)
	}
	res, err = Apply("", []Op{{Op: "add", Text: "A", Section: "Ideas"}, {Op: "add", Text: "B", Section: "Ideas"}})
	if err != nil || res.Content != "# TODO\n\n## Ideas\n\n- [ ] A\n- [ ] B\n" {
		t.Fatalf("new section in a new file: %v %q", err, res.Content)
	}
	src := "# TODO\n\n## Next\n\n- [ ] One\n"
	res, err = Apply(src, []Op{{Op: "add", Text: "Two", Section: "Later"}})
	if err != nil || res.Content != src+"\n## Later\n\n- [ ] Two\n" {
		t.Fatalf("new section: %v %q", err, res.Content)
	}
	// An empty section gets a blank line after its heading.
	src = "# TODO\n\n## Empty\n\n## Full\n\n- [ ] One\n"
	res, err = Apply(src, []Op{{Op: "add", Text: "Two", Section: "Empty"}})
	if err != nil || res.Content != "# TODO\n\n## Empty\n\n- [ ] Two\n\n## Full\n\n- [ ] One\n" {
		t.Fatalf("empty section: %v %q", err, res.Content)
	}
	// Code blocks are text, even when they look like items.
	d := Parse("```\n- [ ] not an item\n```\n- [ ] item\n")
	if n := len(d.Items()); n != 1 {
		t.Errorf("items in a code block: %d", n)
	}
}

func TestPrompt(t *testing.T) {
	d := Parse(nested)
	p := d.Items()[0]
	got := Prompt(nested, *p)
	for _, want := range []string{"Parent item\nthat wraps", "  * [X] Child one", "child more", `"List" section`} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
}
