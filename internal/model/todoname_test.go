package model

import "testing"

func TestTodoName(t *testing.T) {
	for _, c := range []struct {
		t    Todo
		n    int
		want string
	}{
		{Todo{Title: "Set title", Prompt: "# Ignored"}, 10, "Set title"},
		{Todo{Title: "  ", Prompt: "Fix it"}, 10, "Fix it"},
		{Todo{Prompt: "\n\n# Faster   search\nbody"}, 20, "Faster search"},
		{Todo{Prompt: "- list item"}, 20, "list item"},
		{Todo{Prompt: "make the search much faster"}, 12, "make the se…"},
		{Todo{}, 10, ""},
	} {
		if got := c.t.Name(c.n); got != c.want {
			t.Errorf("Name(%q, %q, %d) = %q, want %q", c.t.Title, c.t.Prompt, c.n, got, c.want)
		}
	}
}
