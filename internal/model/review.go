package model

import (
	"strings"
)

// DefaultReviewPrompt is what Claude's review of a task asks when the
// project has no ReviewPrompt of its own. Keep it the same as
// DEFAULT_REVIEW_PROMPT in web/static/app/drawmode.js, which the task
// chat's Review button sends.
const DefaultReviewPrompt = `Review this task's changes as a strict senior reviewer, before they are merged. Don't modify any files.

Check, in this order:
1. Correctness: does it fully do what the instructions ask? Bugs, unhandled edge cases, regressions, callers that break.
2. Security and data loss risks.
3. Tests: are the changes covered where the project has tests? Run the relevant ones if you can.
4. Scope: unrelated changes, leftover debug code, dead code, code that doesn't match the surrounding style.

Read the surrounding code where you need to. Start with a one-line verdict, "Ready to merge" or "Needs changes", then list each must-fix issue with file:line and a concrete fix, then optional nice-to-haves. Leave out praise and anything you haven't verified.`

// Review verdicts: the first line of a review's answer.
const (
	VerdictReady   = "Ready to merge"
	VerdictChanges = "Needs changes"
)

// AutoReviewPrompt is what the automatic review of a finished task asks:
// the project's review prompt (or DefaultReviewPrompt), with the verdict
// line Dash reads (ReviewVerdict) asked for whatever that prompt says.
func AutoReviewPrompt(p Project) string {
	prompt := strings.TrimSpace(p.ReviewPrompt)
	if prompt == "" {
		prompt = DefaultReviewPrompt
	}
	return prompt + "\n\nThis review decides whether the task is approved automatically. Make the first line of your answer exactly \"" +
		VerdictReady + "\" when you found no must-fix issue, else \"" + VerdictChanges + "\"."
}

// ReviewVerdict reads a review's answer: whether its first line says it is
// ready to merge (any other answer counts as issues found), and that line.
func ReviewVerdict(answer string) (ok bool, line string) {
	for _, l := range strings.Split(answer, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	clean := strings.ToLower(strings.Trim(line, "#*_>`'\" \t"))
	clean = strings.TrimPrefix(clean, "verdict:")
	clean = strings.Trim(clean, "#*_>`'\" \t")
	ready := strings.ToLower(VerdictReady)
	if !strings.HasPrefix(clean, ready) {
		return false, line
	}
	// "Ready to merge" alone, or followed by punctuation: not "Ready to merge
	// once …" or "Ready to merge? No".
	rest := strings.TrimSpace(strings.Trim(clean[len(ready):], "*_`'\""))
	return rest == "" || rest == "." || rest == "!", line
}
