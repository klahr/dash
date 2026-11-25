package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// RunByID reads one workflow run of the repository in dir from GitHub
// (gh api repos/{owner}/{repo}/actions/runs/{id}).
func (g *Git) RunByID(ctx context.Context, projectID, dir string, id int64) (Run, error) {
	out, err := g.ghQuiet(ctx, projectID, dir, "api", "repos/{owner}/{repo}/actions/runs/"+strconv.FormatInt(id, 10))
	if err != nil {
		return Run{}, err
	}
	return ParseRESTRun([]byte(jsonPart(out)))
}

// ParseRESTRun reads a workflow run as GitHub's REST API (and its webhooks'
// workflow_run) writes it.
func ParseRESTRun(b []byte) (Run, error) {
	var raw struct {
		ID         int64     `json:"id"`
		Name       string    `json:"name"`
		Title      string    `json:"display_title"`
		Status     string    `json:"status"`
		Conclusion string    `json:"conclusion"`
		SHA        string    `json:"head_sha"`
		Branch     string    `json:"head_branch"`
		URL        string    `json:"html_url"`
		Event      string    `json:"event"`
		CreatedAt  time.Time `json:"created_at"`
		StartedAt  time.Time `json:"run_started_at"`
		UpdatedAt  time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return Run{}, fmt.Errorf("gh api run: %w", err)
	}
	if raw.ID == 0 {
		return Run{}, errors.New("gh api run: no run in the answer")
	}
	return Run{ID: raw.ID, Workflow: raw.Name, Title: raw.Title, Status: raw.Status, Conclusion: raw.Conclusion,
		SHA: raw.SHA, Branch: raw.Branch, URL: raw.URL, Event: raw.Event,
		CreatedAt: raw.CreatedAt, StartedAt: raw.StartedAt, UpdatedAt: raw.UpdatedAt}, nil
}

// HookConfig is a repository webhook Dash registers: where GitHub delivers,
// the secret that signs the deliveries, and the events it sends.
type HookConfig struct {
	URL    string   `json:"url"`
	Secret string   `json:"secret"`
	Events []string `json:"events"`
}

// Hook is a repository webhook as GitHub lists it (never with its secret).
type Hook struct {
	ID     int64    `json:"id"`
	URL    string   `json:"url"`
	Events []string `json:"events,omitempty"`
	Active bool     `json:"active"`
}

func hookBody(c HookConfig, create bool) []byte {
	body := map[string]any{
		"active": true,
		"events": c.Events,
		"config": map[string]string{"url": c.URL, "content_type": "json", "secret": c.Secret, "insecure_ssl": "0"},
	}
	if create {
		body["name"] = "web"
	}
	b, _ := json.Marshal(body)
	return b
}

// ghWithInput runs gh api with body on its stdin (--input -): what it sends
// stays out of the command line, process lists and the activity feed.
func (g *Git) ghWithInput(ctx context.Context, projectID, dir string, body []byte, args ...string) (string, error) {
	args = append(args, "--input", "-")
	if isWrite(args) {
		defer g.spaceWrites(ctx)()
	}
	started := g.limits.now()
	out, err := g.runIn(ctx, false, nil, body, projectID, dir, g.ghBin(), args...)
	return out, g.noteRateLimit(ctx, started, err)
}

// Hooks lists the webhooks of the repository in dir (needs admin rights).
func (g *Git) Hooks(ctx context.Context, projectID, dir string) ([]Hook, error) {
	out, err := g.ghQuiet(ctx, projectID, dir, "api", "repos/{owner}/{repo}/hooks?per_page=100")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID     int64    `json:"id"`
		Active bool     `json:"active"`
		Events []string `json:"events"`
		Config struct {
			URL string `json:"url"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(jsonPart(out)), &raw); err != nil {
		return nil, fmt.Errorf("gh api hooks: %w", err)
	}
	hooks := make([]Hook, len(raw))
	for i, h := range raw {
		hooks[i] = Hook{ID: h.ID, URL: h.Config.URL, Events: h.Events, Active: h.Active}
	}
	return hooks, nil
}

// CreateHook adds a webhook to the repository in dir and returns its id.
func (g *Git) CreateHook(ctx context.Context, projectID, dir string, c HookConfig) (int64, error) {
	out, err := g.ghWithInput(ctx, projectID, dir, hookBody(c, true), "api", "-X", "POST", "repos/{owner}/{repo}/hooks")
	if err != nil {
		return 0, err
	}
	var h struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(jsonPart(out)), &h); err != nil || h.ID == 0 {
		return 0, fmt.Errorf("gh api hooks: no hook id in the answer")
	}
	return h.ID, nil
}

// UpdateHook sets webhook id of the repository in dir to c.
func (g *Git) UpdateHook(ctx context.Context, projectID, dir string, id int64, c HookConfig) error {
	_, err := g.ghWithInput(ctx, projectID, dir, hookBody(c, false), "api", "-X", "PATCH", "repos/{owner}/{repo}/hooks/"+strconv.FormatInt(id, 10))
	return err
}

// DeleteHook removes webhook id from the repository in dir.
func (g *Git) DeleteHook(ctx context.Context, projectID, dir string, id int64) error {
	_, err := g.ghQuiet(ctx, projectID, dir, "api", "-X", "DELETE", "repos/{owner}/{repo}/hooks/"+strconv.FormatInt(id, 10))
	return err
}

// PingHook asks GitHub to send webhook id a ping event.
func (g *Git) PingHook(ctx context.Context, projectID, dir string, id int64) error {
	_, err := g.ghQuiet(ctx, projectID, dir, "api", "-X", "POST", "repos/{owner}/{repo}/hooks/"+strconv.FormatInt(id, 10)+"/pings")
	return err
}

var httpStatusRe = regexp.MustCompile(`\(HTTP (\d{3})\)`)

// GitHubHTTPStatus is the HTTP status GitHub answered a failed gh api call
// with ("gh: Not Found (HTTP 404)"), found in its error's text (also once
// it came back from a runner), or 0.
func GitHubHTTPStatus(err error) int {
	if err == nil {
		return 0
	}
	m := httpStatusRe.FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
