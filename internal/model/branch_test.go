package model

import "testing"

func TestProjectBase(t *testing.T) {
	p := Project{DefaultBranch: "main"}
	if p.Base() != "main" || p.OnWorkBranch() {
		t.Fatalf("no work branch: %q", p.Base())
	}
	p.WorkBranch = "feature"
	if p.Base() != "feature" || !p.OnWorkBranch() {
		t.Fatalf("work branch: %q", p.Base())
	}
	// A todo keeps the branch its worktree was made from; one from before
	// branches could be switched is on the default branch; one not started
	// yet follows the project.
	if b := p.For(Todo{Worktree: &Worktree{BaseBranch: "other"}}).Base(); b != "other" {
		t.Errorf("started on other: %q", b)
	}
	if b := p.For(Todo{Worktree: &Worktree{}}).Base(); b != "main" {
		t.Errorf("old worktree: %q", b)
	}
	if b := p.For(Todo{}).Base(); b != "feature" {
		t.Errorf("not started: %q", b)
	}
}
