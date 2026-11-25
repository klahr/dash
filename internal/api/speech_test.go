package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// failSmall is a fakeSmall whose spoken summaries fail, or answer reply.
type failSmall struct {
	fakeSmall
	fail  error
	reply string
}

func (f *failSmall) Speak(ctx context.Context, kind, text string) (string, error) {
	if _, err := f.fakeSmall.Speak(ctx, kind, text); err != nil {
		return "", err
	}
	if f.fail != nil {
		return "", f.fail
	}
	return f.reply, nil
}

func speechCall(t *testing.T, s *Server, body string) (int, spokenSummary) {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/speech/summary", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	asOwnerHandler(t, s).ServeHTTP(w, r)
	var out spokenSummary
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %v", w.Body, err)
		}
	}
	return w.Code, out
}

func speechBody(kind, text string) string {
	b, _ := json.Marshal(map[string]string{"kind": kind, "text": text})
	return string(b)
}

// longReply is a reply long enough to be summarised.
var longReply = strings.Repeat("Claude changed the login handler so it rejects repeated failed attempts. ", 6) +
	"\n\n```go\nfunc login() {}\n```\n\nShould I also update the docs?"

// A long text is summarised by the small model, once: the same text again
// comes from the cache. Each kind is asked as its own kind.
func TestSpeechSummary(t *testing.T) {
	s, _, _ := boardServer(t)
	small := &failSmall{reply: "**Claude** fixed `web/static/app/chat.js`. Should it update the docs?"}
	s.Small = small

	code, out := speechCall(t, s, speechBody("reply", longReply))
	if code != http.StatusOK || out.Fallback != "" {
		t.Fatalf("reply: %d %+v", code, out)
	}
	// Markdown and the path the model left in are taken out.
	if want := "Claude fixed chat.js. Should it update the docs?"; out.Summary != want {
		t.Errorf("summary %q, want %q", out.Summary, want)
	}
	if _, again := speechCall(t, s, speechBody("reply", longReply)); again.Summary != out.Summary {
		t.Errorf("again: %+v", again)
	}
	if small.n() != 1 {
		t.Errorf("asked Claude %d times for the same reply, want once (cached)", small.n())
	}
	for _, kind := range []string{"approval", "status"} {
		if _, out := speechCall(t, s, speechBody(kind, longReply)); out.Summary == "" {
			t.Errorf("%s: %+v", kind, out)
		}
	}
	if got := strings.Join(small.calls, " "); got != "speak-reply speak-approval speak-status" {
		t.Errorf("calls %q", got)
	}
}

// Short text isn't sent to Claude: it is read as it is.
func TestSpeechSummaryShortText(t *testing.T) {
	s, _, _ := boardServer(t)
	small := &failSmall{reply: "x"}
	s.Small = small
	code, out := speechCall(t, s, speechBody("reply", "Done: the tests pass."))
	if code != http.StatusOK || out.Summary != "" || out.Fallback != fallbackShort {
		t.Errorf("short: %d %+v", code, out)
	}
	if small.n() != 0 {
		t.Error("asked Claude to summarise a short text")
	}
}

// A failed call, an empty answer, or no Claude here at all: no summary, a
// fallback saying so (still 200, so the caller just reads the text), and
// nothing cached, so the next time asks again.
func TestSpeechSummaryFallsBack(t *testing.T) {
	s, _, _ := boardServer(t)
	small := &failSmall{fail: errors.New("claude: rate limited")}
	s.Small = small
	code, out := speechCall(t, s, speechBody("status", longReply))
	if code != http.StatusOK || out.Summary != "" || out.Fallback != fallbackFailed || !strings.Contains(out.Error, "rate limited") {
		t.Errorf("failed call: %d %+v", code, out)
	}
	small.fail, small.reply = nil, "  ```\n```  "
	if _, out := speechCall(t, s, speechBody("status", longReply)); out.Fallback != fallbackFailed {
		t.Errorf("empty answer: %+v", out)
	}
	small.reply = "The task failed: two tests in the agent package fail."
	if _, out := speechCall(t, s, speechBody("status", longReply)); out.Summary != small.reply {
		t.Errorf("after the failures: %+v (a failure was cached?)", out)
	}
	if small.n() != 3 {
		t.Errorf("asked %d times, want 3", small.n())
	}

	s.Small = nil
	if code, out := speechCall(t, s, speechBody("approval", longReply)); code != http.StatusOK || out.Fallback != fallbackFailed {
		t.Errorf("no Claude here: %d %+v", code, out)
	}
}

func TestSpeechSummaryBadRequest(t *testing.T) {
	s, _, _ := boardServer(t)
	s.Small = &failSmall{reply: "x"}
	for _, body := range []string{speechBody("poem", longReply), speechBody("reply", "  "), `{`} {
		if code, _ := speechCall(t, s, body); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, code)
		}
	}
}

// On a hub, the summary is made on the caller's runner, never on the hub.
func TestHubSpeechSummaryOnTheUsersRunner(t *testing.T) {
	f := newClaudeFleet(t)
	r := httptest.NewRequest("POST", "/api/speech/summary", strings.NewReader(speechBody("reply", longReply)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.owner.ServeHTTP(w, r)
	var out spokenSummary
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusOK || out.Summary != "speak-reply from runner" {
		t.Errorf("owner: %d %s", w.Code, w.Body)
	}
	if f.hubSmall.n() != 0 {
		t.Error("the hub ran Claude")
	}
	// Bea has no runner: no summary, read the text.
	w = httptest.NewRecorder()
	f.other.ServeHTTP(w, httptest.NewRequest("POST", "/api/speech/summary", strings.NewReader(speechBody("reply", longReply))))
	out = spokenSummary{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusOK || out.Fallback != fallbackFailed {
		t.Errorf("member without a runner: %d %s", w.Code, w.Body)
	}
}
