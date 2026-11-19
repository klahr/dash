// Package applink is the web app's addresses: the paths that open a view of
// a project, a task, a goal, a commit, an issue or a Planning card. The
// server serves the app's page at each of them (internal/api/static.go), the
// sign-in comes back to them (internal/auth), and links Dash sends out
// (notifications) are built here. web/static/app/router.js reads and writes
// the same paths in the browser; keep the two in step.
//
//	/p/{project}                     the project's board
//	/p/{project}/{view}              board, issues, scans, run, try, history, goals, deploys, files, checklist
//	                                 (run and try: the Shell page at main and at a commit)
//	/p/{project}/tasks/{id}          a task, open over the board
//	/p/{project}/goals/{id}          a goal, open on Goals
//	/p/{project}/history/{sha}       a commit, open on History
//	/p/{project}/deploys/{id}        a deploy, open on Deploys
//	/p/{project}/issues/{id}         an issue, expanded on Issues
//	/planning                        Planning (the org's)
//	/planning/cards/{id}             a Planning card, open
//
// {project} is the project's id for a project of this Dash, or
// {id}@{computer} for one on another computer of a hub (platform admins).
package applink

import (
	"net/url"
	"strings"
)

// Views are the project views a path may name.
var Views = map[string]bool{
	"board": true, "issues": true, "scans": true, "run": true, "try": true,
	"history": true, "goals": true, "deploys": true, "files": true, "checklist": true,
}

// items are the kinds of thing /p/{project}/{kind}/{id} opens.
var items = map[string]bool{"tasks": true, "goals": true, "history": true, "issues": true, "deploys": true}

// Project is the path of project id's board.
func Project(id string) string { return "/p/" + url.PathEscape(id) }

// Task is the path that opens task id of project pid.
func Task(pid, id string) string { return Project(pid) + "/tasks/" + url.PathEscape(id) }

// Goal is the path that opens goal id of project pid.
func Goal(pid, id string) string { return Project(pid) + "/goals/" + url.PathEscape(id) }

// Issue is the path that opens issue id of project pid.
func Issue(pid, id string) string { return Project(pid) + "/issues/" + url.PathEscape(id) }

// Card is the path that opens Planning card id.
func Card(id string) string { return "/planning/cards/" + url.PathEscape(id) }

// Is reports whether path is one of the app's own paths (above), which
// serve the app's page. Anything else under /p/ or /planning/ is not.
func Is(path string) bool {
	if path == "/planning" {
		return true
	}
	if _, ok := CardID(path); ok {
		return true
	}
	rest, ok := strings.CutPrefix(path, "/p/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	if len(parts) > 3 {
		return false
	}
	for _, s := range parts {
		if !segment(s) {
			return false
		}
	}
	switch len(parts) {
	case 1:
		return true
	case 2:
		return Views[parts[1]]
	case 3:
		return items[parts[1]]
	}
	return false
}

// CardID is the card a /planning/cards/{id} path opens.
func CardID(path string) (string, bool) {
	id, ok := strings.CutPrefix(path, "/planning/cards/")
	if !ok || strings.Contains(id, "/") || !segment(id) {
		return "", false
	}
	return id, true
}

// segment reports whether s (decoded) is a usable path segment: not empty,
// not a dot segment, no control characters or backslashes. A computer's
// name may have spaces.
func segment(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 200 {
		return false
	}
	for _, c := range s {
		if c < 0x20 || c == 0x7f || c == '\\' {
			return false
		}
	}
	return true
}
