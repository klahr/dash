package applink

import "testing"

func TestIs(t *testing.T) {
	yes := []string{
		"/planning", "/planning/cards/c1", "/p/abc", "/p/abc/board", "/p/abc/history", "/p/abc/checklist",
		"/p/abc/code", "/p/abc/shell", "/p/abc/try", "/p/abc/deploys", "/p/abc/files", "/p/abc/run", "/p/abc/scans",
		"/p/abc/tasks/t1", "/p/abc/goals/g1", "/p/abc/history/0123abcd", "/p/abc/issues/i1", "/p/abc@mac-mini/goals", "/p/abc@Mac Mini",
	}
	for _, p := range yes {
		if !Is(p) {
			t.Errorf("Is(%q) = false, want true", p)
		}
	}
	no := []string{
		"/", "/index.html", "/p", "/p/", "/p/abc/", "/p/abc/nope", "/p/abc/tasks", "/p/abc/tasks/", "/p/abc/board/x", "/p/abc/code/x",
		"/p/abc/tasks/t1/x", "/p/../api", "/p/abc/tasks/..", "/planning/", "/planning/cards", "/planning/cards/",
		"/planning/cards/a/b", "/planning.html", "/api/projects", "/go/acme", "/wall", "/preview/t1", "/p/a\tb", "/p/a\\b",
	}
	for _, p := range no {
		if Is(p) {
			t.Errorf("Is(%q) = true, want false", p)
		}
	}
}

func TestPaths(t *testing.T) {
	for got, want := range map[string]string{
		Project("p1"):    "/p/p1",
		Task("p1", "t1"): "/p/p1/tasks/t1",
		Goal("p1", "g1"): "/p/p1/goals/g1",
		Card("c1"):       "/planning/cards/c1",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if !Is(got) {
			t.Errorf("Is(%q) = false", got)
		}
	}
}
