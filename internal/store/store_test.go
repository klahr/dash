package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/klahr/dash/internal/model"
)

func TestChatSurvivesReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.ChatMessage{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	if err := s.SetChat(1, 1, want); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Chat(1, 1); !reflect.DeepEqual(got, want) {
		t.Fatalf("after reopen: %v, want %v", got, want)
	}

	if err := s.SetChat(1, 1, nil); err != nil {
		t.Fatal(err)
	}
	if s, _ = Open(path, nil); len(s.Chat(1, 1)) != 0 {
		t.Fatalf("cleared chat came back: %v", s.Chat(1, 1))
	}
}

// A chat client sends the whole conversation each turn: saving it writes
// only the messages that changed, and the result is still what was set.
func TestSetChatWritesOnlyWhatChanged(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	q := model.ChatMessage{Role: "user", Content: "hi", CreatedBy: 7, At: &at}
	a := model.ChatMessage{Role: "assistant", Content: "hello", Usage: &model.ChatUsage{InputTokens: 3}}
	if err := s.SetChat(1, 1, []model.ChatMessage{q, a}); err != nil {
		t.Fatal(err)
	}
	// Mark the stored rows: a row rewritten loses its mark.
	if _, err := s.db.Exec(`UPDATE chat_messages SET data = json_set(data, '$.content', 'kept ' || seq) WHERE kind = 'main'`); err != nil {
		t.Fatal(err)
	}
	reopen := func() []model.ChatMessage {
		t.Helper()
		r, err := Open(path, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r.Chat(1, 1)
	}
	contents := func(msgs []model.ChatMessage) []string {
		var out []string
		for _, m := range msgs {
			out = append(out, m.Content)
		}
		return out
	}

	// A new turn: the conversation so far (an equal copy) and two more.
	at2 := at.Local() // the same instant, as the client sends it back
	q2, a2 := model.ChatMessage{Role: "user", Content: "more"}, model.ChatMessage{Role: "assistant", Content: "sure"}
	q1 := q
	q1.At = &at2
	if err := s.SetChat(1, 1, []model.ChatMessage{q1, {Role: "assistant", Content: "hello", Usage: &model.ChatUsage{InputTokens: 3}}, q2, a2}); err != nil {
		t.Fatal(err)
	}
	if got, want := contents(reopen()), []string{"kept 0", "kept 1", "more", "sure"}; !slices.Equal(got, want) {
		t.Fatalf("after a turn: %q, want %q", got, want)
	}

	// An answer that changed and a shorter conversation: the rest goes.
	a.Content = "hello again"
	if err := s.SetChat(1, 1, []model.ChatMessage{q, a}); err != nil {
		t.Fatal(err)
	}
	if got, want := contents(reopen()), []string{"kept 0", "hello again"}; !slices.Equal(got, want) {
		t.Fatalf("after editing: %q, want %q", got, want)
	}

	// Another field differing is a change too.
	q.CreatedBy = 8
	if err := s.SetChat(1, 1, []model.ChatMessage{q, a}); err != nil {
		t.Fatal(err)
	}
	if got := reopen(); !reflect.DeepEqual(contents(got), []string{"hi", "hello again"}) || got[0].CreatedBy != 8 {
		t.Fatalf("after changing the author: %+v", got)
	}

	if err := s.SetChat(1, 1, nil); err != nil {
		t.Fatal(err)
	}
	if got := reopen(); len(got) != 0 {
		t.Fatalf("cleared chat came back: %v", got)
	}
}

func TestTodoChatSurvivesReopenAndGoesWithTodo(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTodoChat("missing", []model.ChatMessage{{Role: "user", Content: "hi"}}); err != ErrNotFound {
		t.Fatalf("chat on a missing todo: %v, want ErrNotFound", err)
	}
	p, err := s.CreateProject(model.Project{Name: "p", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	td, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	want := []model.ChatMessage{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	if err := s.SetTodoChat(td.ID, want); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.TodoChat(td.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("after reopen: %v, want %v", got, want)
	}
	if len(s.Chat(1, 1)) != 0 {
		t.Fatalf("todo chat leaked into the general chat: %v", s.Chat(1, 1))
	}
	if _, err := s.DeleteTodo(td.ID); err != nil {
		t.Fatal(err)
	}
	if s, _ = Open(path, nil); len(s.TodoChat(td.ID)) != 0 {
		t.Fatalf("deleted todo's chat came back: %v", s.TodoChat(td.ID))
	}
}

func TestReviewingLocksStatusUntilReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(model.Project{Name: "p", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	td, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []model.TodoStatus{model.StatusQueued, model.StatusInProgress, model.StatusReview} {
		if _, err := s.SetStatus(td.ID, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.UpdateTodo(td.ID, func(t *model.Todo) error { t.Reviewing = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(td.ID, model.StatusQueued, ""); err != ErrReviewing {
		t.Fatalf("status change while reviewing: %v, want ErrReviewing", err)
	}
	if _, err := s.UpdateTodo(td.ID, func(t *model.Todo) error { t.Title = "renamed"; return nil }); err != nil {
		t.Fatalf("non-status edit while reviewing: %v", err)
	}
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTodo(td.ID); got.Reviewing {
		t.Fatal("reviewing survived a restart")
	}
	if _, err := s.SetStatus(td.ID, model.StatusQueued, ""); err != nil {
		t.Fatalf("status change after the review: %v", err)
	}
}

func TestDependsOnValidationAndDelete(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.CreateProject(model.Project{Name: "p"})
	q, _ := s.CreateProject(model.Project{Name: "q"})
	a, _ := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "a"})
	elsewhere, _ := s.CreateTodo(model.Todo{ProjectID: q.ID, Title: "x"})

	for name, deps := range map[string][]string{
		"unknown":       {"nope"},
		"other project": {elsewhere.ID},
	} {
		if _, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: name, DependsOn: deps}); !errors.Is(err, ErrBadDependency) {
			t.Errorf("%s: got %v, want ErrBadDependency", name, err)
		}
	}
	if _, err := s.UpdateTodo(a.ID, func(x *model.Todo) error { x.DependsOn = []string{a.ID}; return nil }); !errors.Is(err, ErrBadDependency) {
		t.Errorf("self: got %v, want ErrBadDependency", err)
	}

	b, err := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "b", DependsOn: []string{a.ID, " " + a.ID, ""}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.DependsOn, []string{a.ID}) {
		t.Fatalf("depends_on not cleaned: %q", b.DependsOn)
	}
	// Survives a reopen.
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTodo(b.ID); !reflect.DeepEqual(got.DependsOn, []string{a.ID}) {
		t.Fatalf("after reopen: %q", got.DependsOn)
	}
	// Deleting a forgets it in others' dependencies.
	if _, err := s.DeleteTodo(a.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTodo(b.ID); len(got.DependsOn) != 0 {
		t.Fatalf("deleted dependency kept: %q", got.DependsOn)
	}
}

func TestOldStateWithoutDependsOnLoads(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "dash.db")
	old := `{"projects":{"p":{"id":"p","name":"p"}},"todos":{"t":{"id":"t","project_id":"p","title":"t","status":"queued"}}}`
	if err := os.WriteFile(filepath.Join(dir, LegacyFile), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetTodo("t"); err != nil || len(got.DependsOn) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// Upgrading: projects whose user didn't choose get the default, pull
// requests on GitHub (also projects on runners, forced into them once and
// switched to merging by an earlier upgrade); a user's own choice, projects
// without a GitHub remote and runners' clones keep what they have. Without
// gh here, GitHub projects merge, except those on runners.
func TestDefaultMergeModes(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mk := func(name string, p model.Project) model.Project {
		p.Name = name
		p, err := s.CreateProject(p)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	const gh = "https://github.com/acme/web.git"
	plain := mk("plain", model.Project{RemoteURL: gh})
	ssh := mk("ssh", model.Project{RemoteURL: "git@github.com:acme/api.git", MergeMode: model.MergeLocal})
	runners := mk("runners", model.Project{RemoteURL: gh, MergeMode: model.MergeLocal, Runners: []string{"r"}})
	forced := mk("forced", model.Project{RemoteURL: gh, MergeMode: model.MergePR, Runners: []string{"r"}})
	chosen := mk("chosen", model.Project{RemoteURL: gh, MergeMode: model.MergeLocal, MergeModeSet: true})
	chosenPR := mk("chosen pr", model.Project{RemoteURL: "https://gitlab.com/acme/x.git", MergeMode: model.MergePR, MergeModeSet: true})
	gitlab := mk("gitlab", model.Project{RemoteURL: "https://gitlab.com/acme/x.git", MergeMode: model.MergePR})
	none := mk("none", model.Project{})
	clone := mk("clone", model.Project{RemoteURL: gh, MergeMode: model.MergeLocal, HubProjectID: "hp"})

	names, err := s.DefaultMergeModes(true)
	slices.Sort(names)
	if err != nil || !slices.Equal(names, []string{"gitlab", "none", "plain", "runners", "ssh"}) {
		t.Fatalf("changed %v %v", names, err)
	}
	check := func(want map[string]string) {
		t.Helper()
		ids := map[string]string{plain.Name: plain.ID, ssh.Name: ssh.ID, runners.Name: runners.ID, forced.Name: forced.ID,
			chosen.Name: chosen.ID, chosenPR.Name: chosenPR.ID, gitlab.Name: gitlab.ID, none.Name: none.ID, clone.Name: clone.ID}
		for name, mode := range want {
			if got, _ := s.GetProject(ids[name]); got.MergeMode != mode {
				t.Errorf("%s: merge mode %q, want %q", name, got.MergeMode, mode)
			}
		}
	}
	check(map[string]string{"plain": model.MergePR, "ssh": model.MergePR, "runners": model.MergePR, "forced": model.MergePR,
		"chosen": model.MergeLocal, "chosen pr": model.MergePR, "gitlab": model.MergeLocal, "none": model.MergeLocal,
		"clone": model.MergeLocal})
	if names, _ := s.DefaultMergeModes(true); len(names) != 0 {
		t.Errorf("a second run changed %v", names)
	}

	// gh gone: only the runners still open pull requests.
	names, _ = s.DefaultMergeModes(false)
	slices.Sort(names)
	if !slices.Equal(names, []string{"plain", "ssh"}) {
		t.Errorf("without gh changed %v", names)
	}
	check(map[string]string{"plain": model.MergeLocal, "ssh": model.MergeLocal, "runners": model.MergePR, "forced": model.MergePR,
		"chosen pr": model.MergePR})
}

// A pull request's check state is kept with the task and survives a reopen.
func TestPullRequestChecksKept(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "dash.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.CreateProject(model.Project{Name: "p"})
	td, _ := s.CreateTodo(model.Todo{ProjectID: p.ID, Title: "a"})
	want := model.PullRequest{Number: 10, URL: "https://github.com/o/r/pull/10", State: "OPEN",
		Checks: []model.PRCheck{{Name: "Go", State: "passed", URL: "https://ci/1"}, {Name: "Docker", State: "pending"}},
		Status: "Waiting for checks: Docker (1/2 passed)"}
	if _, err := s.UpdateTodo(td.ID, func(x *model.Todo) error { pr := want; x.PR = &pr; return nil }); err != nil {
		t.Fatal(err)
	}
	// An update that gives up leaves the stored checks as they were.
	_, _ = s.UpdateTodo(td.ID, func(x *model.Todo) error { x.PR.Checks[0].State = "failed"; return errors.New("give up") })
	if got, _ := s.GetTodo(td.ID); got.PR.Checks[0].State != "passed" {
		t.Fatal("an abandoned update changed the stored checks")
	}
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTodo(td.ID); !reflect.DeepEqual(*got.PR, want) {
		t.Fatalf("after reopen: %+v, want %+v", *got.PR, want)
	}
}
