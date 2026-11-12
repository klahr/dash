package mcptools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/klahr/dash/internal/notes"
)

// Shared notes: what the tasks of a hub project leave for each other, kept
// in every runner's own copy (package notes) and synced through the hub's
// relay. A running task reaches them with the NotesTools, on their own MCP
// server (NotesServerName) served by the runner the task runs on: unlike
// the board tools of a hub's task, which go to the hub, these act on the
// runner's local copy, so they answer at once, also while the hub is away.

// NotesServerName is the MCP server of the NotesTools.
const NotesServerName = "dash_notes"

// NotesToolNames are the NotesTools, which a running task gets when its
// project has shared notes on its runner.
var NotesToolNames = []string{"shared_list", "shared_read", "shared_write", "shared_delete", "shared_confirm"}

// Notes is one project's shared notes, as one task's run uses them: its
// writes are credited to the task.
type Notes interface {
	List(q notes.Query) ([]notes.Entry, error)
	Get(id string) (notes.Entry, error)
	// Put writes d (its AuthorTask is set by the Notes).
	Put(d notes.Draft) (notes.Entry, error)
	Delete(id string) error
	// Confirm pushes note id's expiry ttl from now (0: the project's default).
	Confirm(id string, ttl time.Duration) (notes.Entry, error)
}

// ServiceNotes is project's notes in a runner's notes.Service, for task.
// Writes return once saved on this runner; they reach the other runners in
// the background.
type ServiceNotes struct {
	S       *notes.Service
	Project string // the hub's project id
	Task    string // credited with the writes
}

func (n ServiceNotes) ctx() context.Context { return notes.InBackground(context.Background()) }

func (n ServiceNotes) List(q notes.Query) ([]notes.Entry, error) { return n.S.List(n.Project, q) }
func (n ServiceNotes) Get(id string) (notes.Entry, error)        { return n.S.Get(n.Project, id) }
func (n ServiceNotes) Put(d notes.Draft) (notes.Entry, error) {
	d.AuthorTask = n.Task
	return n.S.Put(n.ctx(), n.Project, d)
}
func (n ServiceNotes) Delete(id string) error { return n.S.Delete(n.ctx(), n.Project, id, n.Task) }
func (n ServiceNotes) Confirm(id string, ttl time.Duration) (notes.Entry, error) {
	return n.S.Confirm(n.ctx(), n.Project, id, n.Task, ttl)
}

// NotesHint is the pointer to the shared notes in a task's system prompt:
// how many there are and the tools to use, never their contents, so the
// prompt doesn't grow with the notes.
func NotesHint(count int) string {
	tool := func(name string) string { return "mcp__" + NotesServerName + "__" + name }
	have := fmt.Sprintf("This project has %d shared notes", count)
	switch count {
	case 0:
		have = "This project has no shared notes yet"
	case 1:
		have = "This project has 1 shared note"
	}
	return have + ": facts that tasks on this project, on every computer, leave for each other. " +
		"List them with " + tool("shared_list") + " (filter by tag or text) and read one with " + tool("shared_read") + " " +
		"when they may help with your task. Write one with " + tool("shared_write") + " only for a non-obvious, lasting fact " +
		"that other tasks will need (how to build or test something tricky, a pitfall, where something lives), " +
		"not for what this task did or what the code and git history already say. Never put secrets in a note " +
		"(passwords, tokens, keys): say where to find them instead. When a note is outdated, update it " +
		"(write the same title) or delete it with " + tool("shared_delete") + " instead of adding one that contradicts it. " +
		"When a note you relied on turned out to be correct, call " + tool("shared_confirm") + " so it doesn't expire. " +
		"Notes expire unless confirmed or rewritten; use permanent sparingly, for facts that will stay true."
}

// sameTitle reports whether two note titles are the same title (a project
// has one note per title).
func sameTitle(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// noteRef finds the live note a call names: by id, else by title.
func noteRef(n Notes, a Args) (notes.Entry, error) {
	if id := a.Str("id"); id != "" {
		e, err := n.Get(id)
		if errors.Is(err, notes.ErrNotFound) {
			return e, fmt.Errorf("no shared note with id %q (see shared_list)", id)
		}
		return e, notesErr(err)
	}
	t := a.Str("title")
	if t == "" {
		return notes.Entry{}, errors.New("title (or id) is required")
	}
	es, err := n.List(notes.Query{})
	if err != nil {
		return notes.Entry{}, notesErr(err)
	}
	for _, e := range es {
		if sameTitle(e.Key, t) {
			return e, nil
		}
	}
	return notes.Entry{}, fmt.Errorf("no shared note titled %q (see shared_list)", t)
}

// notesErr makes the store's errors clear to a task.
func notesErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, notes.ErrNoProject):
		return errors.New("shared notes aren't available for this project on this computer")
	case errors.Is(err, notes.ErrTooMany):
		return errors.New("this project's shared notes are full, and the only notes left to evict are permanent: " +
			"delete an outdated note (shared_delete) or write a shorter one")
	case errors.Is(err, notes.ErrStale):
		return errors.New("this computer's copy of the shared notes is waiting for a full resync from another computer; " +
			"writes are refused until then, try again later")
	case errors.Is(err, notes.ErrSecret):
		return fmt.Errorf("not saved: %v", err) // names what it found, and what to do
	case errors.Is(err, notes.ErrTooLarge):
		return fmt.Errorf("not saved: %v (title at most %d bytes, at most %d tags)", err, notes.MaxKeyLen, notes.MaxTags)
	}
	return err
}

