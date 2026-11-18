package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A task saved before links (and a run log entry before linked ones)
// loads without them, and saves without them again.
func TestOldStateLoadsWithoutLinks(t *testing.T) {
	var td Todo
	old := `{"id":"a","project_id":"p","title":"x","status":"review","conflicts":[{"todo_id":"b","name":"y","files":["f.go"]}],"conflict_noted":["b"]}`
	if err := json.Unmarshal([]byte(old), &td); err != nil {
		t.Fatal(err)
	}
	if td.Links != nil || len(td.Conflicts) != 1 {
		t.Fatalf("loaded %+v", td)
	}
	b, err := json.Marshal(td)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"links"`) {
		t.Errorf("saved %s, want no links", b)
	}
	var ev RunEvent
	if err := json.Unmarshal([]byte(`{"kind":"status","text":"hi","at":"2026-11-18T10:00:00Z"}`), &ev); err != nil || ev.Tasks != nil {
		t.Fatalf("event %+v, %v", ev, err)
	}
	if b, _ := json.Marshal(ev); strings.Contains(string(b), `"tasks"`) {
		t.Errorf("event saved as %s", b)
	}
}

// AddLink replaces the same meeting rather than repeating it, keeps the
// newest last, and stays within MaxLinks and MaxLinkFiles.
func TestAddLink(t *testing.T) {
	at := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	var ls []Link
	ls = AddLink(ls, Link{Kind: LinkActiveWork, Out: true, TodoID: "b", At: at})
	ls = AddLink(ls, Link{Kind: LinkConflict, TodoID: "b", At: at})
	ls = AddLink(ls, Link{Kind: LinkActiveWork, TodoID: "b", At: at})                             // the other way: another meeting
	ls = AddLink(ls, Link{Kind: LinkActiveWork, Out: true, TodoID: "b", At: at.Add(time.Minute)}) // again: replaces the first
	if len(ls) != 3 || ls[2].Kind != LinkActiveWork || !ls[2].Out || !ls[2].At.Equal(at.Add(time.Minute)) {
		t.Fatalf("links %+v", ls)
	}
	// Tasks known by name only are told apart by it.
	ls = AddLink(ls, Link{Kind: LinkConflict, Name: "x"})
	ls = AddLink(ls, Link{Kind: LinkConflict, Name: "y"})
	if len(ls) != 5 {
		t.Fatalf("by name: %d links", len(ls))
	}

	files := make([]string, 50)
	for i := range files {
		files[i] = fmt.Sprintf("f%d.go", i)
	}
	for i := range 2 * MaxLinks {
		ls = AddLink(ls, Link{Kind: LinkActiveWork, TodoID: fmt.Sprint(i), Files: files})
	}
	if len(ls) != MaxLinks || ls[MaxLinks-1].TodoID != fmt.Sprint(2*MaxLinks-1) || len(ls[0].Files) != MaxLinkFiles {
		t.Fatalf("%d links, last %q, %d files", len(ls), ls[len(ls)-1].TodoID, len(ls[0].Files))
	}
	files[0] = "changed"
	if ls[0].Files[0] == "changed" {
		t.Error("AddLink shares the caller's files")
	}
}
