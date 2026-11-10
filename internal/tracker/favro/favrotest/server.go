// Package favrotest is a fake Favro REST API (httptest) for tests of the
// Favro backend: accounts and tokens, organizations, collections, boards
// (widgets) with columns and cards, tags, a Number custom field, comments,
// dependencies and webhooks. It pages its listings (requestId + page),
// checks basic auth and the organizationId header, can rate-limit (429
// with X-RateLimit-* headers) and posts signed webhook calls when people
// change cards.
//
// It is independent of package favro (no import), so favro's own tests can
// use it.
package favrotest

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Card is a fake card.
type Card struct {
	CardID              string       `json:"cardId"`
	CardCommonID        string       `json:"cardCommonId"`
	WidgetCommonID      string       `json:"widgetCommonId"`
	ColumnID            string       `json:"columnId"`
	Name                string       `json:"name"`
	DetailedDescription string       `json:"detailedDescription"`
	Tags                []string     `json:"tags"`
	Archived            bool         `json:"archived"`
	NumComments         int          `json:"numComments"`
	CustomFields        []Field      `json:"customFields"`
	Dependencies        []Dependency `json:"dependencies"`
}

// Field is a custom field's value on a card.
type Field struct {
	CustomFieldID string   `json:"customFieldId"`
	Total         *float64 `json:"total,omitempty"`
}

// Dependency is a card's dependency.
type Dependency struct {
	CardID   string `json:"cardId"`
	IsBefore bool   `json:"isBefore"`
}

// Comment is a fake comment.
type Comment struct {
	CommentID    string `json:"commentId"`
	CardCommonID string `json:"cardCommonId"`
	UserID       string `json:"userId"`
	Comment      string `json:"comment"`
	Created      string `json:"created"`
}

// Webhook is a registered webhook.
type Webhook struct {
	WebhookID      string         `json:"webhookId"`
	WidgetCommonID string         `json:"widgetCommonId"`
	Name           string         `json:"name"`
	PostToURL      string         `json:"postToUrl"`
	Secret         string         `json:"-"`
	Options        map[string]any `json:"options,omitempty"`
}

// Request is one request the server got.
type Request struct {
	Method, Path, Query string
	Email, Token, Org   string
	Backend             string // X-Favro-Backend-Identifier sent
	Body                string
}

type org struct {
	id, name    string
	collections map[string]string           // id → name
	widgets     map[string][2]string        // widget id → {name, collection id}
	columns     map[string][]map[string]any // widget id → columns
	tags        map[string]string           // tag id → name
	users       map[string]string           // user id → name
	fields      []map[string]any            // custom fields
	cards       map[string]*Card            // card id →
	order       []string                    // card ids
	comments    map[string][]Comment        // card common id →
	hooks       map[string]*Webhook         // webhook id →
	members     map[string]bool             // account emails
}

type listing struct {
	items   []json.RawMessage
	backend string
}

// Server is a fake Favro.
type Server struct {
	*httptest.Server
	// PageSize is the listings' page size (default 2, to page often).
	PageSize int

	mu       sync.Mutex
	accounts map[string]string // email → token
	orgs     map[string]*org
	seq      int
	listings map[string]listing
	reqs     []Request
	throttle int // requests to answer 429
	reset    time.Duration
	lowAfter int // when >0: answer X-RateLimit-Remaining 0 after this many requests
	client   *http.Client
	hookWG   sync.WaitGroup
}

