package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/agent"
)

// Spoken summaries (docs/dictation.md#spoken-replies): what the browser
// reads aloud instead of a long reply, approval request or task's news, a
// sentence or two from the small model (-title-model). The browser's spoken
// replies (web/static/app/speech.js) ask for them, and so may anything else
// that speaks.
//
// It never fails for want of a summary: text that is already short, or a
// call to Claude that fails, answers {"summary": "", "fallback": …}, and
// the caller reads the text as it would have without one.

// minSpokenWords is how long text must be, in words, to be summarised:
// shorter text is read as it is.
const minSpokenWords = 40

// spokenTimeout stops a summary that takes too long: by then the caller
// has read the text without it.
const spokenTimeout = 20 * time.Second

// Why speechSummary gave no summary.
const (
	fallbackShort  = "short"  // the text is short enough to read as it is
	fallbackFailed = "failed" // Claude couldn't be asked, or failed
)

type spokenSummary struct {
	Summary string `json:"summary"`
	// Fallback is why there is no summary ("" when there is one): read
	// the text instead.
	Fallback string `json:"fallback,omitempty"`
	Error    string `json:"error,omitempty"`
}

// speechSummary handles POST /api/speech/summary: {"kind", "text"} in,
// kind one of agent.SpeakKinds, a spokenSummary out.
func (s *Server) speechSummary(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if !slices.Contains(agent.SpeakKinds, req.Kind) {
		writeErr(w, fmt.Errorf("%w: kind must be one of %s", errBadRequest, strings.Join(agent.SpeakKinds, ", ")))
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeErr(w, fmt.Errorf("%w: text is required", errBadRequest))
		return
	}
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, sv.spokenSummary(r.Context(), req.Kind, req.Text))
}

// spokenSummary summarises text of kind to be read aloud, here or on the
// caller's runner, or says why it doesn't. A summary made before for the
// same caller, kind and text is used again.
func (sv *service) spokenSummary(ctx context.Context, kind, text string) spokenSummary {
	if len(strings.Fields(text)) < minSpokenWords {
		return spokenSummary{Fallback: fallbackShort}
	}
	key := spokenKey(sv.c, kind, text)
	if out, ok := sv.s.spoken.get(key); ok {
		return spokenSummary{Summary: out}
	}
	ctx, cancel := context.WithTimeout(ctx, spokenTimeout)
	defer cancel()
	var out string
	var err error
	if !sv.s.hub() {
		if sv.s.Small == nil {
			err = errNoSpeaker
		} else {
			out, err = sv.s.Small.Speak(ctx, kind, text)
		}
	} else {
		out, err = sv.smallCall(ctx, "speak-"+kind, text)
	}
	if err == nil {
		// A runner's answer is cleaned too: it may be an older one's.
		out, err = agent.CleanSpoken(out)
	}
	if err != nil {
		slog.Debug("spoken summary", "kind", kind, "err", err)
		return spokenSummary{Fallback: fallbackFailed, Error: err.Error()}
	}
	sv.s.spoken.put(key, out)
	return spokenSummary{Summary: out}
}

var errNoSpeaker = errors.New("spoken summaries are not available here")

// spokenKey is the cache key of a summary: who asked, the kind and the text.
func spokenKey(c Caller, kind, text string) string {
	h := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%d:%d:%s:%s", c.Org, c.User, kind, hex.EncodeToString(h[:]))
}

// maxSpokenCache is how many summaries spokenCache keeps.
const maxSpokenCache = 512

// spokenCache keeps the latest summaries, so the same message (heard in
// two tabs, or by the voice mode as well) isn't summarised twice. Its zero
// value is ready to use.
type spokenCache struct {
	mu    sync.Mutex
	m     map[string]string
	order []string // keys, oldest first
}

func (c *spokenCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	return v, ok
}

func (c *spokenCache) put(key, v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]string{}
	}
	if _, ok := c.m[key]; !ok {
		c.order = append(c.order, key)
	}
	c.m[key] = v
	for len(c.order) > maxSpokenCache {
		delete(c.m, c.order[0])
		c.order = c.order[1:]
	}
}
