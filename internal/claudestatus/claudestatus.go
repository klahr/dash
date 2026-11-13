// Package claudestatus follows Claude's status page (status.claude.com, an
// Atlassian Statuspage): a platform admin subscribes the page's webhooks to
// this Dash's webhook URL, and Dash keeps the incidents and component
// statuses it hears about, for everyone's dashboard to show. It can also read
// the page's public API, now and then or when asked, for a Dash the page's
// webhooks can't reach.
package claudestatus

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// DefaultPage is Claude's status page.
const DefaultPage = "https://status.claude.com"

// WebhookPrefix is where the status page posts: WebhookPrefix + the token.
const WebhookPrefix = "/api/claude-status/webhook/"

const (
	keepIncidents  = 50                  // incidents kept, newest first
	keepResolved   = 30 * 24 * time.Hour // resolved incidents older than this are dropped
	recentResolved = 7 * 24 * time.Hour  // resolved incidents shown as recent
	keepDeliveries = 20
	// MaxBody is the largest webhook body taken.
	MaxBody = 1 << 20
	// PollEvery is how often the page's API is read when polling is on.
	PollEvery = 5 * time.Minute
)

var (
	// ErrRefused: the webhook is off, or the token is wrong.
	ErrRefused = errors.New("claude status webhook refused")
	// ErrBadPayload: the body isn't a status page webhook.
	ErrBadPayload = errors.New("not a status page webhook")
	// ErrBadPage: the page address isn't an http(s) URL.
	ErrBadPage = errors.New("the status page address must be an http or https URL")
)

// Store keeps the service's document (store.Store does).
type Store interface {
	ClaudeStatus() ([]byte, error)
	PutClaudeStatus([]byte) error
}

// Settings are the platform admin's.
type Settings struct {
	// Enabled: the webhook is listened to, and the status is shown.
	Enabled bool `json:"enabled"`
	// Token is the secret part of the webhook URL (the status page doesn't
	// sign its webhooks).
	Token string `json:"token,omitempty"`
	// PageURL is the status page (default DefaultPage).
	PageURL string `json:"page_url,omitempty"`
	// Poll reads the page's API every PollEvery as well.
	Poll bool `json:"poll"`
}

// Update is one post on an incident.
type Update struct {
	ID     string    `json:"id"`
	Status string    `json:"status"`
	Body   string    `json:"body"`
	At     time.Time `json:"at"`
}

// Incident is one of the status page's incidents.
type Incident struct {
	ID string `json:"id"`
	// Name is its title.
	Name string `json:"name"`
	// Status: investigating, identified, monitoring, resolved, postmortem
	// (or, for maintenance, scheduled, in_progress, verifying, completed).
	Status string `json:"status"`
	// Impact: none, minor, major, critical, maintenance.
	Impact     string    `json:"impact"`
	URL        string    `json:"url,omitempty"`
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
	Resolved   time.Time `json:"resolved,omitzero"`
	Components []string  `json:"components,omitempty"` // names of the components it affects
	Updates    []Update  `json:"updates,omitempty"`    // newest first
}

// Active reports whether the incident is still going on.
func (i Incident) Active() bool {
	switch i.Status {
	case "resolved", "postmortem", "completed":
		return false
	}
	return i.Resolved.IsZero()
}

// Component is a part of Claude the page reports on (claude.ai, the API,
// Claude Code…).
type Component struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Status  string    `json:"status"` // operational, degraded_performance, partial_outage, major_outage, under_maintenance
	Updated time.Time `json:"updated,omitzero"`
}

// Delivery is one webhook or read of the page the admin sees.
type Delivery struct {
	At      time.Time `json:"at"`
	Source  string    `json:"source"` // "webhook" or "sync"
	Summary string    `json:"summary,omitempty"`
	Error   string    `json:"error,omitempty"`
}

type document struct {
	Settings    Settings    `json:"settings"`
	Indicator   string      `json:"indicator,omitempty"`
	Description string      `json:"description,omitempty"`
	Components  []Component `json:"components,omitempty"`
	Incidents   []Incident  `json:"incidents,omitempty"` // newest first
	Updated     time.Time   `json:"updated,omitzero"`    // when the status last changed
	LastWebhook time.Time   `json:"last_webhook,omitzero"`
	LastSync    time.Time   `json:"last_sync,omitzero"`
	Deliveries  []Delivery  `json:"deliveries,omitempty"` // newest first
}