// checkDraft explains what makes d too large before the store refuses it.
func checkDraft(d notes.Draft) error {
	size := len(d.Key) + len(d.Body)
	for _, t := range d.Tags {
		size += len(t)
		if len(t) > 64 {
			return fmt.Errorf("not saved: tag %.20q… is %d bytes; a tag may have at most 64", t, len(t))
		}
	}
	switch {
	case len(d.Key) > notes.MaxKeyLen:
		return fmt.Errorf("not saved: the title is %d bytes; at most %d", len(d.Key), notes.MaxKeyLen)
	case len(d.Tags) > notes.MaxTags:
		return fmt.Errorf("not saved: %d tags; at most %d", len(d.Tags), notes.MaxTags)
	case size > notes.MaxEntrySize:
		return fmt.Errorf("not saved: the note is %d bytes (title, body and tags); a shared note may hold at most %d, so make it shorter or split it",
			size, notes.MaxEntrySize)
	}
	return nil
}

// noteRow is a note as shared_list shows it.
type noteRow struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Tags      []string   `json:"tags,omitempty"`
	Author    string     `json:"author,omitempty"` // the task that wrote it last
	Updated   time.Time  `json:"updated"`
	Expires   *time.Time `json:"expires,omitempty"`
	Permanent bool       `json:"permanent,omitempty"`
}

func rowOf(e notes.Entry) noteRow {
	r := noteRow{ID: e.ID, Title: e.Key, Tags: e.Tags, Author: e.AuthorTask, Updated: e.Updated, Permanent: e.Permanent}
	if !e.Expires.IsZero() {
		x := e.Expires
		r.Expires = &x
	}
	return r
}

// noteFull is a note as shared_read shows it.
type noteFull struct {
	noteRow
	Created time.Time `json:"created"`
	Body    string    `json:"body"`
}

func fullOf(e notes.Entry) noteFull { return noteFull{rowOf(e), e.Created, e.Body} }

const day = 24 * time.Hour

