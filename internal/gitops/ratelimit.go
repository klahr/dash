package gitops

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klahr/dash/internal/textutil"
)

// Rate limit kinds (RateLimitError.Kind).
const (
	// RateLimitPrimary is GitHub's hourly limit (5000 calls an hour for a
	// user, 60 for an IP address without a login).
	RateLimitPrimary = "primary"
	// RateLimitSecondary is GitHub's limit on bursts: too many calls at
	// once, or too many writes (pull requests, comments) close together.
	RateLimitSecondary = "secondary"
)

// RateLimitError is gh failing because GitHub rate limited it. Dash pauses
// its GitHub polls until Until (see RateLimits); gh calls a person asks for
// still try. Err is the gh command's *CmdError.
type RateLimitError struct {
	Kind    string    `json:"kind"`
	Message string    `json:"message"` // GitHub's, one line
	Until   time.Time `json:"until"`   // the end of the pause
	// NoLogin: the limit was for an IP address, not a user, so gh made the
	// call without a login. `gh auth login` on that computer fixes it.
	NoLogin bool `json:"no_login,omitempty"`
	// Machine is the runner whose gh was limited, on a hub ("" for this
	// computer).
	Machine string `json:"machine,omitempty"`
	Err     error  `json:"-"`

	resetKnown bool // Until is GitHub's, not a guess
}

// rateLimitTimeLayout is how Error writes Until, so that a hub can read it
// back from a runner's answer (ParseRateLimitError).
const rateLimitTimeLayout = "2006-01-02 15:04 MST"

// noLoginNote ends the message of a limit for an IP address.
const noLoginNote = "; gh isn't logged in, `gh auth login` fixes it"

// Error is one line: "GitHub rate limit (secondary), paused until
// 2026-11-23 14:05 UTC: <GitHub's message>".
func (e *RateLimitError) Error() string {
	until := e.Until.UTC()
	if until.Truncate(time.Minute) != until {
		until = until.Truncate(time.Minute).Add(time.Minute) // never earlier than the real end
	}
	s := "GitHub rate limit (" + e.Kind + "), paused until " + until.Format(rateLimitTimeLayout) + ": " + e.Message
	if e.NoLogin {
		s += noLoginNote
	}
	return s
}

func (e *RateLimitError) Unwrap() error { return e.Err }

// AsRateLimit finds a rate limit in err: a *RateLimitError, or one a runner
// sent the hub as text (see ParseRateLimitError).
func AsRateLimit(err error) (*RateLimitError, bool) {
	if err == nil {
		return nil, false
	}
	var rl *RateLimitError
	if errors.As(err, &rl) {
		return rl, true
	}
	if rl := ParseRateLimitError(err.Error()); rl != nil {
		return rl, true
	}
	return nil, false
}

var rateLimitErrorRe = regexp.MustCompile(`GitHub rate limit \((primary|secondary)\), paused until (\d{4}-\d\d-\d\d \d\d:\d\d UTC): (.*)`)

// ParseRateLimitError reads back what RateLimitError.Error wrote, from
// anywhere in s (a runner's error answer is prefixed with its name). It
// returns nil when s has none.
func ParseRateLimitError(s string) *RateLimitError {
	m := rateLimitErrorRe.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	until, err := time.Parse(rateLimitTimeLayout, m[2])
	if err != nil {
		return nil
	}
	msg, noLogin := strings.CutSuffix(strings.TrimSpace(m[3]), noLoginNote)
	return &RateLimitError{Kind: m[1], Message: msg, Until: until, NoLogin: noLogin, resetKnown: true}
}

var (
	// "API rate limit exceeded for user ID 1234." or, without a login,
	// "API rate limit exceeded for 203.0.113.7." (GraphQL says "already
	// exceeded").
	primaryLimitRe = regexp.MustCompile(`(?i)API rate limit (?:already )?exceeded for (user ID|installation ID|[0-9a-f:.]*[0-9a-f])`)
	secondaryRe    = regexp.MustCompile(`(?i)secondary rate limit|abuse detection|submitted too quickly`)
	limitWordsRe   = regexp.MustCompile(`(?i)rate limit|too many requests`)
	httpLimitRe    = regexp.MustCompile(`HTTP 429|HTTP 403`)
	retryAfterRe   = regexp.MustCompile(`(?i)retry-after:\s*(\d+)`)
	resetRe        = regexp.MustCompile(`(?i)x-ratelimit-reset:\s*(\d{9,})`)
)