// Status is what every signed-in user sees.
type Status struct {
	Enabled bool `json:"enabled"`
	// Indicator: none (all fine), minor, major, critical or maintenance;
	// "" before anything was heard.
	Indicator   string `json:"indicator"`
	Description string `json:"description,omitempty"`
	PageURL     string `json:"page_url,omitempty"`
	// Active are the incidents going on, Recent those resolved in the last
	// week (newest first).
	Active []Incident `json:"active"`
	Recent []Incident `json:"recent"`
	// Components are those not operational.
	Components []Component `json:"components"`
	Updated    time.Time   `json:"updated,omitzero"`
}

// Admin is what a platform admin sees.
type Admin struct {
	Settings
	WebhookPath string     `json:"webhook_path"` // "" until there's a token
	LastWebhook time.Time  `json:"last_webhook,omitzero"`
	LastSync    time.Time  `json:"last_sync,omitzero"`
	Deliveries  []Delivery `json:"deliveries"`
}

// Service keeps Claude's status.
type Service struct {
	Store Store
	// Publish is told the new Status when it changes (optional).
	Publish func(Status)
	// Client reads the page's API (default: one with a 20s timeout).
	Client *http.Client
	// Now is the clock (default time.Now).
	Now func() time.Time

	mu      sync.Mutex
	doc     document
	loaded  bool
	syncing sync.Mutex
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// load reads the document once. Callers hold mu.
func (s *Service) load() error {
	if s.loaded {
		return nil
	}
	b, err := s.Store.ClaudeStatus()
	if err != nil {
		return err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &s.doc); err != nil {
			return fmt.Errorf("claude status: %w", err)
		}
	}
	s.loaded = true
	return nil
}

// save writes the document. Callers hold mu.
func (s *Service) save() error {
	b, err := json.Marshal(s.doc)
	if err != nil {
		return err
	}
	return s.Store.PutClaudeStatus(b)
}

func (d *document) pageURL() string {
	if d.Settings.PageURL != "" {
		return d.Settings.PageURL
	}
	return DefaultPage
}

func (d *document) status(now time.Time) Status {
	st := Status{Enabled: d.Settings.Enabled, PageURL: d.pageURL(), Active: []Incident{}, Recent: []Incident{}, Components: []Component{}}
	if !d.Settings.Enabled {
		return st
	}
	st.Indicator, st.Description, st.Updated = d.Indicator, d.Description, d.Updated
	for _, i := range d.Incidents {
		switch {
		case i.Active():
			st.Active = append(st.Active, i)
		case now.Sub(i.Resolved) < recentResolved || (i.Resolved.IsZero() && now.Sub(i.Updated) < recentResolved):
			st.Recent = append(st.Recent, i)
		}
	}
	for _, c := range d.Components {
		if c.Status != "" && c.Status != "operational" {
			st.Components = append(st.Components, c)
		}
	}
	return st
}

// Status is Claude's status as last heard.
func (s *Service) Status() (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return Status{}, err
	}
	return s.doc.status(s.now()), nil
}

// Admin is the settings and recent deliveries, for platform admins.
func (s *Service) Admin() (Admin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return Admin{}, err
	}
	return s.admin(), nil
}

func (s *Service) admin() Admin {
	a := Admin{Settings: s.doc.Settings, LastWebhook: s.doc.LastWebhook, LastSync: s.doc.LastSync,
		Deliveries: append([]Delivery{}, s.doc.Deliveries...)}
	a.PageURL = s.doc.pageURL()
	if a.Token != "" {
		a.WebhookPath = WebhookPrefix + a.Token
	}
	return a
}

