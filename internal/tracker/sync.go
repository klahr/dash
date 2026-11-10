package tracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// fields are a card's intent fields in a form both sides compare in:
// dependencies as card ids, the status as the column it maps to.
type fields struct {
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Priority *int     `json:"priority,omitempty"`
	Deps     []string `json:"deps,omitempty"`
	Column   string   `json:"column,omitempty"`
}

func (f fields) hash() string {
	b, _ := json.Marshal(f)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

func samePriority(a, b *int) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// linkState is the engine's memory of a link: each side's fields as of the
// last sync. They differ only while a tracker edit waits to be applied (a
// prompt change while the todo runs) or can't be (a dependency on a card
// Dash doesn't know).
type linkState struct {
	Dash   fields           `json:"dash"`
	Ext    fields           `json:"ext"`
	Status model.TodoStatus `json:"status,omitempty"` // the todo's status then
	Seen   []string         `json:"seen,omitempty"`   // comment ids already read
	Posted []string         `json:"posted,omitempty"` // hashes of the comments Dash posted
}

const maxRemembered = 200

type link struct {
	model.TrackerLink
	st linkState
}

func newLink(l model.TrackerLink) *link {
	x := &link{TrackerLink: l}
	if len(l.State) > 0 {
		_ = json.Unmarshal(l.State, &x.st)
	}
	return x
}

func (l *link) persist() model.TrackerLink {
	out := l.TrackerLink
	out.State, _ = json.Marshal(l.st)
	out.DashHash, out.ExtHash = l.st.Dash.hash(), l.st.Ext.hash()
	return out
}

func remember(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	list = append(list, v)
	if len(list) > maxRemembered {
		list = list[len(list)-maxRemembered:]
	}
	return list
}

func bodyHash(s string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(s)))
	return hex.EncodeToString(sum[:12])
}

// dashFields are t's intent fields: its dependencies on linked todos as
// their card ids, its status as the column the project maps it to.
func (w *worker) dashFields(t model.Todo) fields {
	f := fields{Title: t.Title, Body: t.Prompt, Priority: t.Priority, Column: w.cfg.ColumnFor(t.Status)}
	if w.skips(FieldPriority) {
		f.Priority = nil
	}
	for _, id := range t.DependsOn {
		if l, ok := w.links[id]; ok && l.Ref.ID != "" {
			f.Deps = append(f.Deps, l.Ref.ID)
		}
	}
	slices.Sort(f.Deps)
	return f
}

// skips reports whether the backend leaves a field to Dash.
func (w *worker) skips(field string) bool {
	fs, ok := w.be.(FieldSkipper)
	return ok && slices.Contains(fs.Skips(), field)
}

func (w *worker) extFields(it Item) fields {
	f := fields{Title: it.Title, Body: it.Description, Priority: it.Priority, Column: it.Column}
	if w.skips(FieldPriority) {
		f.Priority = nil
	}
	f.Deps = append([]string(nil), it.DependsOn...)
	slices.Sort(f.Deps)
	f.Deps = slices.Compact(f.Deps)
	if len(f.Deps) == 0 {
		f.Deps = nil
	}
	return f
}

func itemOf(f fields) Item {
	return Item{Title: f.Title, Description: f.Body, Priority: f.Priority, DependsOn: f.Deps, Column: f.Column}
}

// todoIDs are the todos standing for cards (unknown cards are left out).
func (w *worker) todoIDs(cards []string) []string {
	var out []string
	for _, c := range cards {
		if id, ok := w.byExt[c]; ok {
			out = append(out, id)
		}
	}
	return out
}

// ---- outbound: Dash → tracker ----

// outbound brings a todo's card up to date with it: makes one if the todo
// should have one, pushes the intent fields Dash changed since the last
// sync, moves the card with its status and comments on the moments that
// matter. A deleted todo's card is deleted.
func (w *worker) outbound(ctx context.Context, id string) error {
	t, err := w.m.Store.GetTodo(id)
	if err == nil && t.ProjectID != w.cfg.ProjectID {
		err = store.ErrNotFound // moved projects can't happen, but never sync another project's todo
	}
	l := w.links[id]
	if errors.Is(err, store.ErrNotFound) {
		if l == nil {
			return nil
		}
		if !l.Pending && l.Ref.ID != "" {
			if err := w.be.Delete(ctx, l.Ref); err != nil {
				return fmt.Errorf("delete card %s: %w", l.Ref.ID, err)
			}
		}
		return w.drop(id)
	}
	if err != nil {
		return err
	}
	if l == nil {
		// A todo from before the switch stays Dash-only unless it was
		// exported then; one made since gets a card.
		if !t.CreatedAt.After(w.cfg.SwitchedAt) || t.Status == model.StatusMerged {
			return nil
		}
		return w.export(ctx, t)
	}
	if l.Pending {
		return w.export(ctx, t)
	}
	if l.Ref.ID == "" {
		return nil // detached: its card was deleted, it stays Dash-only
	}
	if t.ExternalRef == nil || *t.ExternalRef != l.Ref {
		if t, err = w.m.Store.SetExternalRef(t.ID, &l.Ref); err != nil {
			return err
		}
	}
	return w.push(ctx, t, l)
}

