// Package favro is the Favro board backend (https://favro.dev): a project
// picks one Favro board (a widget, in an organization and optionally a
// collection) and its cards become the project's todos, synced both ways
// by the tracker package's engine.
//
// Each project builds its own Backend from its own config and credentials
// (the user's email in the config, the API token in the credentials), so
// projects on different boards or accounts never share anything but the
// rate-limit backoff of an account they both use.
//
// Fields:
//
//	card name             ↔ title
//	detailed description  ↔ prompt (markdown)
//	column                ↔ status, through the project's status mapping
//	                        (column names or ids, matched ignoring case)
//	Number custom field   ↔ priority, when one is configured, or tags
//	                        like "P1" with a priority tag prefix; else Dash's
//	                        priority is left alone
//	dependencies          ↔ DependsOn (between linked cards)
//	comments              → the todo's chat (and rework feedback, if the
//	                        project wants); Dash comments PR links, summaries
//	                        and failures
//
// Favro has no change feed (nor change times on cards), so List reads the
// board and tells changes from what it saw before; a changed card's
// UpdatedAt is the previous read, the earliest it can have changed, so a
// conflict with an edit made in Dash in the meantime goes to Dash's.
// Webhooks (registered when Dash has a public URL) make the engine read at
// once, which keeps that window short.
package favro

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/tracker"
)

// Name is the backend's registered name.
const Name = "favro"

// Config is a project's Favro settings (its credentials, the API token,
// are kept apart).
type Config struct {
	// Email is the Favro user the API token belongs to.
	Email          string `json:"email"`
	OrganizationID string `json:"organization_id"`
	// CollectionID only narrows the boards to pick from.
	CollectionID   string `json:"collection_id,omitempty"`
	WidgetCommonID string `json:"widget_common_id"`
	// Tag, when set, limits the project to the cards with this tag; the
	// cards Dash makes get it.
	Tag string `json:"tag,omitempty"`
	// PriorityField is a Number custom field holding the priority.
	PriorityField string `json:"priority_field,omitempty"`
	// PriorityTagPrefix: tags like "P1", "P2" hold the priority instead.
	PriorityTagPrefix string `json:"priority_tag_prefix,omitempty"`
	// CommentFeedback is what comments on a todo in review do (a
	// tracker.Feedback* policy: "", "note" or "rework").
	CommentFeedback string `json:"comment_feedback,omitempty"`
	// BaseURL replaces Favro's API address; only addresses AllowBaseURL
	// allowed (tests), so a project's settings can't send its token
	// elsewhere.
	BaseURL string `json:"base_url,omitempty"`
}

var (
	allowedBases sync.Map
	defaultBase  atomic.Value // string: replaces DefaultBaseURL (tests)
)

// AllowBaseURL lets configs use another API address, e.g. a test server's.
func AllowBaseURL(u string) { allowedBases.Store(strings.TrimRight(u, "/"), true) }

// SetDefaultBaseURL makes every project without a base_url use another API
// address: Dash's end-to-end tests point it at a fake Favro (operators
// never need it).
func SetDefaultBaseURL(u string) { defaultBase.Store(strings.TrimRight(u, "/")) }

func (c Config) baseURL() (string, error) {
	if c.BaseURL == "" {
		if u, _ := defaultBase.Load().(string); u != "" {
			return u, nil
		}
		return DefaultBaseURL, nil
	}
	u := strings.TrimRight(c.BaseURL, "/")
	if _, ok := allowedBases.Load(u); !ok {
		return "", errors.New("favro: base_url isn't allowed")
	}
	return u, nil
}

func parseConfig(raw json.RawMessage) (Config, error) {
	var c Config
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("favro: settings: %w", err)
		}
	}
	c.Email = strings.TrimSpace(c.Email)
	c.Tag = strings.TrimSpace(c.Tag)
	switch c.CommentFeedback {
	case tracker.FeedbackChat, tracker.FeedbackNote, tracker.FeedbackRework:
	default:
		return c, fmt.Errorf("favro: unknown comment feedback %q", c.CommentFeedback)
	}
	return c, nil
}