// Configure changes the settings; a nil field is left as it is. Turning it
// on the first time makes the webhook's token.
func (s *Service) Configure(enabled *bool, pageURL *string, poll *bool) (Admin, error) {
	s.mu.Lock()
	if err := s.load(); err != nil {
		s.mu.Unlock()
		return Admin{}, err
	}
	old := s.doc.Settings
	if pageURL != nil {
		p, err := cleanPage(*pageURL)
		if err != nil {
			s.mu.Unlock()
			return Admin{}, err
		}
		if p != s.doc.pageURL() {
			// Another page: what was heard from the old one no longer counts.
			s.doc.Indicator, s.doc.Description, s.doc.Components, s.doc.Incidents = "", "", nil, nil
		}
		s.doc.Settings.PageURL = p
		if p == DefaultPage {
			s.doc.Settings.PageURL = ""
		}
	}
	if enabled != nil {
		s.doc.Settings.Enabled = *enabled
	}
	if poll != nil {
		s.doc.Settings.Poll = *poll
	}
	if s.doc.Settings.Enabled && s.doc.Settings.Token == "" {
		s.doc.Settings.Token = newToken()
	}
	if err := s.save(); err != nil {
		s.doc.Settings = old
		s.mu.Unlock()
		return Admin{}, err
	}
	a := s.admin()
	st := s.doc.status(s.now())
	s.mu.Unlock()
	s.publish(st)
	return a, nil
}

// Rotate makes a new webhook token: the old URL stops working.
func (s *Service) Rotate() (Admin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return Admin{}, err
	}
	old := s.doc.Settings.Token
	s.doc.Settings.Token = newToken()
	if err := s.save(); err != nil {
		s.doc.Settings.Token = old
		return Admin{}, err
	}
	return s.admin(), nil
}

func cleanPage(p string) (string, error) {
	p = strings.TrimRight(strings.TrimSpace(p), "/")
	if p == "" {
		return DefaultPage, nil
	}
	u, err := url.Parse(p)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return "", ErrBadPage
	}
	return p, nil
}

// webLink reports whether u is an http(s) URL.
func webLink(u string) bool {
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "https" || p.Scheme == "http") && p.Host != ""
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Service) publish(st Status) {
	if s.Publish != nil {
		s.Publish(st)
	}
}

// ---- the page's payloads ----

// apiIncident is an incident as the status page sends it, in a webhook and
// in its API.
type apiIncident struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Impact     string    `json:"impact"`
	Override   string    `json:"impact_override"`
	Shortlink  string    `json:"shortlink"`
	Created    time.Time `json:"created_at"`
	Updated    time.Time `json:"updated_at"`
	Resolved   time.Time `json:"resolved_at"`
	Components []struct {
		Name string `json:"name"`
	} `json:"components"`
	Updates []struct {
		ID        string    `json:"id"`
		Status    string    `json:"status"`
		Body      string    `json:"body"`
		Created   time.Time `json:"created_at"`
		DisplayAt time.Time `json:"display_at"`
		Affected  []struct {
			Name string `json:"name"`
		} `json:"affected_components"`
	} `json:"incident_updates"`
}

func (a apiIncident) incident() Incident {
	i := Incident{ID: a.ID, Name: a.Name, Status: a.Status, Impact: a.Impact, URL: a.Shortlink,
		Created: a.Created, Updated: a.Updated, Resolved: a.Resolved}
	if a.Override != "" {
		i.Impact = a.Override
	}
	if !webLink(i.URL) {
		i.URL = "" // shown as a link: never javascript: or the like
	}
	add := func(name string) {
		if name != "" && !slices.Contains(i.Components, name) {
			i.Components = append(i.Components, name)
		}
	}
	for _, c := range a.Components {
		add(c.Name)
	}
	for _, u := range a.Updates {
		at := u.DisplayAt
		if at.IsZero() {
			at = u.Created
		}
		i.Updates = append(i.Updates, Update{ID: u.ID, Status: u.Status, Body: u.Body, At: at})
		for _, c := range u.Affected {
			add(c.Name)
		}
	}
	slices.SortStableFunc(i.Updates, func(a, b Update) int { return b.At.Compare(a.At) })
	if i.Updated.IsZero() && len(i.Updates) > 0 {
		i.Updated = i.Updates[0].At
	}
	return i
}

type apiComponent struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Status  string    `json:"status"`
	Updated time.Time `json:"updated_at"`
	Group   bool      `json:"group"`
}

// webhook is a status page webhook: an incident's update, or a component's.
type webhook struct {
	Page *struct {
		Indicator   string `json:"status_indicator"`
		Description string `json:"status_description"`
	} `json:"page"`
	Incident        *apiIncident  `json:"incident"`
	Component       *apiComponent `json:"component"`
	ComponentUpdate *struct {
		ComponentID string    `json:"component_id"`
		NewStatus   string    `json:"new_status"`
		Created     time.Time `json:"created_at"`
	} `json:"component_update"`
}