// NotesTools returns shared_list, shared_read, shared_write, shared_delete
// and shared_confirm on n.
func NotesTools(n Notes) []Tool {
	pTitle := Param{"title", "string", "The note's title (any case).", false}
	pID := Param{"id", "string", "Instead of title: the note's id, from shared_list.", false}
	return []Tool{
		{"shared_list", "List the project's shared notes: title, tags, author task, when last updated and when it expires; " +
			"most recently updated first. Read one with shared_read.", []Param{
			{"tag", "string", "Only notes with this tag.", false},
			{"text", "string", "Only notes whose title or body contains this (any case).", false}},
			func(a Args) (any, error) {
				es, err := n.List(notes.Query{Tag: a.Str("tag"), Text: a.Str("text")})
				if err != nil {
					return nil, notesErr(err)
				}
				out := []noteRow{}
				for _, e := range es {
					out = append(out, rowOf(e))
				}
				return out, nil
			}},
		{"shared_read", "Read a shared note in full.", []Param{pTitle, pID},
			func(a Args) (any, error) {
				e, err := noteRef(n, a)
				if err != nil {
					return nil, err
				}
				return fullOf(e), nil
			}},
		{"shared_write", "Write a shared note for the project's other tasks: a non-obvious, lasting fact. " +
			"Writing a title that already exists updates that note (replacing its body and tags). " +
			"Never put secrets in a note. It is saved on this computer at once and reaches the others' copies shortly.", []Param{
			{"title", "string", fmt.Sprintf("Short title saying what the note is about (at most %d bytes); a project has one note per title.", notes.MaxKeyLen), true},
			{"body", "string", fmt.Sprintf("The note, in Markdown. Title, body and tags together hold at most %d bytes.", notes.MaxEntrySize), true},
			{"tags", "array", fmt.Sprintf("Tags to find it by, e.g. build, tests, deploy (at most %d).", notes.MaxTags), false},
			{"expires_in_days", "integer", "Days until it expires unless confirmed or rewritten (default: the project's default, 30 days).", false},
			{"permanent", "boolean", "Never expires nor is evicted when the notes are full. Use it sparingly, for facts that will stay true.", false}},
			func(a Args) (any, error) {
				d := notes.Draft{Key: a.Str("title"), Body: a.Str("body"), Tags: a.List("tags"), Permanent: a.Flag("permanent")}
				if d.Key == "" {
					return nil, errors.New("title is required")
				}
				if d.Body == "" {
					return nil, errors.New("body is required; to remove a note, use shared_delete")
				}
				if days := a.OptNum("expires_in_days"); days != nil {
					if d.Permanent {
						return nil, errors.New("pass expires_in_days or permanent, not both")
					}
					if *days < 1 {
						return nil, errors.New("expires_in_days must be at least 1")
					}
					d.TTL = time.Duration(*days) * day
				}
				if err := checkDraft(d); err != nil {
					return nil, err
				}
				var existed bool
				if es, err := n.List(notes.Query{}); err == nil {
					for _, e := range es {
						existed = existed || sameTitle(e.Key, d.Key)
					}
				}
				e, err := n.Put(d)
				if err != nil {
					return nil, notesErr(err)
				}
				what := "Saved a new shared note"
				if existed {
					what = "Updated the shared note"
				}
				return fmt.Sprintf("%s %q (id %s), %s.", what, e.Key, e.ID, lifeOf(e)), nil
			}},
		{"shared_delete", "Delete a shared note that is wrong or outdated, on every computer.", []Param{pTitle, pID},
			func(a Args) (any, error) {
				e, err := noteRef(n, a)
				if err != nil {
					return nil, err
				}
				if err := n.Delete(e.ID); err != nil {
					return nil, notesErr(err)
				}
				return fmt.Sprintf("Deleted the shared note %q.", e.Key), nil
			}},
		{"shared_confirm", "Say a shared note you relied on turned out to be correct: its expiry moves forward, on every computer.", []Param{pTitle, pID,
			{"days", "integer", "Days from now it should last (default: the project's default, 30 days).", false}},
			func(a Args) (any, error) {
				e, err := noteRef(n, a)
				if err != nil {
					return nil, err
				}
				var ttl time.Duration
				if days := a.OptNum("days"); days != nil {
					if *days < 1 {
						return nil, errors.New("days must be at least 1")
					}
					ttl = time.Duration(*days) * day
				}
				e, err = n.Confirm(e.ID, ttl)
				if err != nil {
					return nil, notesErr(err)
				}
				return fmt.Sprintf("Confirmed the shared note %q: %s.", e.Key, lifeOf(e)), nil
			}},
	}
}

// lifeOf says when e expires.
func lifeOf(e notes.Entry) string {
	if e.Permanent || e.Expires.IsZero() {
		return "permanent"
	}
	return "expires " + e.Expires.UTC().Format("2006-01-02")
}

// NotesHandler serves POST /mcp/notes: the NotesTools of a running task,
// authenticated by its run's token (the same as for /mcp/dash).
type NotesHandler struct {
	Tokens *Tokens
	// Notes is the shared notes of the task a token's scope runs, or an
	// error when it has none (e.g. a task that doesn't run on a runner).
	Notes func(Scope) (Notes, error)
}

func (h *NotesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "this MCP server only accepts POST", http.StatusMethodNotAllowed)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	var sc Scope
	if ok && token != "" {
		sc, ok = h.Tokens.lookup(token)
	}
	if !ok {
		http.Error(w, "unknown or expired token", http.StatusUnauthorized)
		return
	}
	if sc.Todo == "" || h.Notes == nil {
		http.Error(w, "shared notes are for running tasks", http.StatusForbidden)
		return
	}
	n, err := h.Notes(sc)
	if err != nil {
		http.Error(w, notesErr(err).Error(), http.StatusForbidden)
		return
	}
	srv := &Server{
		Name:         NotesServerName,
		Instructions: "The project's shared notes: lasting facts its tasks, on every computer, leave for each other.",
		Tools:        NotesTools(n),
	}
	srv.ServeHTTP(w, r)
}

// NotesServerConfig is the --mcp-config entry for the notes endpoint at
// baseURL (the URL of /mcp/dash's Dash).
func NotesServerConfig(baseURL, token string) map[string]any {
	return map[string]any{
		"type":    "http",
		"url":     strings.TrimRight(baseURL, "/") + "/mcp/notes",
		"headers": map[string]string{"Authorization": "Bearer " + token},
	}
}