func init() {
	tracker.Register(Registration())
}

// Registration is the backend's registry entry.
func Registration() tracker.Registration {
	return tracker.Registration{
		Name: Name, Label: "Favro",
		New: func(cfg tracker.ProjectConfig, creds tracker.Credentials) (tracker.Backend, error) {
			return New(cfg, creds)
		},
		Fields: []tracker.Field{
			{Name: "email", Label: "Favro user email", Hint: "the account the API token belongs to", Placeholder: "you@example.com", Required: true},
			{Name: "token", Label: "API token", Hint: "Favro → My profile → API tokens", Secret: true, Required: true},
			{Name: "organization_id", Label: "Organization", Type: "select", Dynamic: true, DependsOn: []string{"email", "token"}, Required: true},
			{Name: "collection_id", Label: "Collection", Hint: "narrows the boards", Type: "select", Dynamic: true, DependsOn: []string{"organization_id"}},
			{Name: "widget_common_id", Label: "Board", Type: "select", Dynamic: true, DependsOn: []string{"organization_id", "collection_id"}, Required: true},
			{Name: "tag", Label: "Only cards tagged", Hint: "optional: Dash takes only these cards, and tags the ones it makes", Placeholder: "Claude"},
			{Name: "priority_field", Label: "Priority field", Hint: "optional: a Number custom field", Type: "select", Dynamic: true, DependsOn: []string{"organization_id"}},
			{Name: "priority_tag_prefix", Label: "Priority tags", Hint: "optional, instead: tags like P1, P2", Placeholder: "P"},
			{Name: "comment_feedback", Label: "Comments on a card in review", Type: "select", Options: []tracker.Option{
				{Value: tracker.FeedbackChat, Label: "Go to the task's chat"},
				{Value: tracker.FeedbackNote, Label: "Are also kept as rework notes"},
				{Value: tracker.FeedbackRework, Label: "Send the task back to Claude"},
			}},
		},
		DefaultStatusMap: map[model.TodoStatus]string{
			model.StatusDraft: "Backlog", model.StatusQueued: "To do", model.StatusInProgress: "Doing",
			model.StatusReview: "Review", model.StatusMerged: "Done",
		},
		Board: func(raw json.RawMessage) string {
			c, _ := parseConfig(raw)
			return c.OrganizationID + "/" + c.WidgetCommonID
		},
		Options:     Options,
		AutoWebhook: true,
	}
}

// client builds an API client from a (possibly partial) config.
func client(cfg Config, creds tracker.Credentials, needOrg bool) (*Client, error) {
	token := strings.TrimSpace(creds["token"])
	var missing []string
	if cfg.Email == "" {
		missing = append(missing, "user email")
	}
	if token == "" {
		missing = append(missing, "API token")
	}
	if needOrg && cfg.OrganizationID == "" {
		missing = append(missing, "organization")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("favro: set the %s", strings.Join(missing, ", "))
	}
	base, err := cfg.baseURL()
	if err != nil {
		return nil, err
	}
	return NewClient(base, cfg.Email, token, cfg.OrganizationID), nil
}