// New starts a fake Favro; it stops when the test ends.
func New(t interface{ Cleanup(func()) }) *Server {
	s := &Server{PageSize: 2, accounts: map[string]string{}, orgs: map[string]*org{}, listings: map[string]listing{}, client: &http.Client{Timeout: 10 * time.Second}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(func() { s.hookWG.Wait(); s.Close() })
	return s
}

// URL is the API's base URL (like https://favro.com/api/v1).
func (s *Server) APIURL() string { return s.URL + "/api/v1" }

func (s *Server) id(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s%d", prefix, s.seq)
}

// AddAccount makes a user with an API token.
func (s *Server) AddAccount(email, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accounts[email] = token
}

// AddOrg makes an organization the accounts can see.
func (s *Server) AddOrg(name string, emails ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := &org{id: s.id("org"), name: name, collections: map[string]string{}, widgets: map[string][2]string{},
		columns: map[string][]map[string]any{}, tags: map[string]string{}, users: map[string]string{},
		cards: map[string]*Card{}, comments: map[string][]Comment{}, hooks: map[string]*Webhook{}, members: map[string]bool{}}
	for _, e := range emails {
		o.members[e] = true
		uid := "u-" + e
		o.users[uid] = strings.Split(e, "@")[0]
	}
	s.orgs[o.id] = o
	return o.id
}

// AddUser adds a person (who comments) to an organization.
func (s *Server) AddUser(orgID, userID, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orgs[orgID].users[userID] = name
}

// AddCollection makes a collection.
func (s *Server) AddCollection(orgID, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.id("col")
	s.orgs[orgID].collections[id] = name
	return id
}

// AddBoard makes a board in a collection with these columns; it returns
// the board's widgetCommonId and its column ids, in order.
func (s *Server) AddBoard(orgID, collectionID, name string, columns ...string) (string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.orgs[orgID]
	w := s.id("w")
	o.widgets[w] = [2]string{name, collectionID}
	var ids []string
	for i, c := range columns {
		id := s.id("c")
		ids = append(ids, id)
		o.columns[w] = append(o.columns[w], map[string]any{"columnId": id, "widgetCommonId": w, "name": c, "position": i})
	}
	return w, ids
}

// AddNumberField makes a Number custom field.
func (s *Server) AddNumberField(orgID, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.id("cf")
	o := s.orgs[orgID]
	o.fields = append(o.fields, map[string]any{"customFieldId": id, "name": name, "type": "Number", "enabled": true})
	o.fields = append(o.fields, map[string]any{"customFieldId": s.id("cf"), "name": "Notes", "type": "Text", "enabled": true})
	return id
}

// AddTag makes a tag and returns its id.
func (s *Server) AddTag(orgID, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tagLocked(s.orgs[orgID], name)
}

func (s *Server) tagLocked(o *org, name string) string {
	for id, n := range o.tags {
		if strings.EqualFold(n, name) {
			return id
		}
	}
	id := s.id("t")
	o.tags[id] = name
	return id
}

// ---- what people do in Favro ----

// AddCard puts a card on a board as a person would; it returns its cardId.
func (s *Server) AddCard(orgID string, c Card) string {
	s.mu.Lock()
	o := s.orgs[orgID]
	c.CardID, c.CardCommonID = s.id("card"), s.id("common")
	o.cards[c.CardID] = &c
	o.order = append(o.order, c.CardID)
	s.mu.Unlock()
	s.fire(orgID, c.WidgetCommonID, "created", c.CardID)
	return c.CardID
}

// Edit changes a card as a person would (and fires the webhooks).
func (s *Server) Edit(orgID, cardID string, fn func(*Card)) {
	s.mu.Lock()
	c := s.orgs[orgID].cards[cardID]
	fn(c)
	w := c.WidgetCommonID
	s.mu.Unlock()
	s.fire(orgID, w, "updated", cardID)
}

// Move puts a card in another column.
func (s *Server) Move(orgID, cardID, columnID string) {
	s.Edit(orgID, cardID, func(c *Card) { c.ColumnID = columnID })
}

// AddComment comments on a card as userID.
func (s *Server) AddComment(orgID, cardID, userID, body string) {
	s.mu.Lock()
	o := s.orgs[orgID]
	c := o.cards[cardID]
	s.addCommentLocked(o, c, userID, body)
	w := c.WidgetCommonID
	s.mu.Unlock()
	s.fire(orgID, w, "created", cardID)
}

func (s *Server) addCommentLocked(o *org, c *Card, userID, body string) {
	s.seq++
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(s.seq) * time.Second)
	o.comments[c.CardCommonID] = append(o.comments[c.CardCommonID], Comment{CommentID: s.id("k"), CardCommonID: c.CardCommonID, UserID: userID, Comment: body, Created: at.Format(time.RFC3339)})
	c.NumComments = len(o.comments[c.CardCommonID])
}

// Card is a copy of a card.
func (s *Server) Card(orgID, cardID string) Card {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *s.orgs[orgID].cards[cardID]
	c.Tags = slices.Clone(c.Tags)
	c.Dependencies = slices.Clone(c.Dependencies)
	c.CustomFields = slices.Clone(c.CustomFields)
	return c
}

// Cards are a board's cards (archived too), oldest first.
func (s *Server) Cards(orgID, widget string) []Card {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.orgs[orgID]
	var out []Card
	for _, id := range o.order {
		if c := o.cards[id]; c.WidgetCommonID == widget {
			out = append(out, *c)
		}
	}
	return out
}

// Comments are a card's comments.
func (s *Server) Comments(orgID, cardID string) []Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.orgs[orgID]
	return slices.Clone(o.comments[o.cards[cardID].CardCommonID])
}

// TagName is a tag's name.
func (s *Server) TagName(orgID, tagID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orgs[orgID].tags[tagID]
}

