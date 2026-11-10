package queue

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

func TestDeleteProjectKeepsFolder(t *testing.T) {
	parallel(t)

	h := newHarness(t, nil, model.Project{Name: "p"})
	ctx := context.Background()
	td, _ := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: "x"})
	wt := model.Worktree{Name: "todo-x", Path: filepath.Join(h.merges.WorktreeRoot, h.proj.ID, "todo-x"), Branch: "dash/todo-x"}
	if err := h.git.AddWorktreeAt(ctx, h.proj.ID, h.proj.Path, wt.Path, wt.Branch, "main"); err != nil {
		t.Fatal(err)
	}

	todos, err := h.merges.DeleteProject(ctx, h.proj.ID, false)
	if err != nil || len(todos) != 1 || todos[0].ID != td.ID {
		t.Fatalf("delete: %v %+v", err, todos)
	}
	if _, err := h.st.GetProject(h.proj.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("project still there: %v", err)
	}
	if _, err := h.st.GetTodo(td.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("todo still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.proj.Path, ".git")); err != nil {
		t.Fatalf("folder should be kept: %v", err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree should be gone: %v", err)
	}
	if out, _ := exec.Command("git", "-C", h.proj.Path, "branch", "--list", "dash/*").Output(); len(out) != 0 {
		t.Fatalf("dash branches left: %s", out)
	}
}

func TestDeleteProjectWithFolder(t *testing.T) {
	parallel(t)

	h := newHarness(t, nil, model.Project{Name: "p"})
	if _, err := h.merges.DeleteProject(context.Background(), h.proj.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.proj.Path); !os.IsNotExist(err) {
		t.Fatalf("folder should be gone: %v", err)
	}
}

func TestDeleteProjectRefusesBusyAndOddFolders(t *testing.T) {
	parallel(t)

	h := newHarness(t, nil, model.Project{Name: "p"})
	td, _ := h.st.CreateTodo(model.Todo{ProjectID: h.proj.ID, Title: "x"})
	if _, err := h.st.UpdateTodo(td.ID, func(x *model.Todo) error { x.Reviewing = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := h.merges.DeleteProject(context.Background(), h.proj.ID, false); !errors.Is(err, ErrProjectBusy) {
		t.Fatalf("want busy, got %v", err)
	}

	home, _ := os.UserHomeDir()
	for _, p := range []string{"/", home, t.TempDir()} {
		if err := checkDeletable(p); err == nil {
			t.Errorf("checkDeletable(%q): want refusal", p)
		}
	}
	if err := checkDeletable(h.proj.Path); err != nil {
		t.Errorf("project folder: %v", err)
	}
}