// Default pauses when GitHub doesn't say how long a limit lasts: a minute
// for a secondary limit (GitHub asks to wait "a few minutes"), an hour for
// the hourly one.
const (
	secondaryPause = time.Minute
	primaryPause   = time.Hour
)

// DetectRateLimit reads a failed gh command's output (or what
// RateLimitError.Error wrote) for a GitHub rate limit, at time now. It
// returns nil when the failure is something else.
func DetectRateLimit(out string, now time.Time) *RateLimitError {
	if rl := ParseRateLimitError(out); rl != nil {
		return rl
	}
	e := &RateLimitError{}
	var at []int
	switch {
	case secondaryRe.MatchString(out):
		e.Kind, at = RateLimitSecondary, secondaryRe.FindStringIndex(out)
	case primaryLimitRe.MatchString(out):
		m := primaryLimitRe.FindStringSubmatchIndex(out)
		e.Kind, at = RateLimitPrimary, m[:2]
		who := strings.ToLower(out[m[2]:m[3]])
		e.NoLogin = who != "user id" && who != "installation id"
	case httpLimitRe.MatchString(out) && (strings.Contains(out, "HTTP 429") || limitWordsRe.MatchString(out)):
		// 429, or a 403 that says rate limit: GitHub's limits on bursts.
		e.Kind, at = RateLimitSecondary, httpLimitRe.FindStringIndex(out)
		if strings.Contains(strings.ToLower(out), "api rate limit") {
			e.Kind = RateLimitPrimary
		}
	default:
		return nil
	}
	e.Message = limitLine(out, at[0])
	switch {
	case retryAfterRe.MatchString(out):
		s, _ := strconv.Atoi(retryAfterRe.FindStringSubmatch(out)[1])
		e.Until, e.resetKnown = now.Add(time.Duration(s)*time.Second), true
	case resetRe.MatchString(out):
		u, _ := strconv.ParseInt(resetRe.FindStringSubmatch(out)[1], 10, 64)
		e.Until, e.resetKnown = time.Unix(u, 0), true
	case e.Kind == RateLimitSecondary:
		e.Until = now.Add(secondaryPause)
	default:
		e.Until = now.Add(primaryPause)
	}
	return e
}

// limitLine is the line of out around byte i, without gh's prefixes: GitHub's
// message.
func limitLine(out string, i int) string {
	start := strings.LastIndexByte(out[:i], '\n') + 1
	end := len(out)
	if j := strings.IndexByte(out[i:], '\n'); j >= 0 {
		end = i + j
	}
	l := strings.TrimSpace(out[start:end])
	for _, p := range []string{"gh: ", "GraphQL: ", "X ", "error: "} {
		l = strings.TrimPrefix(l, p)
	}
	return textutil.Clip(l, 300)
}

// maxBackoff caps the pause after GitHub limits Dash again and again.
const maxBackoff = 15 * time.Minute

// RateLimits keeps the GitHub rate limits in force, by machine ("" is this
// computer: one gh login). Its zero value is ready to use.
type RateLimits struct {
	// Now and Jitter (a random duration below d) are for tests.
	Now    func() time.Time
	Jitter func(d time.Duration) time.Duration

	mu sync.Mutex
	m  map[string]*limitState
}

type limitState struct {
	err     RateLimitError // the newest; Until is the end of the pause
	since   time.Time      // when the pause began
	strikes int            // pauses in a row with no call through between them
}

func (r *RateLimits) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *RateLimits) jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if r.Jitter != nil {
		return r.Jitter(d)
	}
	return rand.N(d)
}

// Hit records that machine was rate limited with e, and returns the limit
// with Until moved to the end of the pause: GitHub's time, or longer when
// GitHub limits again right after a pause (1, 2, 4, 8 minutes… up to
// maxBackoff, with up to a fifth more at random). Each pause is logged
// once.
func (r *RateLimits) Hit(machine string, e *RateLimitError) *RateLimitError {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]*limitState{}
	}
	s := r.m[machine]
	if s == nil {
		s = &limitState{}
		r.m[machine] = s
	}
	active := now.Before(s.err.Until)
	if !active {
		s.strikes++
		s.since = now
	}
	backoff := min(time.Minute<<min(s.strikes-1, 10), maxBackoff)
	until := now.Add(backoff + r.jitter(backoff/5))
	if s.strikes == 1 && e.resetKnown {
		until = e.Until // GitHub said when; the first time, believe it
	}
	if e.Until.After(until) {
		until = e.Until
	}
	if active && s.err.Until.After(until) {
		until = s.err.Until
	}
	out := *e
	out.Until = until
	s.err = out
	if !active {
		args := []any{"kind", out.Kind, "until", until.Format(time.DateTime), "message", out.Message}
		if machine != "" {
			args = append(args, "runner", machine)
		}
		if out.NoLogin {
			args = append(args, "fix", "gh isn't logged in: run `gh auth login`")
		}
		slog.Warn("GitHub rate limit: pausing GitHub polls", args...)
	}
	return &out
}