// Webhooks are the webhooks registered on an organization's boards.
func (s *Server) Webhooks(orgID string) []Webhook {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Webhook
	for _, h := range s.orgs[orgID].hooks {
		out = append(out, *h)
	}
	slices.SortFunc(out, func(a, b Webhook) int { return strings.Compare(a.WebhookID, b.WebhookID) })
	return out
}

// Requests are the requests made so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reqs)
}

// ResetRequests forgets them.
func (s *Server) ResetRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = nil
}

// Throttle answers the next n requests 429, resetting after reset.
func (s *Server) Throttle(n int, reset time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.throttle, s.reset = n, reset
}

// LowAfter makes the server report the account's quota used up
// (X-RateLimit-Remaining: 0) on its answers once n requests were made.
func (s *Server) LowAfter(n int, reset time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lowAfter, s.reset = n, reset
}

// WaitWebhooks waits for the webhook calls in flight.
func (s *Server) WaitWebhooks() { s.hookWG.Wait() }

// Sign is Favro's webhook signature.
func Sign(secret, payloadID, url string) string {
	m := hmac.New(sha1.New, []byte(secret))
	m.Write([]byte(payloadID + url))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// fire posts a signed call to the board's webhooks, in the background.
func (s *Server) fire(orgID, widget, action, cardID string) {
	s.mu.Lock()
	var hooks []Webhook
	for _, h := range s.orgs[orgID].hooks {
		if h.WidgetCommonID == widget {
			hooks = append(hooks, *h)
		}
	}
	payload := s.id("payload")
	s.mu.Unlock()
	for _, h := range hooks {
		body, _ := json.Marshal(map[string]any{"payloadId": payload, "action": action, "hookId": h.WebhookID,
			"card": map[string]any{"cardId": cardID, "widgetCommonId": widget}})
		s.post(h, payload, body)
	}
}

func (s *Server) post(h Webhook, payload string, body []byte) {
	s.hookWG.Add(1)
	go func() {
		defer s.hookWG.Done()
		req, err := http.NewRequest(http.MethodPost, h.PostToURL, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Favro-Webhook", Sign(h.Secret, payload, h.PostToURL))
		if resp, err := s.client.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
}

// ---- the API ----

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		body = buf.Bytes()
	}
	email, token, _ := r.BasicAuth()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Email: email, Token: token,
		Org: r.Header.Get("organizationId"), Backend: r.Header.Get("X-Favro-Backend-Identifier"), Body: string(body)})
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Favro-Backend-Identifier", "be-1")
		w.Header().Set("X-RateLimit-Limit", "1000")
		if s.lowAfter > 0 && len(s.reqs) >= s.lowAfter {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", time.Now().Add(s.reset).UTC().Format(time.RFC3339Nano))
		} else {
			w.Header().Set("X-RateLimit-Remaining", "999")
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(code int, msg string) { reply(code, map[string]string{"message": msg}) }
	if s.throttle > 0 {
		s.throttle--
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", time.Now().Add(s.reset).UTC().Format(time.RFC3339Nano))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"Rate limit exceeded"}`))
		return
	}
	path, ok := strings.CutPrefix(r.URL.Path, "/api/v1")
	if !ok {
		fail(404, "no such endpoint")
		return
	}
	if want, ok := s.accounts[email]; !ok || want != token || token == "" {
		fail(401, "Unauthorized")
		return
	}
	q := r.URL.Query()
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if path == "/organizations" {
		var out []any
		for _, o := range s.sortedOrgs() {
			if o.members[email] {
				out = append(out, map[string]any{"organizationId": o.id, "name": o.name})
			}
		}
		s.page(w, r, reply, out)
		return
	}
	o := s.orgs[r.Header.Get("organizationId")]
	if o == nil || !o.members[email] {
		fail(403, "organizationId header missing or not yours")
		return
	}
	var in map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			fail(400, "bad JSON")
			return
		}
	}
	switch {
	case r.Method == "GET" && path == "/collections":
		var out []any
		for _, id := range sortedKeys(o.collections) {
			out = append(out, map[string]any{"collectionId": id, "name": o.collections[id]})
		}
		s.page(w, r, reply, out)
	case r.Method == "GET" && path == "/widgets":
		var out []any
		for _, id := range sortedKeys(o.widgets) {
			if c := q.Get("collectionId"); c != "" && o.widgets[id][1] != c {
				continue
			}
			out = append(out, map[string]any{"widgetCommonId": id, "name": o.widgets[id][0], "type": "board", "collectionIds": []string{o.widgets[id][1]}})
		}
		s.page(w, r, reply, out)
	case r.Method == "GET" && path == "/columns":
		cols, ok := o.columns[q.Get("widgetCommonId")]
		if !ok {
			fail(404, "no such widget")
			return
		}
		s.page(w, r, reply, toAny(cols))
	case r.Method == "GET" && path == "/tags":
		var out []any
		for _, id := range sortedKeys(o.tags) {
			if n := q.Get("name"); n != "" && !strings.EqualFold(n, o.tags[id]) {
				continue
			}
			out = append(out, map[string]any{"tagId": id, "name": o.tags[id]})
		}
		s.page(w, r, reply, out)
	case r.Method == "GET" && path == "/users":
		var out []any
		for _, id := range sortedKeys(o.users) {
			out = append(out, map[string]any{"userId": id, "name": o.users[id]})
		}
		s.page(w, r, reply, out)
	case r.Method == "GET" && path == "/customfields":
		s.page(w, r, reply, toAny(o.fields))
	case r.Method == "GET" && path == "/cards":
		wid := q.Get("widgetCommonId")
		if _, ok := o.widgets[wid]; !ok && q.Get("requestId") == "" {
			fail(404, "no such widget")
			return
		}
		var out []any
		for _, id := range o.order {
			if c := o.cards[id]; c.WidgetCommonID == wid && !c.Archived {
				out = append(out, c)
			}
		}
		s.page(w, r, reply, out)
	case r.Method == "GET" && len(parts) == 2 && parts[0] == "cards":
		c := o.cards[parts[1]]
		if c == nil {
			fail(404, "no such card")
			return
		}
		reply(200, c)
	case r.Method == "POST" && path == "/cards":
		wid, _ := in["widgetCommonId"].(string)
		if _, ok := o.widgets[wid]; !ok {
			fail(400, "widgetCommonId required")
			return
		}
		c := &Card{CardID: s.id("card"), CardCommonID: s.id("common"), WidgetCommonID: wid}
		c.Name, _ = in["name"].(string)
		c.DetailedDescription, _ = in["detailedDescription"].(string)
		c.ColumnID, _ = in["columnId"].(string)
		if c.ColumnID == "" {
			c.ColumnID = o.columns[wid][0]["columnId"].(string)
		}
		for _, t := range strs(in["tags"]) {
			c.Tags = append(c.Tags, s.tagLocked(o, t))
		}
		setFields(c, in["customFields"])
		o.cards[c.CardID] = c
		o.order = append(o.order, c.CardID)
		reply(200, c)
	case r.Method == "PUT" && len(parts) == 2 && parts[0] == "cards":
		c := o.cards[parts[1]]
		if c == nil {
			fail(404, "no such card")
			return
		}
		if v, ok := in["name"].(string); ok {
			c.Name = v
		}
		if v, ok := in["detailedDescription"].(string); ok {
			c.DetailedDescription = v
		}
		if v, ok := in["columnId"].(string); ok {
			if !slices.ContainsFunc(o.columns[c.WidgetCommonID], func(m map[string]any) bool { return m["columnId"] == v }) {
				fail(400, "no such column")
				return
			}
			c.ColumnID = v
		}
		if v, ok := in["archive"].(bool); ok {
			c.Archived = v
		}
		for _, t := range strs(in["addTags"]) {
			if id := s.tagLocked(o, t); !slices.Contains(c.Tags, id) {
				c.Tags = append(c.Tags, id)
			}
		}
		for _, t := range strs(in["removeTags"]) {
			id := s.tagLocked(o, t)
			c.Tags = slices.DeleteFunc(c.Tags, func(x string) bool { return x == id })
		}
		setFields(c, in["customFields"])
		reply(200, c)
	case len(parts) >= 3 && parts[0] == "cards" && parts[2] == "dependencies":
		c := o.cards[parts[1]]
		if c == nil {
			fail(404, "no such card")
			return
		}
		switch {
		case r.Method == "POST" && len(parts) == 3:
			dep, _ := in["dependsOnCardId"].(string)
			other := o.cards[dep]
			if other == nil {
				fail(404, "no such card to depend on")
				return
			}
			c.Dependencies = append(c.Dependencies, Dependency{CardID: dep})
			other.Dependencies = append(other.Dependencies, Dependency{CardID: c.CardID, IsBefore: true})
			reply(200, c)
		case r.Method == "DELETE" && len(parts) == 4:
			dep := parts[3]
			c.Dependencies = slices.DeleteFunc(c.Dependencies, func(d Dependency) bool { return d.CardID == dep && !d.IsBefore })
			if other := o.cards[dep]; other != nil {
				other.Dependencies = slices.DeleteFunc(other.Dependencies, func(d Dependency) bool { return d.CardID == c.CardID && d.IsBefore })
			}
			reply(200, c)
		default:
			fail(404, "no such endpoint")
		}
	case r.Method == "GET" && path == "/comments":
		s.page(w, r, reply, toAny(o.comments[q.Get("cardCommonId")]))
	case r.Method == "POST" && path == "/comments":
		cc, _ := in["cardCommonId"].(string)
		var card *Card
		for _, c := range o.cards {
			if c.CardCommonID == cc {
				card = c
			}
		}
		if card == nil {
			fail(404, "no such card")
			return
		}
		text, _ := in["comment"].(string)
		s.addCommentLocked(o, card, "u-"+email, text)
		reply(200, o.comments[cc][len(o.comments[cc])-1])
	case r.Method == "GET" && path == "/webhooks":
		var out []any
		for _, id := range sortedKeys(o.hooks) {
			if h := o.hooks[id]; q.Get("widgetCommonId") == "" || h.WidgetCommonID == q.Get("widgetCommonId") {
				out = append(out, h)
			}
		}
		s.page(w, r, reply, out)
	case r.Method == "POST" && path == "/webhooks":
		h := &Webhook{WebhookID: s.id("hook")}
		h.WidgetCommonID, _ = in["widgetCommonId"].(string)
		h.Name, _ = in["name"].(string)
		h.PostToURL, _ = in["postToUrl"].(string)
		h.Secret, _ = in["secret"].(string)
		h.Options, _ = in["options"].(map[string]any)
		if _, ok := o.widgets[h.WidgetCommonID]; !ok || h.PostToURL == "" || h.Secret == "" {
			fail(400, "widgetCommonId, postToUrl and secret required")
			return
		}
		o.hooks[h.WebhookID] = h
		payload := s.id("payload")
		body, _ := json.Marshal(map[string]any{"payloadId": payload, "action": "ping", "hookId": h.WebhookID, "hook": map[string]any{"url": h.PostToURL}})
		s.post(*h, payload, body)
		reply(200, h)
	case r.Method == "DELETE" && len(parts) == 2 && parts[0] == "webhooks":
		if _, ok := o.hooks[parts[1]]; !ok {
			fail(404, "no such webhook")
			return
		}
		delete(o.hooks, parts[1])
		reply(200, map[string]any{})
	default:
		fail(404, "no such endpoint")
	}
}

