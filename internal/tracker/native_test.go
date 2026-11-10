package tracker_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tracker"
)

func TestNativeBackendPassesThrough(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, _ := st.CreateProject(model.Project{Name: "web", Path: "/tmp/web"})
	other, _ := st.CreateProject(model.Project{Name: "other", Path: "/tmp/other"})
	be := tracker.NewNative(st, p.ID)
	var _ tracker.Backend = be
	ctx := context.Background()

	ref, err := be.Create(ctx, tracker.Item{Title: "T", Description: "do it", Column: string(model.StatusQueued)})
	if err != nil {
		t.Fatal(err)
	}
	td, _ := st.GetTodo(ref.ID)
	if td.ProjectID != p.ID || td.Status != model.StatusQueued || td.Prompt != "do it" || td.ExternalRef != nil {
		t.Fatalf("todo %+v", td)
	}
	if err := be.Update(ctx, ref, tracker.Item{Title: "T2", Description: "do it better"}); err != nil {
		t.Fatal(err)
	}
	if err := be.SetStatus(ctx, ref, model.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	if err := be.Update(ctx, ref, tracker.Item{Title: "T3", Description: "changed while running"}); err == nil {
		t.Fatal("a running todo's prompt changed")
	}
	if err := be.SetStatus(ctx, ref, model.StatusMerged); err == nil {
		t.Fatal("the state machine was skipped")
	}
	if err := be.Comment(ctx, ref, "hello"); err != nil {
		t.Fatal(err)
	}
	items, err := be.List(ctx, time.Time{})
	if err != nil || len(items) != 1 || items[0].Title != "T2" || items[0].Column != string(model.StatusInProgress) {
		t.Fatalf("items %+v err %v", items, err)
	}
	// Another project's todos are out of reach.
	ot, _ := st.CreateTodo(model.Todo{ProjectID: other.ID, Title: "x"})
	if err := be.Delete(ctx, tracker.Ref{ID: ot.ID}); err == nil {
		t.Fatal("deleted another project's todo")
	}
	if r, ok := tracker.Lookup(""); !ok || !r.Native {
		t.Fatal("the default backend isn't native")
	}
	if _, err := tracker.New(tracker.ProjectConfig{Backend: tracker.Native}); err == nil {
		t.Fatal("New built a native backend")
	}
}
