// Package screenshot takes before/after screenshots of a finished task's
// pages, for Review: it runs the project's start server command
// (run_command) on the task's branch and on the commit it started from,
// opens each page in headless Chrome at a wide and a phone size, and keeps
// small WebP pictures in the task's folder under the data directory.
//
// Which pages: those the project always captures
// (model.ScreenshotSettings.Pages), then those Claude lists in its final
// message under "Screenshot pages:" (ParsePages). Whether to capture at all
// is Decide's. Capturing never fails a task: what goes wrong is a note.
package screenshot

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/klahr/dash/internal/model"
)

const (
	// MaxListed is how many of the pages Claude lists are captured.
	MaxListed = 4
	// MaxPages is how many pages one task's capture takes in all.
	MaxPages = 6
	// maxLabel and maxPath bound a page's label and path.
	maxLabel = 80
	maxPath  = 512
)

// Hint tells a task's Claude how to list the pages to capture.
const Hint = "This project takes before/after screenshots of the pages a task changes, for its review. " +
	"If your change shows in the app, end your final message with the pages a reviewer should look at (at most 4), " +
	"after a line `Screenshot pages:`, one per line as `- /path — Short label`, e.g. `- /settings — Settings → Board`. " +
	"Use paths of the running app (a ?query or #fragment if that opens the right view). Leave the list out if no page changes."

var (
	// headRE is the line that starts the list: "Screenshot pages:", also as
	// a Markdown heading or in bold.
	headRE = regexp.MustCompile(`(?i)^[\s#>*_]*screenshot\s+pages?[\s*_]*:?[\s*_]*$`)
	itemRE = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(.*)$`)
	// sepRE separates a page's path from its label.
	sepRE = regexp.MustCompile(`^\s*(?:—|–|-{1,2}|:|\|)?\s*`)
)

// ParsePages reads the pages Claude listed in report: the list items after
// a "Screenshot pages:" line, each a path (backticks allowed) and a label.
// Items without a valid path are skipped; at most MaxListed are returned.
func ParsePages(report string) []model.ScreenshotPage {
	lines := strings.Split(strings.ReplaceAll(report, "\r\n", "\n"), "\n")
	start := -1
	for i, l := range lines {
		if headRE.MatchString(l) {
			start = i + 1 // the last such line counts: the final list
		}
	}
	if start < 0 {
		return nil
	}
	var out []model.ScreenshotPage
	started := false
	for _, l := range lines[start:] {
		if strings.TrimSpace(l) == "" {
			if started {
				break
			}
			continue
		}
		m := itemRE.FindStringSubmatch(l)
		if m == nil {
			break
		}
		started = true
		if p, ok := parseItem(m[1]); ok && !hasPath(out, p.Path) {
			out = append(out, p)
			if len(out) == MaxListed {
				break
			}
		}
	}
	return out
}

// parseItem reads "/path — Label" (or "`/path` Label", "Label: /path").
func parseItem(s string) (model.ScreenshotPage, bool) {
	s = strings.TrimSpace(s)
	// The path, in backticks or as the first word.
	var raw, rest string
	if strings.HasPrefix(s, "`") {
		end := strings.Index(s[1:], "`")
		if end < 0 {
			return model.ScreenshotPage{}, false
		}
		raw, rest = s[1:end+1], s[end+2:]
	} else if strings.HasPrefix(s, "/") {
		raw, rest, _ = strings.Cut(s, " ")
	} else if i := strings.Index(s, "`/"); i >= 0 {
		// "Settings → Board: `/settings`"
		end := strings.Index(s[i+1:], "`")
		if end < 0 {
			return model.ScreenshotPage{}, false
		}
		raw, rest = s[i+1:i+1+end], strings.TrimRight(strings.TrimSpace(s[:i]), ":—–- ")
	} else {
		return model.ScreenshotPage{}, false
	}
	p, ok := CleanPath(raw)
	if !ok {
		return model.ScreenshotPage{}, false
	}
	label := strings.TrimSpace(sepRE.ReplaceAllString(rest, ""))
	label = strings.Trim(label, "*_")
	return model.ScreenshotPage{Path: p, Label: CleanLabel(label, p)}, true
}

// CleanPath checks a page's path: it starts with one "/" (never a full URL
// or "//host"), and has no spaces or control characters.
func CleanPath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" || len(p) > maxPath || p[0] != '/' || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "", false
	}
	for _, r := range p {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '\\' {
			return "", false
		}
	}
	return p, true
}