// Options lists the choices of the settings' dropdowns.
func Options(ctx context.Context, pc tracker.ProjectConfig, creds tracker.Credentials, field string) ([]tracker.Option, error) {
	cfg, err := parseConfig(pc.Config)
	if err != nil {
		return nil, err
	}
	c, err := client(cfg, creds, field != "organization_id")
	if err != nil {
		return nil, err
	}
	var out []tracker.Option
	switch field {
	case "organization_id":
		orgs, err := listAll[organization](ctx, c, "/organizations", nil)
		if err != nil {
			return nil, err
		}
		for _, o := range orgs {
			out = append(out, tracker.Option{Value: o.OrganizationID, Label: o.Name})
		}
	case "collection_id":
		cols, err := listAll[collection](ctx, c, "/collections", nil)
		if err != nil {
			return nil, err
		}
		out = append(out, tracker.Option{Value: "", Label: "All collections"})
		for _, x := range cols {
			if !x.Archived {
				out = append(out, tracker.Option{Value: x.CollectionID, Label: x.Name})
			}
		}
	case "widget_common_id":
		q := url.Values{}
		if cfg.CollectionID != "" {
			q.Set("collectionId", cfg.CollectionID)
		}
		ws, err := listAll[widget](ctx, c, "/widgets", q)
		if err != nil {
			return nil, err
		}
		for _, w := range ws {
			if !w.Archived && (w.Type == "" || w.Type == "board") {
				out = append(out, tracker.Option{Value: w.WidgetCommonID, Label: w.Name})
			}
		}
	case "priority_field":
		fs, err := listAll[customField](ctx, c, "/customfields", nil)
		if err != nil {
			return nil, err
		}
		out = append(out, tracker.Option{Value: "", Label: "None"})
		for _, f := range fs {
			if strings.EqualFold(f.Type, "Number") && (f.Enabled == nil || *f.Enabled) {
				out = append(out, tracker.Option{Value: f.CustomFieldID, Label: f.Name})
			}
		}
	default:
		return nil, tracker.ErrNoOptions
	}
	slices.SortStableFunc(out, func(a, b tracker.Option) int {
		if (a.Value == "") != (b.Value == "") { // "none"/"all" first
			if a.Value == "" {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Label), strings.ToLower(b.Label))
	})
	return out, nil
}

// ---- the backend ----

// Backend is one project's Favro board.
type Backend struct {
	c   *Client
	cfg Config
	pc  tracker.ProjectConfig

	// Caches, owned by the engine's goroutine (one call at a time).
	columns  []column
	tagID    string            // the filter tag's id, once found
	tagNames map[string]string // tag id → name (priority tags)
	users    map[string]string // user id → name
	commons  map[string]string // card id → card common id
	seen     map[string]*seenCard
	lastRead time.Time // when List last read the board
	now      func() time.Time
}

// seenCard is what List last saw of a card.
type seenCard struct {
	fp       string
	at       time.Time
	comments int
	read     []tracker.Comment
}

// New builds a project's backend.
func New(pc tracker.ProjectConfig, creds tracker.Credentials) (*Backend, error) {
	cfg, err := parseConfig(pc.Config)
	if err != nil {
		return nil, err
	}
	c, err := client(cfg, creds, true)
	if err != nil {
		return nil, err
	}
	if cfg.WidgetCommonID == "" {
		return nil, errors.New("favro: pick the board")
	}
	return &Backend{c: c, cfg: cfg, pc: pc, commons: map[string]string{}, seen: map[string]*seenCard{}, now: time.Now}, nil
}

func (b *Backend) Name() string { return Name }

// CommentFeedback is the project's choice for comments on cards in review.
func (b *Backend) CommentFeedback() string { return b.cfg.CommentFeedback }

// Test checks the token, organization and board by reading the columns.
func (b *Backend) Test(ctx context.Context) error {
	cols, err := b.loadColumns(ctx)
	if err == nil && len(cols) == 0 {
		err = errors.New("favro: the board has no columns (is it a board, not a backlog?)")
	}
	return err
}

// Columns are the board's columns, left to right.
func (b *Backend) Columns(ctx context.Context) ([]tracker.Column, error) {
	cols, err := b.loadColumns(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tracker.Column, 0, len(cols))
	for _, c := range cols {
		// The mapping is by name (columns keep their ids when renamed, but
		// names read better); ids work in the mapping too.
		out = append(out, tracker.Column{ID: c.Name, Name: c.Name})
	}
	return out, nil
}