// OK records a gh call on machine that went through, started at started:
// a pause that began before it ends, and backing off starts over.
func (r *RateLimits) OK(machine string, started time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.m[machine]; s != nil && started.After(s.since) {
		delete(r.m, machine)
	}
}

// Active is the rate limit machine is paused for, or nil.
func (r *RateLimits) Active(machine string) *RateLimitError {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.m[machine]; s != nil && now.Before(s.err.Until) {
		e := s.err
		return &e
	}
	return nil
}

// RateLimit is the GitHub rate limit this computer's gh is paused for, or
// nil. GitHub polls skip their calls while it lasts.
func (g *Git) RateLimit() *RateLimitError { return g.limits.Active("") }

// RateLimits is this computer's rate limit state (for tests: its clock).
func (g *Git) RateLimits() *RateLimits { return &g.limits }

// noteRateLimit records how a gh call started at started ended:
// one rate limited comes back as a *RateLimitError wrapping err.
func (g *Git) noteRateLimit(ctx context.Context, started time.Time, err error) error {
	if err == nil {
		g.limits.OK("", started)
		return nil
	}
	var ce *CmdError
	if !errors.As(err, &ce) {
		return err
	}
	rl := DetectRateLimit(ce.Output, g.limits.now())
	if rl == nil {
		return err
	}
	if rl.Kind == RateLimitPrimary && !rl.resetKnown {
		if reset := g.primaryReset(ctx); !reset.IsZero() {
			rl.Until, rl.resetKnown = reset, true
		}
	}
	rl.Err = err
	return g.limits.Hit("", rl)
}

// primaryReset asks GitHub when the hourly limit used up resets (asking
// doesn't count against it): the latest reset of the core and GraphQL
// limits with nothing left, or zero when gh can't say.
func (g *Git) primaryReset(ctx context.Context) time.Time {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := g.run(ctx, false, "", "", g.ghBin(), "api", "rate_limit", "--jq",
		"[.resources.core, .resources.graphql] | map(select(.remaining == 0) | .reset) | max // empty")
	if err != nil {
		return time.Time{}
	}
	u, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil || u <= 0 {
		return time.Time{}
	}
	return time.Unix(u, 0)
}

// WriteGap is the least time between two gh calls that write to GitHub
// (open, comment on or merge a pull request…) from this computer: GitHub's
// secondary limit counts writes close together. Tests set it to 0.
var WriteGap = time.Second

// spaceWrites waits for this computer's previous write to GitHub to end and
// WriteGap to pass, and returns the func to call once this one ended.
func (g *Git) spaceWrites(ctx context.Context) (done func()) {
	g.writeMu.Lock()
	if wait := time.Until(g.lastWrite.Add(WriteGap)); wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
		case <-t.C:
		}
		t.Stop()
	}
	return func() {
		g.lastWrite = time.Now()
		g.writeMu.Unlock()
	}
}

// ghWrites are the gh commands ("cmd sub") that write to GitHub.
var ghWrites = map[string]bool{
	"pr create": true, "pr comment": true, "pr merge": true, "pr ready": true, "pr close": true,
	"pr review": true, "pr edit": true, "pr reopen": true,
	"issue create": true, "issue comment": true, "issue close": true, "issue edit": true,
	"run rerun": true, "release create": true, "release edit": true, "release delete": true,
	"repo create": true, "repo delete": true,
}

// isWrite reports whether gh args write to GitHub: a write command, a REST
// call with a method other than GET, or a GraphQL mutation.
func isWrite(args []string) bool {
	if len(args) >= 2 && ghWrites[args[0]+" "+args[1]] {
		return true
	}
	if len(args) == 0 || args[0] != "api" {
		return false
	}
	for i, a := range args {
		if (a == "-X" || a == "--method") && i+1 < len(args) {
			return !strings.EqualFold(args[i+1], "GET")
		}
		if v, ok := strings.CutPrefix(a, "--method="); ok {
			return !strings.EqualFold(v, "GET")
		}
		if strings.HasPrefix(a, "query=") && strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(a, "query=")), "mutation") {
			return true
		}
	}
	return false
}