// CleanLabel trims a page's label to one short line; empty, it is the path.
func CleanLabel(label, path string) string {
	label = strings.Join(strings.Fields(label), " ")
	if label == "" {
		return path
	}
	if utf8.RuneCountInString(label) > maxLabel {
		r := []rune(label)
		label = string(r[:maxLabel-1]) + "…"
	}
	return label
}

func hasPath(ps []model.ScreenshotPage, p string) bool {
	for _, x := range ps {
		if x.Path == p {
			return true
		}
	}
	return false
}

// Pages are the pages to capture: the project's own, then those Claude
// listed, each once, at most MaxPages.
func Pages(fixed, listed []model.ScreenshotPage) []model.ScreenshotPage {
	var out []model.ScreenshotPage
	for _, p := range append(append([]model.ScreenshotPage(nil), fixed...), listed...) {
		path, ok := CleanPath(p.Path)
		if !ok || hasPath(out, path) {
			continue
		}
		out = append(out, model.ScreenshotPage{Path: path, Label: CleanLabel(p.Label, path)})
		if len(out) == MaxPages {
			break
		}
	}
	return out
}

// TouchesUI reports whether one of changed (paths relative to the
// repository root) is under one of uiPaths: a folder ("web/" or "web"), a
// file, or a glob of one path segment each ("src/*/views"). No uiPaths:
// any change counts.
func TouchesUI(changed, uiPaths []string) bool {
	var dirs []string
	for _, u := range uiPaths {
		u = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(u), "./"), "/")
		u = strings.TrimSuffix(u, "/")
		if u != "" && u != "." {
			dirs = append(dirs, u)
		}
	}
	if len(dirs) == 0 {
		return len(changed) > 0
	}
	for _, f := range changed {
		for _, d := range dirs {
			if under(f, d) {
				return true
			}
		}
	}
	return false
}

// under reports whether file is dir, or in it; dir may be a glob.
func under(file, dir string) bool {
	if !strings.ContainsAny(dir, "*?[") {
		return file == dir || strings.HasPrefix(file, dir+"/")
	}
	// Match the glob against file and each folder file is in.
	for f := file; f != "." && f != "/" && f != ""; f = path.Dir(f) {
		if ok, _ := path.Match(dir, f); ok {
			return true
		}
	}
	return false
}

// Decision is whether a finished task's screenshots are taken. Note says why
// not when the reviewer should know (no Chrome, no pages); a project or a
// change screenshots don't apply to has none, and no set is kept.
type Decision struct {
	Run   bool
	Note  string
	Pages []model.ScreenshotPage
}

// Decide says whether to capture the pages of a task of p that changed
// changed files, with listed the pages Claude listed, and chrome the
// browser found here ("": none).
func Decide(p model.Project, changed []string, listed []model.ScreenshotPage, chrome string) Decision {
	switch {
	case !p.ScreenshotsOn():
		return Decision{} // off: the default
	case p.TerminalPreview() || strings.TrimSpace(p.RunCommand) == "":
		if why := p.Held(model.CmdRun); why != "" {
			return Decision{Note: "Screenshots skipped: " + why}
		}
		return Decision{} // not a web project with a server to start
	case len(changed) == 0 || !TouchesUI(changed, p.Screenshots.UIPaths):
		return Decision{} // nothing in the UI changed
	}
	pages := Pages(p.Screenshots.Pages, listed)
	switch {
	case len(pages) == 0:
		return Decision{Note: "No screenshots: Claude listed no pages, and the project sets none."}
	case chrome == "":
		return Decision{Pages: pages, Note: "No screenshots: this computer has no Chrome or Chromium (set DASH_CHROME to its path)."}
	}
	if why := p.Held(model.CmdScreenshot); why != "" {
		return Decision{Pages: pages, Note: "No screenshots: " + why}
	}
	return Decision{Run: true, Pages: pages}
}

// shotName is the file of one picture: the run, the page's place in the
// list, the size and the side, e.g. "r3-p1-wide-after.webp".
func shotName(run, page int, size, side string) string {
	return fmt.Sprintf("r%d-p%d-%s-%s.webp", run, page, size, side)
}

// nameRE is what shotName makes: the only names served.
var nameRE = regexp.MustCompile(`^r\d{1,6}-p\d{1,2}-(wide|phone)-(before|after)\.webp$`)

// ValidName reports whether name can be a picture's file name.
func ValidName(name string) bool { return nameRE.MatchString(name) }