// page answers one page of a listing: the first from items, the next ones
// (requestId + page) from the listing it remembered, served only by the
// backend that holds it (X-Favro-Backend-Identifier).
func (s *Server) page(w http.ResponseWriter, r *http.Request, reply func(int, any), items []any) {
	q := r.URL.Query()
	size := s.PageSize
	if size <= 0 {
		size = 2
	}
	var l listing
	reqID := q.Get("requestId")
	if reqID != "" {
		var ok bool
		if l, ok = s.listings[reqID]; !ok {
			reply(400, map[string]string{"message": "unknown requestId"})
			return
		}
		if r.Header.Get("X-Favro-Backend-Identifier") != l.backend {
			reply(400, map[string]string{"message": "page asked of another backend"})
			return
		}
	} else {
		for _, it := range items {
			b, _ := json.Marshal(it)
			l.items = append(l.items, b)
		}
		l.backend = "be-1"
		reqID = s.id("req")
		s.listings[reqID] = l
	}
	n, _ := strconv.Atoi(q.Get("page"))
	pages := (len(l.items) + size - 1) / size
	lo, hi := min(n*size, len(l.items)), min((n+1)*size, len(l.items))
	ents := l.items[lo:hi]
	if ents == nil {
		ents = []json.RawMessage{}
	}
	reply(200, map[string]any{"limit": size, "page": n, "pages": pages, "requestId": reqID, "entities": ents})
}

func (s *Server) sortedOrgs() []*org {
	var out []*org
	for _, id := range sortedKeys(s.orgs) {
		out = append(out, s.orgs[id])
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

func toAny[T any](xs []T) []any {
	out := make([]any, 0, len(xs))
	for _, x := range xs {
		out = append(out, x)
	}
	return out
}

func strs(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func setFields(c *Card, v any) {
	l, ok := v.([]any)
	if !ok {
		return
	}
	for _, x := range l {
		m, _ := x.(map[string]any)
		id, _ := m["customFieldId"].(string)
		var total *float64
		if f, ok := m["total"].(float64); ok {
			total = &f
		}
		c.CustomFields = slices.DeleteFunc(c.CustomFields, func(f Field) bool { return f.CustomFieldID == id })
		if total != nil {
			c.CustomFields = append(c.CustomFields, Field{CustomFieldID: id, Total: total})
		}
	}
}