// export makes a card for t and links them.
func (w *worker) export(ctx context.Context, t model.Todo) error {
	df := w.dashFields(t)
	ref, err := w.be.Create(ctx, itemOf(df))
	if err != nil {
		return fmt.Errorf("create card for %q: %w", t.Title, err)
	}
	if ref.Provider == "" {
		ref.Provider = w.be.Name()
	}
	l := &link{TrackerLink: model.TrackerLink{TodoID: t.ID, ProjectID: t.ProjectID, Ref: ref}}
	l.st = linkState{Dash: df, Ext: df, Status: t.Status}
	if err := w.save(l); err != nil {
		return err
	}
	_, err = w.m.Store.SetExternalRef(t.ID, &ref)
	return err
}

// push sends what Dash changed about t since the last sync.
func (w *worker) push(ctx context.Context, t model.Todo, l *link) error {
	st := &l.st
	if err := w.settle(ctx, &t, l); err != nil {
		return err
	}
	df := w.dashFields(t)
	ext := st.Ext
	changed := false
	if df.Title != st.Dash.Title {
		ext.Title, changed = df.Title, true
	}
	if df.Body != st.Dash.Body {
		ext.Body, changed = df.Body, true
	}
	if !samePriority(df.Priority, st.Dash.Priority) {
		ext.Priority, changed = df.Priority, true
	}
	if !slices.Equal(df.Deps, st.Dash.Deps) {
		ext.Deps, changed = df.Deps, true
	}
	moved := df.Column != st.Dash.Column && df.Column != ""
	statusChanged := t.Status != st.Status
	if !changed && !moved && !statusChanged {
		if df.hash() == st.Dash.hash() {
			return nil
		}
		st.Dash = df // e.g. a status whose column is unmapped: nothing to move
		return w.save(l)
	}
	if (changed || moved) && !w.fresh {
		// Read the tracker's changes first, so a newer edit there isn't
		// overwritten unseen (and conflicts go to the later writer).
		w.fresh = true
		cursor, err := w.pull(ctx)
		if err != nil {
			return err
		}
		w.advance(cursor)
		return w.outbound(ctx, t.ID)
	}
	if changed {
		it := itemOf(ext)
		it.Ref = l.Ref
		if err := w.be.Update(ctx, l.Ref, it); err != nil {
			return fmt.Errorf("update card %s: %w", l.Ref.ID, err)
		}
	}
	if moved {
		if err := w.be.SetStatus(ctx, l.Ref, t.Status); err != nil {
			return fmt.Errorf("move card %s: %w", l.Ref.ID, err)
		}
		ext.Column = df.Column
	}
	st.Dash, st.Ext = df, ext
	if statusChanged {
		st.Status = t.Status
		if note := statusNote(t); note != "" {
			if err := w.comment(ctx, l, note); err != nil {
				_ = w.save(l)
				return err
			}
		}
	}
	return w.save(l)
}

// settle applies a tracker edit to the prompt that waited for the todo to
// be editable again (see inbound): when Dash's prompt is still the one
// last synced and the todo is now a draft, queued or failed.
func (w *worker) settle(ctx context.Context, t *model.Todo, l *link) error {
	st := &l.st
	if st.Ext.Body == st.Dash.Body || t.Prompt != st.Dash.Body || !PromptEditable(t.Status) {
		return nil
	}
	body := st.Ext.Body
	nt, err := w.m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if !PromptEditable(x.Status) || x.Prompt != st.Dash.Body {
			return errSkip
		}
		x.Prompt = body
		if x.SessionID != "" {
			x.Feedback = append(x.Feedback, descriptionNote(w.be.Name(), body))
		}
		return nil
	})
	if errors.Is(err, errSkip) {
		return nil
	}
	if err != nil {
		return err
	}
	*t = nt
	st.Dash.Body = body
	return nil
}

var errSkip = errors.New("skip")

func descriptionNote(provider, body string) string {
	return fmt.Sprintf("The task's description was changed in %s. It now reads:\n\n%s", label(provider), body)
}

