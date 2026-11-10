// Package trackertest is an in-memory tracker for tests of the sync engine
// and of code that configures board backends.
//
// A Provider is one fake tracker service with any number of boards; Register
// makes it a backend. A project's config picks its board ({"board": "x"})
// and its credentials must carry the board's token when one is set, so two
// projects can use different boards and accounts of one provider.
package trackertest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tracker"
)

// Config is a fake backend's settings.
type Config struct {
	Board string `json:"board"`
}

// Provider is a fake tracker service.
type Provider struct {
	Name string

	mu     sync.Mutex
	boards map[string]*board
	tokens map[string]string // board → the token it needs
	calls  []string
	last   time.Time
	fail   error
}

type board struct {
	cards map[string]*tracker.Item
	order []string
}

var seq struct {
	sync.Mutex
	n int
}

// Register makes a new provider and registers it as backend name; it is
// unregistered when the test ends.
func Register(t interface{ Cleanup(func()) }, name string) *Provider {
	p := &Provider{Name: name, boards: map[string]*board{}, tokens: map[string]string{}}
	tracker.Register(tracker.Registration{
		Name: name, Label: "Fake " + name,
		New: p.New,
		Fields: []tracker.Field{
			{Name: "board", Label: "Board", Required: true},
			{Name: "token", Label: "API token", Secret: true},
		},
		DefaultStatusMap: map[model.TodoStatus]string{
			model.StatusDraft: "Backlog", model.StatusQueued: "Todo", model.StatusInProgress: "Doing",
			model.StatusReview: "Review", model.StatusMerging: "Done", model.StatusMerged: "Done", model.StatusFailed: "Failed",
		},
		Board: func(raw json.RawMessage) string {
			var c Config
			_ = json.Unmarshal(raw, &c)
			return c.Board
		},
	})
	t.Cleanup(func() { tracker.Unregister(name) })
	return p
}

// RequireToken makes a board refuse backends without token.
func (p *Provider) RequireToken(boardName, token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokens[boardName] = token
}

// FailNext makes the next backend call fail with err.
func (p *Provider) FailNext(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fail = err
}

// Calls are the backend calls made so far, like "create b/c1",
// "update b/c1", "status b/c1 review", "comment b/c1", "delete b/c1".
func (p *Provider) Calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

// ResetCalls forgets the calls made so far.
func (p *Provider) ResetCalls() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = nil
}

func (p *Provider) boardLocked(name string) *board {
	b := p.boards[name]
	if b == nil {
		b = &board{cards: map[string]*tracker.Item{}}
		p.boards[name] = b
	}
	return b
}

// tick is a strictly increasing clock, so every change has its own time.
func (p *Provider) tickLocked() time.Time {
	now := time.Now()
	if !now.After(p.last) {
		now = p.last.Add(time.Microsecond)
	}
	p.last = now
	return now
}

func (p *Provider) touchLocked(it *tracker.Item) {
	it.UpdatedAt = p.tickLocked()
	n, _ := strconv.Atoi(it.Version)
	it.Version = strconv.Itoa(n + 1)
}

// ---- what people do on the fake tracker ----

// AddCard puts a new card on a board and returns its id.
func (p *Provider) AddCard(boardName string, it tracker.Item) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	seq.Lock()
	seq.n++
	id := fmt.Sprintf("c%d", seq.n)
	seq.Unlock()
	b := p.boardLocked(boardName)
	it.Ref = tracker.Ref{Provider: p.Name, ID: id, URL: "https://tracker.test/" + boardName + "/" + id}
	p.touchLocked(&it)
	b.cards[id] = &it
	b.order = append(b.order, id)
	return id
}

// Edit changes a card as a person would.
func (p *Provider) Edit(boardName, id string, fn func(*tracker.Item)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.boardLocked(boardName).cards[id]
	if it == nil {
		panic("no card " + id)
	}
	fn(it)
	p.touchLocked(it)
}

// Move puts a card in another column.
func (p *Provider) Move(boardName, id, column string) {
	p.Edit(boardName, id, func(it *tracker.Item) { it.Column = column })
}

// AddComment comments on a card as author.
func (p *Provider) AddComment(boardName, id, author, body string) {
	p.Edit(boardName, id, func(it *tracker.Item) {
		it.Comments = append(it.Comments, tracker.Comment{ID: fmt.Sprintf("%s-k%d", id, len(it.Comments)+1), Author: author, Body: body})
	})
}

// Card is a copy of a card (ok false: none).
func (p *Provider) Card(boardName, id string) (tracker.Item, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.boardLocked(boardName).cards[id]
	if it == nil {
		return tracker.Item{}, false
	}
	return clone(*it), true
}

// Cards are a board's cards (not deleted ones), oldest first.
func (p *Provider) Cards(boardName string) []tracker.Item {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.boardLocked(boardName)
	var out []tracker.Item
	for _, id := range b.order {
		if it := b.cards[id]; !it.Deleted {
			out = append(out, clone(*it))
		}
	}
	return out
}

func clone(it tracker.Item) tracker.Item {
	it.DependsOn = append([]string(nil), it.DependsOn...)
	it.Comments = append([]tracker.Comment(nil), it.Comments...)
	if it.Priority != nil {
		p := *it.Priority
		it.Priority = &p
	}
	return it
}