// Webhook takes a webhook the status page posted to token's URL.
func (s *Service) Webhook(token string, body []byte) error {
	s.mu.Lock()
	if err := s.load(); err != nil {
		s.mu.Unlock()
		return err
	}
	want := s.doc.Settings.Token
	if !s.doc.Settings.Enabled || want == "" || subtle.ConstantTimeCompare([]byte(token), []byte(want)) != 1 {
		s.mu.Unlock()
		return ErrRefused
	}
	now := s.now()
	var w webhook
	err := json.Unmarshal(body, &w)
	if err == nil && w.Incident == nil && w.Component == nil && w.Page == nil {
		err = ErrBadPayload
	}
	if err != nil {
		s.record(Delivery{At: now, Source: "webhook", Error: ErrBadPayload.Error()})
		_ = s.save()
		s.mu.Unlock()
		return ErrBadPayload
	}
	var summary string
	if w.Page != nil && w.Page.Indicator != "" {
		s.doc.Indicator, s.doc.Description = w.Page.Indicator, w.Page.Description
		summary = w.Page.Description
	}
	if w.Incident != nil && w.Incident.ID != "" {
		i := w.Incident.incident()
		s.putIncident(i)
		summary = fmt.Sprintf("%s: %s", i.Name, i.Status)
	}
	if w.Component != nil && w.Component.ID != "" {
		c := Component{ID: w.Component.ID, Name: w.Component.Name, Status: w.Component.Status, Updated: w.Component.Updated}
		if u := w.ComponentUpdate; u != nil && u.NewStatus != "" {
			c.Status = u.NewStatus
			if !u.Created.IsZero() {
				c.Updated = u.Created
			}
		}
		s.putComponent(c)
		summary = fmt.Sprintf("%s: %s", c.Name, strings.ReplaceAll(c.Status, "_", " "))
	}
	if w.Page == nil || w.Page.Indicator == "" {
		s.doc.Indicator, s.doc.Description = s.doc.derived()
	}
	s.doc.Updated, s.doc.LastWebhook = now, now
	s.prune(now)
	s.record(Delivery{At: now, Source: "webhook", Summary: summary})
	err = s.save()
	st := s.doc.status(now)
	s.mu.Unlock()
	s.publish(st)
	return err
}

func (s *Service) putIncident(i Incident) {
	for k, old := range s.doc.Incidents {
		if old.ID != i.ID {
			continue
		}
		// A webhook carries every update so far; keep any it lacks all the same.
		for _, u := range old.Updates {
			if !slices.ContainsFunc(i.Updates, func(n Update) bool { return n.ID == u.ID }) {
				i.Updates = append(i.Updates, u)
			}
		}
		slices.SortStableFunc(i.Updates, func(a, b Update) int { return b.At.Compare(a.At) })
		if i.URL == "" {
			i.URL = old.URL
		}
		s.doc.Incidents[k] = i
		s.sortIncidents()
		return
	}
	s.doc.Incidents = append(s.doc.Incidents, i)
	s.sortIncidents()
}

func (s *Service) sortIncidents() {
	slices.SortStableFunc(s.doc.Incidents, func(a, b Incident) int { return b.Created.Compare(a.Created) })
}

func (s *Service) putComponent(c Component) {
	for k, old := range s.doc.Components {
		if old.ID == c.ID {
			if c.Name == "" {
				c.Name = old.Name
			}
			s.doc.Components[k] = c
			return
		}
	}
	s.doc.Components = append(s.doc.Components, c)
}

// derived is the page's indicator worked out from the incidents and
// components, for a webhook without the page's own.
func (d *document) derived() (indicator, description string) {
	rank := map[string]int{"none": 0, "maintenance": 1, "minor": 2, "major": 3, "critical": 4}
	indicator = "none"
	for _, i := range d.Incidents {
		if i.Active() && rank[i.Impact] > rank[indicator] {
			indicator = i.Impact
		}
	}
	for _, c := range d.Components {
		var ind string
		switch c.Status {
		case "degraded_performance":
			ind = "minor"
		case "partial_outage":
			ind = "major"
		case "major_outage":
			ind = "critical"
		case "under_maintenance":
			ind = "maintenance"
		}
		if rank[ind] > rank[indicator] {
			indicator = ind
		}
	}
	description = map[string]string{
		"none": "All Systems Operational", "maintenance": "Service Under Maintenance", "minor": "Minor Service Outage",
		"major": "Partial System Outage", "critical": "Major System Outage",
	}[indicator]
	return indicator, description
}