func (b *Backend) loadColumns(ctx context.Context) ([]column, error) {
	cols, err := listAll[column](ctx, b.c, "/columns", url.Values{"widgetCommonId": {b.cfg.WidgetCommonID}})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(cols, func(a, b column) int {
		switch {
		case a.Position < b.Position:
			return -1
		case a.Position > b.Position:
			return 1
		}
		return 0
	})
	b.columns = cols
	return cols, nil
}

// norm folds a column name for matching: lower case, letters and digits only.
func norm(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// rawColumn is how the project's mapping names a column: the mapped value
// that is its id or its name, else its name.
func (b *Backend) rawColumn(id string) string {
	name := ""
	for _, c := range b.columns {
		if c.ColumnID == id {
			name = c.Name
			break
		}
	}
	for _, s := range model.Statuses {
		v := b.pc.StatusMap[s]
		if v != "" && (v == id || name != "" && norm(v) == norm(name)) {
			return v
		}
	}
	if name != "" {
		return name
	}
	return id
}

// columnID finds the column a mapping value (a name or an id) means.
func (b *Backend) columnID(ctx context.Context, value string) (string, error) {
	find := func() string {
		for _, c := range b.columns {
			if c.ColumnID == value {
				return c.ColumnID
			}
		}
		for _, c := range b.columns {
			if norm(c.Name) == norm(value) {
				return c.ColumnID
			}
		}
		return ""
	}
	if id := find(); id != "" {
		return id, nil
	}
	if _, err := b.loadColumns(ctx); err != nil { // renamed or added since
		return "", err
	}
	if id := find(); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("favro: no column %q on the board: check the project's status mapping", value)
}

// filterTag is the id of the project's tag ("" without one, or while no
// card has it: then no card is the project's).
func (b *Backend) filterTag(ctx context.Context) (string, error) {
	if b.cfg.Tag == "" || b.tagID != "" {
		return b.tagID, nil
	}
	tags, err := listAll[tag](ctx, b.c, "/tags", url.Values{"name": {b.cfg.Tag}})
	if err != nil {
		return "", err
	}
	for _, t := range tags {
		if strings.EqualFold(t.Name, b.cfg.Tag) {
			b.tagID = t.TagID
		}
	}
	return b.tagID, nil
}

func (b *Backend) loadTagNames(ctx context.Context) error {
	tags, err := listAll[tag](ctx, b.c, "/tags", nil)
	if err != nil {
		return err
	}
	b.tagNames = map[string]string{}
	for _, t := range tags {
		b.tagNames[t.TagID] = t.Name
	}
	return nil
}

func (b *Backend) userName(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	if _, ok := b.users[id]; !ok {
		us, err := listAll[user](ctx, b.c, "/users", nil)
		if err == nil {
			b.users = map[string]string{}
			for _, u := range us {
				b.users[u.UserID] = u.Name
			}
		}
		if _, ok := b.users[id]; !ok {
			if b.users == nil {
				b.users = map[string]string{}
			}
			b.users[id] = "" // don't ask again for a user we can't see
		}
	}
	return b.users[id]
}

// ---- priority ----

// priorityTag reads a priority tag name ("P2" → 2).
func (b *Backend) priorityTag(name string) (int, bool) {
	p := b.cfg.PriorityTagPrefix
	if p == "" || len(name) <= len(p) || !strings.EqualFold(name[:len(p)], p) {
		return 0, false
	}
	n, err := strconv.Atoi(name[len(p):])
	return n, err == nil
}

func (b *Backend) priorityOf(ctx context.Context, cd card) (*int, error) {
	switch {
	case b.cfg.PriorityField != "":
		for _, f := range cd.CustomFields {
			if f.CustomFieldID == b.cfg.PriorityField && f.Total != nil {
				n := int(math.Round(*f.Total))
				return &n, nil
			}
		}
	case b.cfg.PriorityTagPrefix != "":
		var best *int
		for _, id := range cd.Tags {
			name, ok := b.tagNames[id]
			if !ok {
				if err := b.loadTagNames(ctx); err != nil {
					return nil, err
				}
				name = b.tagNames[id]
				if _, ok := b.tagNames[id]; !ok {
					b.tagNames[id] = ""
				}
			}
			if n, ok := b.priorityTag(name); ok && (best == nil || n < *best) {
				best = &n
			}
		}
		return best, nil
	}
	return nil, nil
}

// Skips: without a priority field or tags, Dash's priority is its own.
func (b *Backend) Skips() []string {
	if b.cfg.PriorityField == "" && b.cfg.PriorityTagPrefix == "" {
		return []string{tracker.FieldPriority}
	}
	return nil
}

// ---- reading ----

// List reads the board and returns the cards changed since then (and the
// ones gone from it since the last read, as deleted).
func (b *Backend) List(ctx context.Context, since time.Time) ([]tracker.Item, error) {
	if _, err := b.loadColumns(ctx); err != nil {
		return nil, err
	}
	tagID, err := b.filterTag(ctx)
	if err != nil {
		return nil, err
	}
	cards, err := listAll[card](ctx, b.c, "/cards", url.Values{
		"widgetCommonId": {b.cfg.WidgetCommonID}, "unique": {"true"}, "descriptionFormat": {"markdown"},
	})
	if err != nil {
		return nil, err
	}
	now := b.now()
	// A card that changed since the last read changed after it: date it
	// then, the earliest it can have (Favro doesn't say). A conflict with
	// an edit in Dash meanwhile goes to Dash's, whose time is exact.
	changedAt := b.lastRead
	if changedAt.IsZero() {
		changedAt = now
	}
	byID := map[string]bool{}
	byCommon := map[string]string{}
	for _, cd := range cards {
		byID[cd.CardID] = true
		byCommon[cd.CardCommonID] = cd.CardID
		b.commons[cd.CardID] = cd.CardCommonID
	}
	present := map[string]bool{}
	var out []tracker.Item
	for _, cd := range cards {
		if cd.Archived || cd.WidgetCommonID != "" && cd.WidgetCommonID != b.cfg.WidgetCommonID {
			continue
		}
		present[cd.CardID] = true
		if b.cfg.Tag != "" && (tagID == "" || !slices.Contains(cd.Tags, tagID)) {
			continue // not the project's (it stays linked if it was, without syncing)
		}
		it, err := b.item(ctx, cd, byID, byCommon)
		if err != nil {
			return nil, err
		}
		fp := fingerprint(it, cd.NumComments)
		s := b.seen[cd.CardID]
		if s == nil || s.fp != fp {
			if cd.NumComments > 0 && (s == nil || s.comments != cd.NumComments) {
				if it.Comments, err = b.comments(ctx, cd.CardCommonID); err != nil {
					return nil, err
				}
			} else if s != nil {
				it.Comments = s.read
			}
			s = &seenCard{fp: fp, at: changedAt, comments: cd.NumComments, read: it.Comments}
			b.seen[cd.CardID] = s
		} else {
			it.Comments = s.read
		}
		it.UpdatedAt = s.at
		if since.IsZero() || s.at.After(since) {
			out = append(out, it)
		}
	}
	for id := range b.seen {
		if !present[id] {
			delete(b.seen, id)
			out = append(out, tracker.Item{Ref: tracker.Ref{Provider: Name, ID: id}, Deleted: true, UpdatedAt: changedAt})
		}
	}
	b.lastRead = now
	return out, nil
}

func fingerprint(it tracker.Item, comments int) string {
	b, _ := json.Marshal(struct {
		T, D, C  string
		P        *int
		Deps     []string
		Comments int
	}{it.Title, it.Description, it.Column, it.Priority, it.DependsOn, comments})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

// item is a card's intent fields. Dependencies name cards by their id on
// this board (Favro may give common ids).
func (b *Backend) item(ctx context.Context, cd card, byID map[string]bool, byCommon map[string]string) (tracker.Item, error) {
	it := tracker.Item{
		Ref:         tracker.Ref{Provider: Name, ID: cd.CardID},
		Title:       cd.Name,
		Description: strings.TrimSpace(cd.DetailedDescription),
		Column:      b.rawColumn(cd.ColumnID),
	}
	var err error
	if it.Priority, err = b.priorityOf(ctx, cd); err != nil {
		return it, err
	}
	for _, d := range cd.Dependencies {
		if d.IsBefore || d.CardID == "" {
			continue
		}
		id := d.CardID
		if !byID[id] {
			if x, ok := byCommon[id]; ok {
				id = x
			}
		}
		if !slices.Contains(it.DependsOn, id) {
			it.DependsOn = append(it.DependsOn, id)
		}
	}
	slices.Sort(it.DependsOn)
	return it, nil
}

func (b *Backend) comments(ctx context.Context, common string) ([]tracker.Comment, error) {
	cs, err := listAll[comment](ctx, b.c, "/comments", url.Values{"cardCommonId": {common}})
	if err != nil {
		return nil, err
	}
	out := make([]tracker.Comment, 0, len(cs))
	for _, c := range cs {
		at, _ := time.Parse(time.RFC3339Nano, c.Created)
		out = append(out, tracker.Comment{ID: c.CommentID, Author: b.userName(ctx, c.UserID), Body: c.Comment, At: at})
	}
	slices.SortStableFunc(out, func(a, b tracker.Comment) int { return a.At.Compare(b.At) })
	return out, nil
}

func (b *Backend) getCard(ctx context.Context, id string) (card, error) {
	var cd card
	err := b.c.do(ctx, http.MethodGet, "/cards/"+url.PathEscape(id), url.Values{"descriptionFormat": {"markdown"}}, nil, &cd)
	if err == nil && cd.CardCommonID != "" {
		b.commons[id] = cd.CardCommonID
	}
	return cd, err
}

// ---- writing ----

// priorityFields is the custom field value setting p (nil: none).
func (b *Backend) priorityFields(p *int) []map[string]any {
	if b.cfg.PriorityField == "" {
		return nil
	}
	var total any // nil clears it
	if p != nil {
		total = *p
	}
	return []map[string]any{{"customFieldId": b.cfg.PriorityField, "total": total}}
}

func (b *Backend) priorityTagName(p *int) string {
	if b.cfg.PriorityTagPrefix == "" || p == nil {
		return ""
	}
	return b.cfg.PriorityTagPrefix + strconv.Itoa(*p)
}

// Create makes a card in the column it.Column means (the board's first
// column when the status is unmapped).
func (b *Backend) Create(ctx context.Context, it tracker.Item) (tracker.Ref, error) {
	var colID string
	var err error
	if it.Column != "" {
		colID, err = b.columnID(ctx, it.Column)
	} else {
		if len(b.columns) == 0 {
			_, err = b.loadColumns(ctx)
		}
		if err == nil && len(b.columns) > 0 {
			colID = b.columns[0].ColumnID
		}
	}
	if err != nil {
		return tracker.Ref{}, err
	}
	body := map[string]any{
		"name": it.Title, "widgetCommonId": b.cfg.WidgetCommonID,
		"detailedDescription": it.Description, "descriptionFormat": "markdown",
	}
	if colID != "" {
		body["columnId"] = colID
	}
	var tags []string
	if b.cfg.Tag != "" {
		tags = append(tags, b.cfg.Tag)
	}
	if t := b.priorityTagName(it.Priority); t != "" {
		tags = append(tags, t)
	}
	if len(tags) > 0 {
		body["tags"] = tags
	}
	if f := b.priorityFields(it.Priority); f != nil && it.Priority != nil {
		body["customFields"] = f
	}
	var cd card
	if err := b.c.do(ctx, http.MethodPost, "/cards", nil, body, &cd); err != nil {
		return tracker.Ref{}, err
	}
	if cd.CardID == "" {
		return tracker.Ref{}, errors.New("favro: the new card has no id")
	}
	b.commons[cd.CardID] = cd.CardCommonID
	ref := tracker.Ref{Provider: Name, ID: cd.CardID}
	if err := b.setDeps(ctx, cd, it.DependsOn); err != nil {
		return ref, err
	}
	return ref, nil
}

// Update sets a card's name, description, priority and dependencies.
func (b *Backend) Update(ctx context.Context, ref tracker.Ref, it tracker.Item) error {
	cur, err := b.getCard(ctx, ref.ID)
	if err != nil {
		return err
	}
	body := map[string]any{"name": it.Title, "detailedDescription": it.Description, "descriptionFormat": "markdown"}
	if f := b.priorityFields(it.Priority); f != nil {
		body["customFields"] = f
	}
	if b.cfg.PriorityTagPrefix != "" {
		want := b.priorityTagName(it.Priority)
		if b.tagNames == nil {
			if err := b.loadTagNames(ctx); err != nil {
				return err
			}
		}
		var remove []string
		has := false
		for _, id := range cur.Tags {
			name := b.tagNames[id]
			if _, ok := b.priorityTag(name); ok {
				if strings.EqualFold(name, want) {
					has = true
				} else {
					remove = append(remove, name)
				}
			}
		}
		if len(remove) > 0 {
			body["removeTags"] = remove
		}
		if want != "" && !has {
			body["addTags"] = []string{want}
		}
	}
	if err := b.c.do(ctx, http.MethodPut, "/cards/"+url.PathEscape(ref.ID), nil, body, nil); err != nil {
		return err
	}
	return b.setDeps(ctx, cur, it.DependsOn)
}

// setDeps makes the cards cd depends on exactly want (card ids).
func (b *Backend) setDeps(ctx context.Context, cd card, want []string) error {
	var have []string
	for _, d := range cd.Dependencies {
		if !d.IsBefore && d.CardID != "" {
			have = append(have, d.CardID)
		}
	}
	matches := func(list []string, id string) bool {
		return slices.ContainsFunc(list, func(x string) bool { return x == id || b.commons[x] == id || b.commons[id] == x })
	}
	path := "/cards/" + url.PathEscape(cd.CardID) + "/dependencies"
	for _, id := range want {
		if !matches(have, id) {
			if err := b.c.do(ctx, http.MethodPost, path, nil, map[string]any{"dependsOnCardId": id, "isBefore": false}, nil); err != nil {
				return fmt.Errorf("add dependency: %w", err)
			}
		}
	}
	for _, id := range have {
		if !matches(want, id) {
			if err := b.c.do(ctx, http.MethodDelete, path+"/"+url.PathEscape(id), nil, nil, nil); err != nil && !IsNotFound(err) {
				return fmt.Errorf("remove dependency: %w", err)
			}
		}
	}
	return nil
}

// SetStatus moves a card to the column the project maps status to (an
// unmapped status leaves it).
func (b *Backend) SetStatus(ctx context.Context, ref tracker.Ref, status model.TodoStatus) error {
	col := b.pc.ColumnFor(status)
	if col == "" {
		return nil
	}
	id, err := b.columnID(ctx, col)
	if err != nil {
		return err
	}
	return b.c.do(ctx, http.MethodPut, "/cards/"+url.PathEscape(ref.ID), nil,
		map[string]any{"widgetCommonId": b.cfg.WidgetCommonID, "columnId": id}, nil)
}

// Comment adds a comment to a card.
func (b *Backend) Comment(ctx context.Context, ref tracker.Ref, markdown string) error {
	common := b.commons[ref.ID]
	if common == "" {
		cd, err := b.getCard(ctx, ref.ID)
		if err != nil {
			return err
		}
		common = cd.CardCommonID
	}
	return b.c.do(ctx, http.MethodPost, "/comments", nil, map[string]any{"cardCommonId": common, "comment": markdown}, nil)
}

// Delete archives a card (Favro keeps it, out of the board's way).
func (b *Backend) Delete(ctx context.Context, ref tracker.Ref) error {
	err := b.c.do(ctx, http.MethodPut, "/cards/"+url.PathEscape(ref.ID), nil, map[string]any{"archive": true}, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// ---- webhooks ----

// hookName names the project's webhook, so it is found again.
func (b *Backend) hookName() string { return "Dash project " + b.pc.ProjectID }

// RegisterWebhook replaces the project's webhook on its board.
func (b *Backend) RegisterWebhook(ctx context.Context, url, secret string) error {
	if err := b.UnregisterWebhook(ctx, url); err != nil {
		return err
	}
	return b.c.do(ctx, http.MethodPost, "/webhooks", nil, map[string]any{
		"widgetCommonId": b.cfg.WidgetCommonID, "name": b.hookName(), "postToUrl": url, "secret": secret,
		"options": map[string]any{"notifications": webhookNotifications},
	}, nil)
}

// UnregisterWebhook removes the project's webhooks from its board (the
// ones to url, or by its name, from an earlier address).
func (b *Backend) UnregisterWebhook(ctx context.Context, hookURL string) error {
	hooks, err := listAll[webhook](ctx, b.c, "/webhooks", url.Values{"widgetCommonId": {b.cfg.WidgetCommonID}})
	if err != nil {
		return err
	}
	for _, h := range hooks {
		if h.WidgetCommonID != "" && h.WidgetCommonID != b.cfg.WidgetCommonID {
			continue
		}
		if h.PostToURL == hookURL || h.Name == b.hookName() {
			if err := b.c.do(ctx, http.MethodDelete, "/webhooks/"+url.PathEscape(h.WebhookID), nil, nil, nil); err != nil && !IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

// SignatureHeader carries Favro's webhook signature.
const SignatureHeader = "X-Favro-Webhook"

// Sign is a webhook call's signature: the base64 HMAC-SHA1 of its
// payloadId followed by the webhook's URL, keyed with its secret.
func Sign(secret, payloadID, hookURL string) string {
	m := hmac.New(sha1.New, []byte(secret))
	m.Write([]byte(payloadID + hookURL))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// VerifyWebhook checks a webhook call's signature with the project's
// secret and returns the card it is about. It only reads the config, so
// it is safe beside the engine's calls.
func (b *Backend) VerifyWebhook(r *http.Request, body []byte, secret string) ([]string, error) {
	var call webhookCall
	if err := json.Unmarshal(body, &call); err != nil {
		return nil, errors.New("favro webhook: not JSON")
	}
	given := r.Header.Get(SignatureHeader)
	if given == "" || call.PayloadID == "" {
		return nil, errors.New("favro webhook: unsigned")
	}
	hookURL := b.pc.WebhookURL
	if hookURL == "" && call.Hook != nil {
		hookURL = call.Hook.URL // the secret still has to match
	}
	if hookURL == "" {
		return nil, errors.New("favro webhook: no address to check the signature with")
	}
	want := Sign(secret, call.PayloadID, hookURL)
	if !hmac.Equal([]byte(given), []byte(want)) {
		return nil, errors.New("favro webhook: bad signature")
	}
	if call.Card != nil && call.Card.CardID != "" {
		return []string{call.Card.CardID}, nil
	}
	return nil, nil
}

var (
	_ tracker.Backend           = (*Backend)(nil)
	_ tracker.Webhooker         = (*Backend)(nil)
	_ tracker.WebhookRegistrar  = (*Backend)(nil)
	_ tracker.Tester            = (*Backend)(nil)
	_ tracker.ColumnLister      = (*Backend)(nil)
	_ tracker.CommentFeedbacker = (*Backend)(nil)
	_ tracker.FieldSkipper      = (*Backend)(nil)
)
