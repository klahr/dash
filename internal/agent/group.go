package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// An IssueGroup is issues Group thinks one task should fix together, with
// that task's title.
type IssueGroup struct {
	Title string `json:"title"`
	// Issues are the group's issues, as indexes (0-based) into the list
	// Group was given.
	Issues []int `json:"issues"`
}

// Group sorts a numbered list of issues (see the caller's listing, "1. …")
// into groups of similar ones, each to be fixed by one task, and names each
// group's task. Nothing is stored: the caller checks the groups (every
// issue in one) and creates the tasks.
func (t Titler) Group(ctx context.Context, issues string) ([]IssueGroup, error) {
	if len(issues) > maxGroup {
		return nil, errors.New("too many issues to group at once: pick fewer")
	}
	model := t.SplitModel
	if model == "" {
		model = "sonnet"
	}
	out, err := t.run(ctx, model, 3*time.Minute,
		"You triage issues found in a software project into tasks for a coding agent. Reply with only JSON.",
		"Group the numbered issues below into tasks for a coding agent working in the repository. "+
			"Put issues in one task when fixing them together makes sense: the same kind of problem or "+
			"pattern, the same rule, or the same file, component or feature, so one change fixes them "+
			"consistently. Keep unrelated issues apart; an issue unlike the others is a task of its own. "+
			"Keep each task small enough for a reviewer to read its diff in one sitting. Every issue goes "+
			"in exactly one task.\n\n"+
			"Reply with only a JSON array, one object per task: "+
			`{"title": "...", "issues": [1, 4]}. `+
			"title: at most 8 words, sentence case, starting with a verb, saying what the task fixes "+
			"across its issues (e.g. \"Escape user input in admin templates\"), no issue count. "+
			"issues: the numbers of the issues in the task.\n\nIssues:\n"+issues)
	if err != nil {
		return nil, err
	}
	return parseGroups(out)
}

// maxGroup is how long a listing of issues Group takes, in bytes.
const maxGroup = 60000

// parseGroups reads Group's reply: a JSON array, perhaps in a code fence
// or with words around it. Issue numbers are 1-based there and 0-based in
// an IssueGroup; ones below 1 are dropped (the caller drops ones past the
// end of its list).
func parseGroups(s string) ([]IssueGroup, error) {
	i, j := strings.IndexByte(s, '['), strings.LastIndexByte(s, ']')
	if i < 0 || j < i {
		return nil, errors.New("no groups were proposed")
	}
	var raw []struct {
		Title  string `json:"title"`
		Issues []int  `json:"issues"`
	}
	if err := json.Unmarshal([]byte(s[i:j+1]), &raw); err != nil {
		return nil, fmt.Errorf("the proposed groups weren't readable: %w", err)
	}
	var out []IssueGroup
	for _, r := range raw {
		g := IssueGroup{}
		g.Title, _ = cleanTitle(r.Title) // untitled: named from its issues
		for _, n := range r.Issues {
			if n >= 1 {
				g.Issues = append(g.Issues, n-1)
			}
		}
		if len(g.Issues) > 0 {
			out = append(out, g)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no groups were proposed")
	}
	return out, nil
}