func (s *Service) prune(now time.Time) {
	s.doc.Incidents = slices.DeleteFunc(s.doc.Incidents, func(i Incident) bool {
		return !i.Active() && !i.Resolved.IsZero() && now.Sub(i.Resolved) > keepResolved
	})
	if len(s.doc.Incidents) > keepIncidents {
		s.doc.Incidents = s.doc.Incidents[:keepIncidents]
	}
}

func (s *Service) record(d Delivery) {
	s.doc.Deliveries = append([]Delivery{d}, s.doc.Deliveries...)
	if len(s.doc.Deliveries) > keepDeliveries {
		s.doc.Deliveries = s.doc.Deliveries[:keepDeliveries]
	}
}

// ---- reading the page's API ----

// Sync reads the page's API (summary.json and incidents.json) now.
func (s *Service) Sync(ctx context.Context) (Status, error) {
	s.syncing.Lock()
	defer s.syncing.Unlock()
	s.mu.Lock()
	if err := s.load(); err != nil {
		s.mu.Unlock()
		return Status{}, err
	}
	page := s.doc.pageURL()
	s.mu.Unlock()

	var summary struct {
		Status struct {
			Indicator   string `json:"indicator"`
			Description string `json:"description"`
		} `json:"status"`
		Components []apiComponent `json:"components"`
		Incidents  []apiIncident  `json:"incidents"`
	}
	var list struct {
		Incidents []apiIncident `json:"incidents"`
	}
	err := s.get(ctx, page+"/api/v2/summary.json", &summary)
	if err == nil {
		err = s.get(ctx, page+"/api/v2/incidents.json", &list)
	}

	s.mu.Lock()
	now := s.now()
	if page != s.doc.pageURL() {
		s.mu.Unlock()
		return Status{}, errors.New("the status page changed while it was read: try again")
	}
	if err != nil {
		s.record(Delivery{At: now, Source: "sync", Error: err.Error()})
		_ = s.save()
		s.mu.Unlock()
		return Status{}, err
	}
	before, _ := json.Marshal(s.doc.status(now))
	s.doc.Indicator, s.doc.Description = summary.Status.Indicator, summary.Status.Description
	comps := []Component{}
	for _, c := range summary.Components {
		if !c.Group {
			comps = append(comps, Component{ID: c.ID, Name: c.Name, Status: c.Status, Updated: c.Updated})
		}
	}
	s.doc.Components = comps
	for _, a := range append(list.Incidents, summary.Incidents...) {
		if a.ID != "" {
			s.putIncident(a.incident())
		}
	}
	// An incident the page no longer lists as unresolved has ended, even if
	// its last update was missed.
	unresolved := map[string]bool{}
	for _, a := range summary.Incidents {
		unresolved[a.ID] = true
	}
	for k, i := range s.doc.Incidents {
		if i.Active() && !unresolved[i.ID] {
			s.doc.Incidents[k].Status = "resolved"
			if s.doc.Incidents[k].Resolved.IsZero() {
				s.doc.Incidents[k].Resolved = now
			}
		}
	}
	s.prune(now)
	s.doc.LastSync = now
	st := s.doc.status(now)
	after, _ := json.Marshal(st)
	changed := string(before) != string(after)
	if changed {
		s.doc.Updated = now
		st.Updated = now
	}
	s.record(Delivery{At: now, Source: "sync", Summary: summary.Status.Description})
	err = s.save()
	s.mu.Unlock()
	if changed {
		s.publish(st)
	}
	return st, err
}

func (s *Service) get(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", u, res.Status)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4*MaxBody)).Decode(v); err != nil {
		return fmt.Errorf("%s: %w", u, err)
	}
	return nil
}

// Run reads the page every PollEvery while polling is on, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(PollEvery)
	defer t.Stop()
	for {
		s.mu.Lock()
		on := s.load() == nil && s.doc.Settings.Enabled && s.doc.Settings.Poll
		s.mu.Unlock()
		if on {
			_, _ = s.Sync(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
