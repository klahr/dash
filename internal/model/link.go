package model

import (
	"slices"
	"time"
)

// Kinds of Link: how two tasks met.
const (
	// LinkConflict: their branches conflict (queue.ConflictWatch found it).
	LinkConflict = "conflict"
	// LinkNote: the task ahead in the merge queue (Out) gave the other a
	// note about a conflict between them.
	LinkNote = "note"
	// LinkActiveWork: a task (Out) called active_work and got the other back.
	LinkActiveWork = "active_work"
)

// MaxLinks caps a task's Links; the oldest go first.
const MaxLinks = 30

// Link is one time a task met another task: a conflict between their
// branches, a conflict note one gave the other, an active_work call of one
// that returned the other. Each side keeps its own Link, from its point of
// view, so a card draws from its own list.
type Link struct {
	Kind string `json:"kind"`
	// Out: this task did it (gave the note, called active_work); else the
	// other task did it, to this one. A conflict goes both ways (false).
	Out bool `json:"out,omitempty"`
	// TodoID is the other task. On a runner, HubTodoID is the hub's id for
	// it, if it is a hub's task: the hub shows the link with its own ids.
	TodoID    string `json:"todo_id,omitempty"`
	HubTodoID string `json:"hub_todo_id,omitempty"`
	Name      string `json:"name,omitempty"` // the other task's name
	// Files are the files it was about (a conflict's, a note's, the other
	// task's files an active_work call listed), at most MaxLinkFiles.
	Files []string `json:"files,omitempty"`
	// Delivered: a note this task got has gone to Claude, in a run.
	Delivered bool      `json:"delivered,omitempty"`
	At        time.Time `json:"at"`
}

// MaxLinkFiles caps a Link's Files.
const MaxLinkFiles = 10

// same reports whether l and o record the same meeting (kind, direction,
// other task), which a newer one replaces.
func (l Link) same(o Link) bool {
	return l.Kind == o.Kind && l.Out == o.Out && l.TodoID == o.TodoID && (l.TodoID != "" || l.Name == o.Name)
}

// AddLink adds l to links, replacing an older Link of the same meeting
// (so repeated active_work calls don't push the others out), newest last,
// at most MaxLinks. A replaced note keeps whether it was delivered.
func AddLink(links []Link, l Link) []Link {
	if len(l.Files) > MaxLinkFiles {
		l.Files = l.Files[:MaxLinkFiles]
	}
	l.Files = slices.Clone(l.Files)
	if l.At.IsZero() {
		l.At = time.Now()
	}
	out := make([]Link, 0, len(links)+1)
	for _, x := range links {
		if x.same(l) {
			continue
		}
		out = append(out, x)
	}
	out = append(out, l)
	if len(out) > MaxLinks {
		out = out[len(out)-MaxLinks:]
	}
	return out
}

// CloneLinks copies links deeply.
func CloneLinks(links []Link) []Link {
	if links == nil {
		return nil
	}
	out := make([]Link, len(links))
	for i, l := range links {
		l.Files = slices.Clone(l.Files)
		out[i] = l
	}
	return out
}

// TaskRef names another task in a RunEvent (a linked entry in the run log).
// On a runner HubID is the hub's id for it, so the hub's dashboards, which
// know no runner ids, can link it.
type TaskRef struct {
	ID    string `json:"id,omitempty"`
	HubID string `json:"hub_id,omitempty"`
	Name  string `json:"name,omitempty"`
}
