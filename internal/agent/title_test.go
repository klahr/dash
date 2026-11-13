package agent

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Add rate limiting to login\n":         "Add rate limiting to login",
		"\"Fix currency rounding.\"":           "Fix currency rounding",
		"**Validate payment requests**\nextra": "Validate payment requests",
	} {
		if got, err := cleanTitle(in); err != nil || got != want {
			t.Errorf("cleanTitle(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := cleanTitle("  \n"); err == nil {
		t.Error("empty output should be an error")
	}
}

// Calls the real CLI; opt in with DASH_REAL_CLAUDE=1.
func TestTitlerReal(t *testing.T) {
	if os.Getenv("DASH_REAL_CLAUDE") == "" {
		t.Skip("set DASH_REAL_CLAUDE=1 to call the claude CLI")
	}
	title, err := Titler{}.Title(context.Background(), "The login handler should reject more than 5 failed attempts per IP within 15 minutes and return 429 with Retry-After.")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("title: %q", title)
}

func TestCleanSummary(t *testing.T) {
	if got, err := cleanSummary("\"Added a summary\nline to Done todos.\"\n"); err != nil || got != "Added a summary line to Done todos." {
		t.Errorf("cleanSummary = %q, %v", got, err)
	}
	if _, err := cleanSummary(" "); err == nil {
		t.Error("empty output should be an error")
	}
}

func TestCleanRefined(t *testing.T) {
	for in, want := range map[string]string{
		"  Add a button.\n":                    "Add a button.",
		"```markdown\n- Add a button\n```\n":   "- Add a button",
		"Run:\n```sh\ngo test\n```\nThen fix.": "Run:\n```sh\ngo test\n```\nThen fix.",
	} {
		if got, err := cleanRefined(in); err != nil || got != want {
			t.Errorf("cleanRefined(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := cleanRefined(" \n"); err == nil {
		t.Error("empty output should be an error")
	}
}

func TestParseParts(t *testing.T) {
	out := "Here you go:\n```json\n[" +
		`{"title": "\"Add the users table.\"", "prompt": " Create the table. "},` +
		`{"title": "Skipped", "prompt": "  "},` +
		`{"title": "Add the signup form", "prompt": "Build the form.", "after": [1, 1, 2, 3, 4, 0]},` +
		`{"title": "", "prompt": "Write the docs.", "after": [3]}` +
		"]\n```\nDone."
	got, err := parseParts(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []Part{
		{Title: "Add the users table", Prompt: "Create the table."},
		{Title: "Add the signup form", Prompt: "Build the form.", After: []int{0}},
		{Prompt: "Write the docs.", After: []int{1}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseParts:\n got %+v\nwant %+v", got, want)
	}
	for _, bad := range []string{"", "no JSON here", "[]", `[{"title": "x", "prompt": ""}]`, "[{oops}]"} {
		if _, err := parseParts(bad); err == nil {
			t.Errorf("parseParts(%q): no error", bad)
		}
	}
	var many strings.Builder
	many.WriteString("[")
	for i := range MaxParts + 3 {
		if i > 0 {
			many.WriteString(",")
		}
		fmt.Fprintf(&many, `{"title": "Part %d", "prompt": "Do %d.", "after": [%d]}`, i, i, MaxParts+1)
	}
	many.WriteString("]")
	if got, err := parseParts(many.String()); err != nil || len(got) != MaxParts || got[MaxParts-1].After != nil {
		t.Errorf("parseParts of %d parts: %d parts, %v", MaxParts+3, len(got), err)
	}
}

// Calls the real CLI; opt in with DASH_REAL_CLAUDE=1.
func TestSplitReal(t *testing.T) {
	if os.Getenv("DASH_REAL_CLAUDE") == "" {
		t.Skip("set DASH_REAL_CLAUDE=1 to call the claude CLI")
	}
	parts, err := Titler{}.Split(context.Background(), "Add user accounts: a users table with email and password hash, "+
		"a signup form and a login form, sessions in a cookie, and a page to change the password. Document it in docs/accounts.md.")
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range parts {
		t.Logf("%d. %s (after %v)\n%s", i+1, p.Title, p.After, p.Prompt)
	}
}

func TestCleanSuggestion(t *testing.T) {
	for in, want := range map[string]string{
		"Run the tests\n":                        "Run the tests",
		"\"Create a task for it\"":               "Create a task for it",
		"**Yes, do that**\nBecause Claude asked": "Yes, do that",
		"User: Merge it":                         "Merge it",
		"you: \"Ship it\"":                       "Ship it",
		"NONE":                                   "",
		"none.":                                  "",
		"  \n":                                   "",
		"I can also add tests if you'd like.":    "",
		"Would you like me to open a task?":      "",
		"Let me know if anything else comes up":  "",
		"Claude: Sure, here it is":               "",
		strings.Repeat("word ", 40):              "",
	} {
		if got := CleanSuggestion(in); got != want {
			t.Errorf("CleanSuggestion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSuggestTranscript(t *testing.T) {
	var h []ChatMessage
	for i := range 10 {
		h = append(h, ChatMessage{Role: "user", Content: fmt.Sprintf("question %d", i)}, ChatMessage{Role: "assistant", Content: fmt.Sprintf("answer %d", i)})
	}
	got := SuggestTranscript(h)
	want := "User: question 7\n\nClaude: answer 7\n\nUser: question 8\n\nClaude: answer 8\n\nUser: question 9\n\nClaude (latest message): answer 9"
	if got != want {
		t.Errorf("SuggestTranscript:\n got %q\nwant %q", got, want)
	}

	// A long reply keeps its end (where its questions are), a long question its start.
	long := []ChatMessage{
		{Role: "user", Content: "START " + strings.Repeat("q", 5000) + " END"},
		{Role: "assistant", Content: "START " + strings.Repeat("a", 5000) + " Shall I add a test?"},
	}
	got = SuggestTranscript(long)
	if !strings.Contains(got, "User: START") || strings.Contains(got, "q END") ||
		!strings.HasSuffix(got, "Shall I add a test?") || strings.Contains(got, "Claude: START") {
		t.Errorf("long messages weren't cut as expected: %q…%q", got[:40], got[len(got)-40:])
	}
	if len(got) > maxTranscript {
		t.Errorf("transcript is %d bytes, over %d", len(got), maxTranscript)
	}
	if SuggestTranscript(nil) != "" {
		t.Error("an empty conversation has a transcript")
	}
}

// Suggest runs the small model on the conversation with the prompt that
// asks for the user's next message, and reads its reply.
func TestSuggestAsksHaiku(t *testing.T) {
	dir := t.TempDir()
	bin := dir + "/claude"
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + dir + "/args\ncat > " + dir + "/stdin\nprintf '%s' \"$REPLY\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := SuggestTranscript([]ChatMessage{{Role: "user", Content: "Why does login fail?"}, {Role: "assistant", Content: "The cookie lacks Secure. Want me to fix it?"}})
	for reply, want := range map[string]string{"\"Yes, fix it\"\n": "Yes, fix it", "NONE": ""} {
		t.Setenv("REPLY", reply)
		got, err := Titler{Bin: bin}.Suggest(context.Background(), transcript)
		if err != nil || got != want {
			t.Errorf("reply %q: Suggest = %q, %v; want %q", reply, got, err, want)
		}
	}
	args, _ := os.ReadFile(dir + "/args")
	if !strings.Contains(string(args), "--model\nhaiku\n") || !strings.Contains(string(args), "predict the reply a developer") {
		t.Errorf("claude ran with %q", args)
	}
	stdin, _ := os.ReadFile(dir + "/stdin")
	if !strings.Contains(string(stdin), "never Claude offering help") || !strings.Contains(string(stdin), "never ask for it again") || !strings.HasSuffix(string(stdin), transcript) {
		t.Errorf("claude was asked %q", stdin)
	}
	// Nothing to go on: no call.
	if got, err := (Titler{Bin: dir + "/missing"}).Suggest(context.Background(), " "); got != "" || err != nil {
		t.Errorf("Suggest of an empty transcript = %q, %v", got, err)
	}
}

func TestSuggestTranscriptMarks(t *testing.T) {
	for _, c := range []struct {
		name    string
		history []ChatMessage
		done    []string
		want    string
	}{
		{"latest reply marked",
			[]ChatMessage{{Role: "user", Content: "Why does login fail?"}, {Role: "assistant", Content: "The cookie lacks Secure."}},
			nil,
			"User: Why does login fail?\n\nClaude (latest message): The cookie lacks Secure."},
		{"actions noted after the latest reply",
			[]ChatMessage{{Role: "user", Content: "Make a task to fix it"}, {Role: "assistant", Content: "I've queued a task for this."}},
			[]string{"created a task", "queued a task"},
			"User: Make a task to fix it\n\nClaude (latest message): I've queued a task for this.\n" +
				"Already done in Claude's latest message: created a task; queued a task."},
		{"only the last reply is the latest",
			[]ChatMessage{{Role: "assistant", Content: "Hi"}, {Role: "user", Content: "Fix it"}, {Role: "assistant", Content: "Done."}},
			nil,
			"Claude: Hi\n\nUser: Fix it\n\nClaude (latest message): Done."},
		{"ending on the user: no latest reply, no note",
			[]ChatMessage{{Role: "assistant", Content: "Hi"}, {Role: "user", Content: "Fix it"}},
			[]string{"edited files"},
			"Claude: Hi\n\nUser: Fix it"},
		{"an empty last reply is skipped",
			[]ChatMessage{{Role: "user", Content: "Fix it"}, {Role: "assistant", Content: "  "}},
			nil,
			"User: Fix it"},
	} {
		if got := SuggestTranscript(c.history, c.done...); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}

	// A long latest reply keeps its end, and the note, within the limit.
	long := []ChatMessage{
		{Role: "user", Content: strings.Repeat("q", 5000)},
		{Role: "assistant", Content: strings.Repeat("a", 5000) + " Shall I queue it?"},
	}
	got := SuggestTranscript(long, "created a task")
	if !strings.Contains(got, "Claude (latest message): … ") || !strings.HasSuffix(got, "Shall I queue it?\nAlready done in Claude's latest message: created a task.") {
		t.Errorf("long latest reply: %q", got[len(got)-80:])
	}
	if len(got) > maxTranscript {
		t.Errorf("transcript is %d bytes, over %d", len(got), maxTranscript)
	}
}

func TestActionNote(t *testing.T) {
	for tool, want := range map[string]string{
		"mcp__dash_board__create_todo":        "created a task",
		"mcp__dash_board__queue_todo":         "queued a task",
		"mcp__dash_board__add_checklist_item": "added a checklist item",
		"Edit":                                "edited files",
		"mcp__dash_board__list_todos":         "",
		"Read":                                "",
		"":                                    "",
	} {
		if got := ActionNote(tool); got != want {
			t.Errorf("ActionNote(%q) = %q, want %q", tool, got, want)
		}
	}
}

func TestFitSuggestion(t *testing.T) {
	ask := func(user, reply string) []ChatMessage {
		return []ChatMessage{{Role: "user", Content: user}, {Role: "assistant", Content: reply}}
	}
	for _, c := range []struct {
		name       string
		suggestion string
		history    []ChatMessage
		done       []string
		want       string
	}{
		{"fits", "Yes, fix it", ask("Why does login fail?", "The cookie lacks Secure. Want me to fix it?"), nil, "Yes, fix it"},
		{"repeats the user", "Fix the login bug", ask("fix the login bug!", "Done: the cookie now has Secure."), nil, ""},
		{"repeats the user, other punctuation", "Run the tests.", ask("Run   the TESTS", "All 42 pass."), nil, ""},
		{"only the last user message counts", "Run the tests",
			[]ChatMessage{{Role: "user", Content: "Run the tests"}, {Role: "assistant", Content: "3 fail."}, {Role: "user", Content: "Fix them"}, {Role: "assistant", Content: "Fixed. Run them again?"}},
			nil, "Run the tests"},
		{"task queued, said in the reply", "Create a task for it", ask("The export is slow", "I've queued a task for this."), nil, ""},
		{"task queued, by its tools", "Create a task for it", ask("The export is slow", "Done, it'll run shortly."), []string{"created a task", "queued a task"}, ""},
		{"task queued: queue it again", "Queue it", ask("Make a task for it", "Task created and queued."), nil, ""},
		{"task created as a draft: queueing it fits", "Queue it", ask("Make a task for it", "I've created a draft task for this."), []string{"created a task"}, "Queue it"},
		{"task created: another one", "Make it a task", ask("Make a task for it", "I created the task."), nil, ""},
		{"checklist item added", "Add it to the checklist", ask("Note that down", "I've added it to the checklist."), []string{"added a checklist item"}, ""},
		{"nothing done: a task fits", "Create a task for it", ask("The export is slow", "It reads every row twice. Want a task to fix it?"), nil, "Create a task for it"},
		{"empty", "", ask("Hi", "Hello"), nil, ""},
		{"only punctuation", "?!", ask("Hi", "Hello"), nil, ""},
	} {
		if got := FitSuggestion(c.suggestion, c.history, c.done); got != c.want {
			t.Errorf("%s: FitSuggestion(%q) = %q, want %q", c.name, c.suggestion, got, c.want)
		}
	}
}