// ---- the backend ----

// New is the provider's factory.
func (p *Provider) New(cfg tracker.ProjectConfig, creds tracker.Credentials) (tracker.Backend, error) {
	var c Config
	if len(cfg.Config) > 0 {
		if err := json.Unmarshal(cfg.Config, &c); err != nil {
			return nil, err
		}
	}
	if c.Board == "" {
		return nil, errors.New("fake: no board")
	}
	return &Backend{p: p, board: c.Board, cfg: cfg, token: creds["token"]}, nil
}

// Backend is one project's board on the provider.
type Backend struct {
	p     *Provider
	board string
	cfg   tracker.ProjectConfig
	token string
}

func (b *Backend) Name() string { return b.p.Name }

// Board is the board this backend works on.
func (b *Backend) Board() string { return b.board }

func (b *Backend) begin(call string) (*board, error) {
	b.p.mu.Lock()
	if want, ok := b.p.tokens[b.board]; ok && want != b.token {
		b.p.mu.Unlock()
		return nil, errors.New("fake: unauthorized")
	}
	if err := b.p.fail; err != nil {
		b.p.fail = nil
		b.p.mu.Unlock()
		return nil, err
	}
	if call != "" {
		b.p.calls = append(b.p.calls, call)
	}
	return b.p.boardLocked(b.board), nil
}

func (b *Backend) card(bd *board, ref tracker.Ref) (*tracker.Item, error) {
	it := bd.cards[ref.ID]
	if it == nil || it.Deleted {
		return nil, fmt.Errorf("fake: no card %s on board %s", ref.ID, b.board)
	}
	return it, nil
}

func (b *Backend) Test(ctx context.Context) error {
	_, err := b.begin("")
	if err == nil {
		b.p.mu.Unlock()
	}
	return err
}

func (b *Backend) Columns(ctx context.Context) ([]tracker.Column, error) {
	var out []tracker.Column
	for _, s := range model.Statuses {
		if c := b.cfg.ColumnFor(s); c != "" && !slices.ContainsFunc(out, func(x tracker.Column) bool { return x.ID == c }) {
			out = append(out, tracker.Column{ID: c, Name: c})
		}
	}
	return out, nil
}

func (b *Backend) List(_ context.Context, since time.Time) ([]tracker.Item, error) {
	bd, err := b.begin("")
	if err != nil {
		return nil, err
	}
	defer b.p.mu.Unlock()
	var out []tracker.Item
	for _, id := range bd.order {
		if it := bd.cards[id]; since.IsZero() || it.UpdatedAt.After(since) {
			out = append(out, clone(*it))
		}
	}
	return out, nil
}

func (b *Backend) Create(_ context.Context, it tracker.Item) (tracker.Ref, error) {
	if _, err := b.begin(""); err != nil {
		return tracker.Ref{}, err
	}
	b.p.mu.Unlock()
	id := b.p.AddCard(b.board, clone(it))
	b.p.mu.Lock()
	b.p.calls = append(b.p.calls, "create "+b.board+"/"+id)
	ref := b.p.boards[b.board].cards[id].Ref
	b.p.mu.Unlock()
	return ref, nil
}

func (b *Backend) Update(_ context.Context, ref tracker.Ref, it tracker.Item) error {
	bd, err := b.begin("update " + b.board + "/" + ref.ID)
	if err != nil {
		return err
	}
	defer b.p.mu.Unlock()
	c, err := b.card(bd, ref)
	if err != nil {
		return err
	}
	c.Title, c.Description, c.Priority, c.DependsOn = it.Title, it.Description, it.Priority, append([]string(nil), it.DependsOn...)
	b.p.touchLocked(c)
	return nil
}

func (b *Backend) SetStatus(_ context.Context, ref tracker.Ref, status model.TodoStatus) error {
	bd, err := b.begin("status " + b.board + "/" + ref.ID + " " + string(status))
	if err != nil {
		return err
	}
	defer b.p.mu.Unlock()
	c, err := b.card(bd, ref)
	if err != nil {
		return err
	}
	col := b.cfg.ColumnFor(status)
	if col == "" {
		return nil
	}
	c.Column = col
	b.p.touchLocked(c)
	return nil
}

func (b *Backend) Comment(_ context.Context, ref tracker.Ref, markdown string) error {
	bd, err := b.begin("comment " + b.board + "/" + ref.ID)
	if err != nil {
		return err
	}
	defer b.p.mu.Unlock()
	c, err := b.card(bd, ref)
	if err != nil {
		return err
	}
	c.Comments = append(c.Comments, tracker.Comment{ID: fmt.Sprintf("%s-k%d", ref.ID, len(c.Comments)+1), Author: "Dash", Body: markdown})
	b.p.touchLocked(c)
	return nil
}

func (b *Backend) Delete(_ context.Context, ref tracker.Ref) error {
	bd, err := b.begin("delete " + b.board + "/" + ref.ID)
	if err != nil {
		return err
	}
	defer b.p.mu.Unlock()
	c, err := b.card(bd, ref)
	if err != nil {
		return err
	}
	c.Deleted = true
	b.p.touchLocked(c)
	return nil
}
