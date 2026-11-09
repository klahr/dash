package queue

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/klahr/dash/internal/model"
)

type recordPub struct {
	mu  sync.Mutex
	evs []model.Event
}

func (p *recordPub) Publish(ev model.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evs = append(p.evs, ev)
}

func TestTodoLogEvents(t *testing.T) {
	pub := &recordPub{}
	l := &TodoLog{Pub: pub, Root: t.TempDir()}
	td := model.Todo{ID: "t1", ProjectID: "p1"}

	if evs, err := l.Events(td.ID); err != nil || len(evs) != 0 {
		t.Fatalf("no events yet: %v %v", evs, err)
	}
	l.Event(td, model.RunEvent{Kind: "run_start", Text: "Started"})
	l.Event(td, model.RunEvent{Kind: "tool_use", ID: "tu1", Tool: "Bash", Input: []byte(`{"command":"go test ./..."}`)})
	l.Event(td, model.RunEvent{Kind: "tool_result", ID: "tu1", Text: "ok"})
	l.Line(td, "✔ ready for review")

	evs, err := l.Events(td.ID)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range evs {
		if e.At.IsZero() {
			t.Errorf("%s has no time", e.Kind)
		}
		kinds = append(kinds, e.Kind)
	}
	if got := strings.Join(kinds, ","); got != "run_start,tool_use,tool_result,status" {
		t.Fatalf("kinds: %s", got)
	}
	if string(evs[1].Input) != `{"command":"go test ./..."}` || evs[2].Text != "ok" || evs[3].Text != "✔ ready for review" {
		t.Errorf("events: %+v", evs)
	}

	// The text log keeps the summaries only.
	text, _ := l.Read(td.ID)
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], " $ go test ./...") || !strings.HasSuffix(lines[1], " ✔ ready for review") {
		t.Errorf("text log:\n%s", text)
	}

	var events, outputs int
	for _, e := range pub.evs {
		switch e.Type {
		case model.EvTodoEvent:
			events++
		case model.EvTodoOutput:
			outputs++
		}
		if e.TodoID != td.ID || e.ProjectID != td.ProjectID {
			t.Errorf("published for %s/%s", e.ProjectID, e.TodoID)
		}
	}
	if events != 4 || outputs != 2 {
		t.Errorf("published %d events, %d outputs", events, outputs)
	}

	// A line cut short by a crash is skipped.
	f, _ := os.OpenFile(filepath.Join(l.Root, "t1.events.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"kind":"te`)
	f.Close()
	if evs, err := l.Events(td.ID); err != nil || len(evs) != 4 {
		t.Errorf("after a torn line: %d events, %v", len(evs), err)
	}

	l.Remove(td.ID)
	if evs, _ := l.Events(td.ID); len(evs) != 0 {
		t.Errorf("events after Remove: %+v", evs)
	}
	if text, _ := l.Read(td.ID); text != "" {
		t.Errorf("text log after Remove: %q", text)
	}
}

// A log past MaxBytes keeps its newest events, whole, behind a note, and
// never grows much beyond the cap.
func TestTodoLogCapped(t *testing.T) {
	l := &TodoLog{Pub: &recordPub{}, Root: t.TempDir(), MaxBytes: 4096}
	td := model.Todo{ID: "t1", ProjectID: "p1"}
	for i := range 500 {
		l.Event(td, model.RunEvent{Kind: "text", Text: fmt.Sprintf("line %03d %s", i, strings.Repeat("x", 40))})
	}
	for _, p := range []string{l.eventsPath(td.ID), l.path(td.ID)} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > l.MaxBytes {
			t.Errorf("%s is %d bytes, over the cap of %d", filepath.Base(p), info.Size(), l.MaxBytes)
		}
		if _, err := os.Stat(p + ".trim"); !os.IsNotExist(err) {
			t.Errorf("temporary file left: %v", err)
		}
	}
	evs, err := l.Events(td.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) < 10 || evs[0].Kind != "status" || !strings.Contains(evs[0].Text, "trimmed") {
		t.Fatalf("first event: %+v (of %d)", evs[0], len(evs))
	}
	for _, e := range evs[1:] {
		if e.Kind != "text" || !strings.HasPrefix(e.Text, "line ") {
			t.Fatalf("a cut or stray event: %+v", e)
		}
	}
	if last := evs[len(evs)-1].Text; !strings.HasPrefix(last, "line 499 ") {
		t.Errorf("newest event lost: %q", last)
	}
	text, _ := l.Read(td.ID)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if !strings.Contains(lines[0], "trimmed") || !strings.Contains(lines[len(lines)-1], "line 499 ") {
		t.Errorf("text log: first %q, last %q", lines[0], lines[len(lines)-1])
	}
}

func TestTodoLogUncapped(t *testing.T) {
	l := &TodoLog{Pub: &recordPub{}, Root: t.TempDir()}
	td := model.Todo{ID: "t1", ProjectID: "p1"}
	for i := range 200 {
		l.Event(td, model.RunEvent{Kind: "text", Text: fmt.Sprintf("line %d", i)})
	}
	if evs, _ := l.Events(td.ID); len(evs) != 200 {
		t.Errorf("%d events, want all 200", len(evs))
	}
}
