package model

import "time"

// CIRun is a GitHub Actions workflow run (gitops.Run).
type CIRun struct {
	ID         int64     `json:"id"`
	Workflow   string    `json:"workflow"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`     // queued | in_progress | completed | …
	Conclusion string    `json:"conclusion"` // success | failure | cancelled | skipped | …
	SHA        string    `json:"sha"`
	Branch     string    `json:"branch,omitempty"` // the branch or tag it ran for
	URL        string    `json:"url"`
	Event      string    `json:"event"`
	CreatedAt  time.Time `json:"created_at"`
}

func (r CIRun) Done() bool { return r.Status == "completed" }

// Failed reports a finished run that didn't pass (cancelled runs don't count).
func (r CIRun) Failed() bool {
	switch r.Conclusion {
	case "failure", "timed_out", "startup_failure":
		return true
	}
	return false
}

// ReleaseBuild is a release tag of a project and how the GitHub Actions
// runs pushing it started went (ci.Release*: building, published, failed,
// no_build). Dash keeps one per tag, whether it pushed the tag (from this
// machine, or from a runner for a hub) or found its runs on GitHub.
type ReleaseBuild struct {
	ProjectID string    `json:"project_id,omitempty"`
	Tag       string    `json:"tag"`
	SHA       string    `json:"sha"`
	State     string    `json:"state"`
	Runs      []CIRun   `json:"runs"`
	URL       string    `json:"url,omitempty"` // the GitHub release (or tag) page
	Pushed    time.Time `json:"pushed"`        // when the tag was pushed (or its first run started)
	Updated   time.Time `json:"updated,omitzero"`
	// Found: Dash didn't push the tag, it found the tag's runs on GitHub.
	Found bool `json:"found,omitempty"`
}