// statusNote is the comment a status change gets on the card.
func statusNote(t model.Todo) string {
	var b strings.Builder
	switch t.Status {
	case model.StatusReview:
		b.WriteString("Ready for review in Dash.")
		if t.Summary != "" {
			b.WriteString("\n\n" + t.Summary)
		}
		if t.PR != nil && t.PR.URL != "" {
			fmt.Fprintf(&b, "\n\nPull request: %s", t.PR.URL)
		}
	case model.StatusFailed:
		b.WriteString("Failed in Dash.")
		if t.Error != "" {
			b.WriteString("\n\n" + t.Error)
		}
	case model.StatusMerged:
		b.WriteString("Merged.")
		switch {
		case t.PR != nil && t.PR.URL != "":
			fmt.Fprintf(&b, "\n\nPull request: %s", t.PR.URL)
		case t.MergedCommit != "":
			fmt.Fprintf(&b, "\n\nCommit %s", t.MergedCommit)
		}
	default:
		return ""
	}
	return b.String()
}

func (w *worker) comment(ctx context.Context, l *link, body string) error {
	if err := w.be.Comment(ctx, l.Ref, body); err != nil {
		return fmt.Errorf("comment on card %s: %w", l.Ref.ID, err)
	}
	l.st.Posted = remember(l.st.Posted, bodyHash(body))
	return nil
}

// ---- inbound: tracker → Dash ----

// pull reads the tracker's changes since the cursor and applies them; it
// returns the cursor to read from next (zero: keep it).
func (w *worker) pull(ctx context.Context) (time.Time, error) {
	items, err := w.be.List(ctx, w.cfg.Cursor)
	if err != nil {
		return time.Time{}, fmt.Errorf("list cards: %w", err)
	}
	var latest time.Time
	var errs error
	for _, it := range items {
		if ctx.Err() != nil {
			return time.Time{}, ctx.Err()
		}
		if err := w.inbound(ctx, it); err != nil {
			errs = errors.Join(errs, fmt.Errorf("card %s: %w", it.Ref.ID, err))
		}
		if it.UpdatedAt.After(latest) {
			latest = it.UpdatedAt
		}
	}
	if errs != nil {
		return time.Time{}, errs // read them again next time
	}
	// A second back: changes made in the same instant as the latest are
	// read again (and skipped as unchanged) rather than missed.
	if !latest.IsZero() {
		latest = latest.Add(-time.Second)
	}
	return latest, nil
}

