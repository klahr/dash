package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// What Speak summarises: the kinds of text Dash reads aloud.
const (
	SpeakReply    = "reply"    // Claude's reply in a chat
	SpeakApproval = "approval" // a permission request: what Claude wants to do, and why
	SpeakStatus   = "status"   // a task finished or failed
)

// SpeakKinds are the kinds Speak takes.
var SpeakKinds = []string{SpeakReply, SpeakApproval, SpeakStatus}

// speakAsk is what Speak asks the small model for each kind.
var speakAsk = map[string]string{
	SpeakReply: "Below is a reply from Claude, an AI coding assistant, to a developer. Say in one or two short " +
		"sentences what Claude did or changed, or what it found, and what it needs from the developer, if anything " +
		"(a question it asked, a choice to make). Speak to the developer about Claude in the third person " +
		"(\"Claude fixed …\", \"Claude asks whether …\").",
	SpeakApproval: "Below is a permission request: an action Claude, an AI coding assistant, wants to take, and " +
		"perhaps why. Say the action and its reason as one short question to the developer, for example " +
		"\"Claude wants to run go test on the agent package, okay?\". Describe a command by what it does; " +
		"don't read it out.",
	SpeakStatus: "Below is the news of a coding task that Claude, an AI coding assistant, worked on. Say in one or " +
		"two short sentences the task, its result (ready for review, failed and why, or merged) and whether the " +
		"tests passed, if the news says.",
}

// speakRules are the same for every kind: it is heard, not read.
const speakRules = "\n\nIt will be read aloud by a speech synthesizer, so write plain spoken English: no Markdown, " +
	"no lists, no code, no quotes, no URLs. Name a file by its short name (\"the title file\", \"chat.js\"), never " +
	"a path, and don't spell out identifiers, hashes or numbers nobody would say aloud. At most 40 words. " +
	"No preamble.\n\nText:\n"

// maxSpeakInput is how much of the text Speak reads, in bytes.
const maxSpeakInput = 12000

// maxSpoken is how long Speak's summary may be, in bytes.
const maxSpoken = 400

// Speak summarises text of kind (SpeakKinds) as a sentence or two to be
// read aloud, in plain speech.
func (t Titler) Speak(ctx context.Context, kind, text string) (string, error) {
	ask, ok := speakAsk[kind]
	if !ok {
		return "", fmt.Errorf("no spoken summary of kind %q", kind)
	}
	text = cutEnd(strings.TrimSpace(text), maxSpeakInput)
	out, err := t.run(ctx, PromptRequest{Model: t.Model, Timeout: 30 * time.Second},
		"You write what a voice assistant says aloud. Reply with only the words to say.", ask+speakRules+text)
	if err != nil {
		return "", err
	}
	return CleanSpoken(out)
}

var (
	spokenFence = regexp.MustCompile("(?s)```.*?(```|$)")
	spokenLink  = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	spokenURL   = regexp.MustCompile(`\bhttps?://\S+`)
	spokenMarks = regexp.MustCompile("[`*_#>|]+")
	spokenList  = regexp.MustCompile(`(?m)^\s*(?:[-+•]|\d+[.)])\s+`)
	// A path of three parts or more: its last part is said.
	spokenPath = regexp.MustCompile(`(?:~|\.{1,2})?/?(?:[\w@.+-]+/){2,}([\w@.+-]+)`)
)

// CleanSpoken makes the model's summary fit to be read aloud: one
// paragraph of plain words, without Markdown, code, links or long paths
// (left in despite the prompt), at most maxSpoken bytes, cut at a
// sentence. An error when nothing is left.
func CleanSpoken(s string) (string, error) {
	s = spokenFence.ReplaceAllString(s, " ")
	s = spokenLink.ReplaceAllString(s, "$1")
	s = spokenURL.ReplaceAllString(s, "a link")
	s = spokenList.ReplaceAllString(s, "")
	s = spokenPath.ReplaceAllString(s, "$1")
	s = spokenMarks.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, "\"'“”‘’ ")
	if len(s) > maxSpoken {
		cut := strings.ToValidUTF8(s[:maxSpoken], "")
		if i := strings.LastIndexAny(cut, ".!?"); i > maxSpoken/3 {
			cut = cut[:i+1]
		} else if i := strings.LastIndexByte(cut, ' '); i > 0 {
			cut = cut[:i] + "…"
		}
		s = cut
	}
	if s == "" {
		return "", fmt.Errorf("no usable spoken summary")
	}
	return s, nil
}