// inbound applies a card's changes to its todo, or makes a todo of a new
// card in one of the project's mapped columns.
func (w *worker) inbound(ctx context.Context, it Item) error {
	if it.Ref.ID == "" {
		return nil
	}
	id, ok := w.byExt[it.Ref.ID]
	if !ok {
		return w.importCard(ctx, it)
	}
	l := w.links[id]
	t, err := w.m.Store.GetTodo(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil // deleted in Dash: outbound deletes the card
	}
	if err != nil {
		return err
	}
	if it.Deleted {
		// The todo stays, Dash-only: a detached link keeps it from getting
		// a new card.
		delete(w.byExt, l.Ref.ID)
		l.Ref = Ref{Provider: l.Ref.Provider}
		if err := w.save(l); err != nil {
			return err
		}
		_, err := w.m.Store.SetExternalRef(id, nil)
		return err
	}
	ef := w.extFields(it)
	st := &l.st
	newComments, err := w.readComments(&t, l, it.Comments)
	if err != nil {
		return err
	}
	if ef.hash() == l.ExtHash && st.Ext.hash() == l.ExtHash {
		// Nothing changed there (or it is an echo of Dash's own push).
		if newComments || it.Version != l.ExtVersion {
			l.ExtVersion = it.Version
			return w.save(l)
		}
		return nil
	}
	df := w.dashFields(t)
	// dashWins: Dash changed the field too, after the tracker did (as of
	// before this sync's own writes to the todo).
	dashAt := t.UpdatedAt
	dashWins := func(dashChanged bool) bool { return dashChanged && dashAt.After(it.UpdatedAt) }

	var title, prompt *string
	var prio **int
	var notes []string
	if ef.Title != st.Ext.Title {
		if !dashWins(df.Title != st.Dash.Title) {
			title = &ef.Title
		}
		st.Ext.Title = ef.Title
	}
	if !samePriority(ef.Priority, st.Ext.Priority) {
		if !dashWins(!samePriority(df.Priority, st.Dash.Priority)) {
			p := ef.Priority
			prio = &p
		}
		st.Ext.Priority = ef.Priority
	}
	if ef.Body != st.Ext.Body {
		if !dashWins(df.Body != st.Dash.Body) {
			switch {
			case PromptEditable(t.Status):
				prompt = &ef.Body
			case t.Status == model.StatusInProgress || t.Status == model.StatusReview:
				// It waits until the todo is a draft, queued or failed
				// again (see settle): a running Claude keeps its task.
				notes = append(notes, "Dash will use the new description when the task is queued again: it can't change while the task is "+statusWord(t.Status)+".")
			}
		}
		st.Ext.Body = ef.Body
	}
	if title != nil || prompt != nil || prio != nil {
		nt, err := w.m.Store.UpdateTodo(id, func(x *model.Todo) error {
			if title != nil {
				x.Title = *title
			}
			if prio != nil {
				x.Priority = *prio
			}
			if prompt != nil && PromptEditable(x.Status) {
				x.Prompt = *prompt
				if x.SessionID != "" {
					x.Feedback = append(x.Feedback, descriptionNote(w.be.Name(), *prompt))
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		t = nt
		if title != nil {
			st.Dash.Title = t.Title
		}
		if prio != nil {
			st.Dash.Priority = t.Priority
		}
		if prompt != nil && t.Prompt == *prompt {
			st.Dash.Body = t.Prompt
		}
	}
	if !slices.Equal(ef.Deps, st.Ext.Deps) {
		if !dashWins(!slices.Equal(df.Deps, st.Dash.Deps)) {
			deps := w.todoIDs(ef.Deps)
			nt, err := w.m.Store.UpdateTodo(id, func(x *model.Todo) error {
				// Keep dependencies on Dash-only todos the card can't name.
				for _, d := range x.DependsOn {
					if l, ok := w.links[d]; !ok || l.Ref.ID == "" {
						deps = append(deps, d)
					}
				}
				x.DependsOn = deps
				return nil
			})
			if err == nil {
				t = nt
				st.Dash.Deps = w.dashFields(t).Deps
			} else if !errors.Is(err, store.ErrBadDependency) {
				return err
			}
		}
		st.Ext.Deps = ef.Deps
	}
	if ef.Column != st.Ext.Column {
		col, note, err := w.moveFromTracker(ctx, &t, l, ef.Column, dashWins(df.Column != st.Dash.Column))
		if err != nil {
			return err
		}
		st.Ext.Column = col
		if note != "" {
			notes = append(notes, note)
		}
	}
	l.ExtVersion = it.Version
	for _, n := range notes {
		if err := w.comment(ctx, l, n); err != nil {
			_ = w.save(l)
			return err
		}
	}
	return w.save(l)
}

// moveFromTracker applies a card's move to another column: a status the
// state machine (and InboundAllowed) lets a person pick is applied; any
// other move is undone on the card, with a note saying why. It returns
// the column the card ends up in.
func (w *worker) moveFromTracker(ctx context.Context, t *model.Todo, l *link, column string, dashWins bool) (string, string, error) {
	st := &l.st
	cur := w.cfg.ColumnFor(t.Status)
	target := w.cfg.StatusFor(column)
	switch {
	case target == "" || column == cur:
		return column, "", nil // an unmapped column, or the todo's own already
	case dashWins:
		return column, "", nil // outbound moves the card back to the todo's status
	}
	var reason string
	if InboundAllowed(t.Status, target) {
		nt, err := w.m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
			if !InboundAllowed(x.Status, target) {
				return model.ErrInvalidTransition{From: x.Status, To: target}
			}
			from := x.Status
			x.Status, x.Error = target, ""
			x.Paused, x.Waiting = false, ""
			if target == model.StatusQueued && (from == model.StatusReview || from == model.StatusFailed) {
				// As a rework: a person is involved again.
				x.AutoFixes, x.AutoMerge, x.SentBack = 0, false, ""
			}
			return nil
		})
		if err == nil {
			*t = nt
			st.Dash.Column, st.Status = w.cfg.ColumnFor(nt.Status), nt.Status
			if target == model.StatusQueued && w.m.Kick != nil {
				w.m.Kick()
			}
			return column, "", nil
		}
		if errors.Is(err, store.ErrReviewing) {
			reason = "Claude is reviewing it"
		} else if !errors.As(err, new(model.ErrInvalidTransition)) {
			return "", "", err
		}
	}
	if reason == "" {
		reason = fmt.Sprintf("Dash can't move a task that is %s to %s", statusWord(t.Status), statusWord(target))
		if !slices.Contains(inboundStatuses, target) {
			reason += " by hand: Dash moves it there itself"
		}
	}
	if cur == "" {
		return column, "", nil // the todo's status has no column: nowhere to move the card back to
	}
	if err := w.be.SetStatus(ctx, l.Ref, t.Status); err != nil {
		return "", "", fmt.Errorf("move card %s back: %w", l.Ref.ID, err)
	}
	return cur, fmt.Sprintf("Moved back to %s: %s.", cur, reason), nil
}

func statusWord(s model.TodoStatus) string { return strings.ReplaceAll(string(s), "_", " ") }

// readComments adds the card's new comments by people to the todo's chat;
// it reports whether there were any comments it hadn't seen. On a todo in
// review they are also rework feedback when the project wants that (see
// CommentFeedbacker): kept as notes, or sending it back to Claude at once.
func (w *worker) readComments(t *model.Todo, l *link, comments []Comment) (bool, error) {
	seen := false
	var feedback []string
	for _, c := range comments {
		if c.ID == "" || slices.Contains(l.st.Seen, c.ID) {
			continue
		}
		seen = true
		l.st.Seen = remember(l.st.Seen, c.ID)
		if slices.Contains(l.st.Posted, bodyHash(c.Body)) {
			continue // Dash's own
		}
		at := c.At
		if at.IsZero() {
			at = w.m.now()
		}
		who := c.Author
		if who == "" {
			who = "Someone"
		}
		msg := fmt.Sprintf("**%s** commented in %s:\n\n%s", who, label(w.be.Name()), c.Body)
		_ = w.m.Store.AppendTodoChat(t.ID, model.ChatMessage{Role: "user", Content: msg, At: &at})
		if strings.TrimSpace(c.Body) != "" {
			feedback = append(feedback, msg)
		}
	}
	policy := FeedbackChat
	if cf, ok := w.be.(CommentFeedbacker); ok {
		policy = cf.CommentFeedback()
	}
	if len(feedback) == 0 || policy == FeedbackChat || t.Status != model.StatusReview {
		return seen, nil
	}
	nt, err := w.m.Store.UpdateTodo(t.ID, func(x *model.Todo) error {
		if x.Status != model.StatusReview {
			return errSkip
		}
		x.Feedback = append(x.Feedback, feedback...)
		if policy == FeedbackRework && InboundAllowed(x.Status, model.StatusQueued) {
			// As the Rework button: a person is involved again.
			x.Status, x.Error = model.StatusQueued, ""
			x.Paused, x.Waiting = false, ""
			x.AutoFixes, x.AutoMerge, x.SentBack = 0, false, ""
		}
		return nil
	})
	switch {
	case errors.Is(err, errSkip), errors.Is(err, store.ErrReviewing):
		return seen, nil // Claude is reviewing it: the comments stay in the chat
	case err != nil:
		return seen, err
	}
	*t = nt
	if t.Status == model.StatusQueued && w.m.Kick != nil {
		w.m.Kick()
	}
	return seen, nil
}

// importCard makes a todo of a card Dash hasn't seen, when it sits in a
// column the project maps to draft or queued; cards elsewhere are left
// alone (Dash only starts work, it doesn't adopt work in other states).
func (w *worker) importCard(_ context.Context, it Item) error {
	if it.Deleted {
		return nil
	}
	status := w.cfg.StatusFor(it.Column)
	if status != model.StatusDraft && status != model.StatusQueued {
		return nil
	}
	ref := it.Ref
	if ref.Provider == "" {
		ref.Provider = w.be.Name()
	}
	prio := it.Priority
	if w.skips(FieldPriority) {
		prio = nil
	}
	nt := model.Todo{ProjectID: w.cfg.ProjectID, Title: it.Title, Prompt: it.Description, Priority: prio,
		DependsOn: w.todoIDs(it.DependsOn), ExternalRef: &ref}
	t, err := w.m.Store.CreateTodo(nt)
	if errors.Is(err, store.ErrBadDependency) {
		nt.DependsOn = nil
		t, err = w.m.Store.CreateTodo(nt)
	}
	if err != nil {
		return err
	}
	if status == model.StatusQueued {
		if q, err := w.m.Store.SetStatus(t.ID, model.StatusQueued, ""); err == nil {
			t = q
			if w.m.Kick != nil {
				w.m.Kick()
			}
		}
	}
	l := &link{TrackerLink: model.TrackerLink{TodoID: t.ID, ProjectID: t.ProjectID, Ref: ref, ExtVersion: it.Version}}
	l.st = linkState{Dash: w.dashFields(t), Ext: w.extFields(it), Status: t.Status}
	for _, c := range it.Comments {
		if c.ID != "" {
			l.st.Seen = remember(l.st.Seen, c.ID) // its history stays on the card
		}
	}
	return w.save(l)
}
