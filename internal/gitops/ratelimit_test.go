package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// What gh prints when GitHub rate limits it.
const (
	ghPrimary = "gh: API rate limit exceeded for user ID 1234567. If you reach out to GitHub Support for help, " +
		"please include the request ID 0400:3D1B:5C2A51:5E7F6B:6543A1B2 and timestamp 2026-11-23 13:04:12 UTC. (HTTP 403)"
	ghPrimaryGraphQL = "GraphQL: API rate limit already exceeded for user ID 1234567."
	ghNoLogin        = "gh: API rate limit exceeded for 203.0.113.7. (But here's the good news: Authenticated requests " +
		"get a higher rate limit. Check out the documentation for more details.) (HTTP 403)"
	ghNoLoginV6 = "gh: API rate limit exceeded for 2001:db8::7. (But here's the good news: Authenticated requests " +
		"get a higher rate limit. Check out the documentation for more details.) (HTTP 403)"
	ghSecondary = "gh: You have exceeded a secondary rate limit. Please wait a few minutes before you try again. " +
		"If you reach out to GitHub Support for help, please include the request ID 0401:21A7:9C3E2:A1F55:6543A1C0 " +
		"and timestamp 2026-11-23 13:04:13 UTC. (HTTP 403)"
	ghAbuse     = "gh: You have triggered an abuse detection mechanism. Please wait a few minutes before you try again. (HTTP 403)"
	ghTooQuick  = "pull request create failed: GraphQL: was submitted too quickly (createPullRequest)"
	ghTooMany   = "HTTP 429: Too Many Requests (https://api.github.com/repos/o/r/actions/runs?per_page=20)"
	ghForbidden = "HTTP 403: Resource not accessible by integration (https://api.github.com/repos/o/r/pulls)"
)

func TestDetectRateLimit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 23, 13, 4, 0, 0, time.UTC)
	for _, c := range []struct {
		name, out string
		kind      string // "" for none
		noLogin   bool
		until     time.Time
		message   string
	}{
		{"primary", "\n" + ghPrimary + "\n", RateLimitPrimary, false, now.Add(time.Hour), "API rate limit exceeded for user ID 1234567."},
		{"primary graphql", ghPrimaryGraphQL, RateLimitPrimary, false, now.Add(time.Hour), "API rate limit already exceeded for user ID 1234567."},
		{"no login", ghNoLogin, RateLimitPrimary, true, now.Add(time.Hour), "API rate limit exceeded for 203.0.113.7."},
		{"no login IPv6", ghNoLoginV6, RateLimitPrimary, true, now.Add(time.Hour), "API rate limit exceeded for 2001:db8::7."},
		{"secondary", ghSecondary, RateLimitSecondary, false, now.Add(time.Minute), "You have exceeded a secondary rate limit."},
		{"abuse", ghAbuse, RateLimitSecondary, false, now.Add(time.Minute), "You have triggered an abuse detection mechanism."},
		{"too quickly", ghTooQuick, RateLimitSecondary, false, now.Add(time.Minute), "was submitted too quickly"},
		{"429", ghTooMany, RateLimitSecondary, false, now.Add(time.Minute), "HTTP 429: Too Many Requests"},
		{"403 with wording", "HTTP 403: API rate limit exceeded (https://api.github.com/graphql)", RateLimitPrimary, false, now.Add(time.Hour), "API rate limit exceeded"},
		{"retry-after", ghSecondary + "\nRetry-After: 120", RateLimitSecondary, false, now.Add(2 * time.Minute), "secondary rate limit"},
		{"reset header", ghPrimary + "\nX-RateLimit-Reset: 1795957200", RateLimitPrimary, false, time.Unix(1795957200, 0), "user ID 1234567"},
		{"forbidden", ghForbidden, "", false, time.Time{}, ""},
		{"other failure", "X Pull request o/r#7 is not mergeable: the base branch policy prohibits the merge.", "", false, time.Time{}, ""},
	} {
		rl := DetectRateLimit(c.out, now)
		if c.kind == "" {
			if rl != nil {
				t.Errorf("%s: want no rate limit, got %+v", c.name, rl)
			}
			continue
		}
		if rl == nil {
			t.Errorf("%s: no rate limit found in %q", c.name, c.out)
			continue
		}
		if rl.Kind != c.kind || rl.NoLogin != c.noLogin || !rl.Until.Equal(c.until) || !strings.Contains(rl.Message, c.message) {
			t.Errorf("%s: got kind %s, no login %v, until %v, message %q", c.name, rl.Kind, rl.NoLogin, rl.Until, rl.Message)
		}
		if strings.HasPrefix(rl.Message, "gh: ") || strings.Contains(rl.Message, "\n") {
			t.Errorf("%s: message %q should be GitHub's, on one line", c.name, rl.Message)
		}
	}
}

// A runner's error answer carries the limit to its hub as text, which the
// hub reads back.
func TestRateLimitErrorRoundTrip(t *testing.T) {
	t.Parallel()
	until := time.Date(2026, 11, 23, 14, 4, 30, 0, time.UTC)
	for _, e := range []*RateLimitError{
		{Kind: RateLimitSecondary, Message: "You have exceeded a secondary rate limit.", Until: until},
		{Kind: RateLimitPrimary, Message: "API rate limit exceeded for 203.0.113.7.", Until: until, NoLogin: true},
	} {
		msg := "runner-1: " + e.Error()
		if !strings.Contains(msg, "paused until 2026-11-23 14:05 UTC") {
			t.Fatalf("want the end rounded up to the minute, got %q", msg)
		}
		got, ok := AsRateLimit(errors.New(msg))
		if !ok || got.Kind != e.Kind || got.Message != e.Message || got.NoLogin != e.NoLogin || !got.Until.Equal(until.Truncate(time.Minute).Add(time.Minute)) {
			t.Fatalf("read back %+v from %q", got, msg)
		}
		if again, _ := AsRateLimit(fmt.Errorf("wrapped: %w", e)); again != e {
			t.Fatal("AsRateLimit must find a wrapped *RateLimitError")
		}
	}
	if _, ok := AsRateLimit(errors.New("HTTP 403: rate limit wording from somewhere else")); ok {
		t.Fatal("only gh's output or a RateLimitError's text is a rate limit")
	}
}

// clock is a fake time for RateLimits.
type clock struct{ t time.Time }

func (c *clock) now() time.Time            { return c.t }
func (c *clock) add(d time.Duration)       { c.t = c.t.Add(d) }
func noJitter(time.Duration) time.Duration { return 0 }

// gh failing with a secondary limit pauses this computer's polls for a
// minute; limited again right after, the pause doubles; a call that goes
// through ends it and starts the backoff over. The pause is capped.
func TestRateLimitBackoff(t *testing.T) {
	t.Parallel()
	g, calls := mergeGh(t, `if [ -f "$(dirname "$0")/limited" ]; then echo "`+ghSecondary+`" >&2; exit 1; fi; echo '[]'`)
	limited := strings.TrimSuffix(calls, "calls") + "limited"
	c := &clock{t: time.Date(2026, 11, 23, 13, 0, 0, 0, time.UTC)}
	g.limits.Now, g.limits.Jitter = c.now, noJitter
	ctx := context.Background()

	if _, err := g.ghQuiet(ctx, "p", "", "run", "list"); err != nil || g.RateLimit() != nil {
		t.Fatalf("want a call through and no limit, got %v %v", err, g.RateLimit())
	}
	writeFile(t, limited, "")
	_, err := g.ghQuiet(ctx, "p", "", "run", "list")
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.Kind != RateLimitSecondary || !rl.Until.Equal(c.t.Add(time.Minute)) {
		t.Fatalf("want a secondary limit for a minute, got %v", err)
	}
	if !errors.As(err, new(*CmdError)) {
		t.Fatal("a rate limit must still be the gh command's *CmdError")
	}
	if got := g.RateLimit(); got == nil || !got.Until.Equal(rl.Until) {
		t.Fatalf("want the pause in force, got %+v", got)
	}
	c.add(time.Minute)
	if g.RateLimit() != nil {
		t.Fatal("the pause should end after a minute")
	}

	// Limited again right after: 2, 4, 8 minutes, then at most 15.
	for _, want := range []time.Duration{2, 4, 8, 15, 15} {
		_, err := g.ghQuiet(ctx, "p", "", "run", "list")
		if !errors.As(err, &rl) || !rl.Until.Equal(c.t.Add(want*time.Minute)) {
			t.Fatalf("want a pause of %dm, got until %v (now %v)", want, rl.Until, c.t)
		}
		c.add(want * time.Minute)
	}

	// Through again: the backoff starts over.
	removeFile(t, limited)
	if _, err := g.ghQuiet(ctx, "p", "", "run", "list"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, limited, "")
	if _, err := g.ghQuiet(ctx, "p", "", "run", "list"); !errors.As(err, &rl) || !rl.Until.Equal(c.t.Add(time.Minute)) {
		t.Fatalf("want a one-minute pause after a call went through, got %v", err)
	}
}

// Without a reset time in gh's output, the hourly limit's comes from
// `gh api rate_limit`; when that can't say, an hour.
func TestPrimaryLimitResetFromGitHub(t *testing.T) {
	t.Parallel()
	g, calls := mergeGh(t, `case "$*" in "api rate_limit"*) echo 1795957200 ;; *) echo "`+ghPrimary+`" >&2; exit 1 ;; esac`)
	c := &clock{t: time.Unix(1795957200, 0).Add(-20 * time.Minute)}
	g.limits.Now, g.limits.Jitter = c.now, noJitter
	_, err := g.ghQuiet(context.Background(), "p", "", "pr", "list")
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.Kind != RateLimitPrimary || !rl.Until.Equal(time.Unix(1795957200, 0)) {
		t.Fatalf("want the limit until GitHub's reset, got %v", err)
	}
	if got := readCalls(t, calls); !strings.Contains(got, "api rate_limit") {
		t.Fatalf("want rate_limit asked, gh calls %q", got)
	}

	g, _ = mergeGh(t, `echo "`+ghNoLogin+`" >&2; exit 1`)
	g.limits.Now, g.limits.Jitter = c.now, noJitter
	_, err = g.ghQuiet(context.Background(), "p", "", "pr", "list")
	if !errors.As(err, &rl) || !rl.NoLogin || !rl.Until.Equal(c.t.Add(time.Hour)) {
		t.Fatalf("want an hour's pause for a limit without a login, got %v", err)
	}
	if !strings.Contains(rl.Error(), "gh auth login") {
		t.Fatalf("the error should say how to fix it: %q", rl.Error())
	}
}

func TestIsWrite(t *testing.T) {
	t.Parallel()
	for args, want := range map[string]bool{
		"pr create --base main": true,
		"pr merge 7 --squash":   true,
		"pr comment 7 --body x": true,
		"run rerun 5 --failed":  true,
		"pr list --head x":      false,
		"pr view 7":             false,
		"run list":              false,
		"api -X POST repos/{owner}/{repo}/pulls/7/comments/1/replies":                       true,
		"api --method=PATCH repos/o/r":                                                      true,
		"api -X GET repos/o/r":                                                              false,
		"api --paginate repos/{owner}/{repo}/pulls/7/reviews?per_page=100":                  false,
		"api graphql -f query=mutation($id:ID!){resolveReviewThread(input:{threadId:$id})}": true,
		"api graphql --paginate -f query=query{viewer{login}}":                              false,
	} {
		if got := isWrite(strings.Fields(args)); got != want {
			t.Errorf("isWrite(%s) = %v, want %v", args, got, want)
		}
	}
}

// Writes from one computer run one after another, WriteGap apart.
func TestWritesSpacedOut(t *testing.T) {
	g, _ := mergeGh(t, `exit 0`)
	defer func(gap time.Duration) { WriteGap = gap }(WriteGap)
	WriteGap = 150 * time.Millisecond
	start := time.Now()
	for range 3 {
		if _, err := g.gh(context.Background(), "p", "", "pr", "comment", "7", "--body", "x"); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 2*WriteGap {
		t.Fatalf("three writes took %v, want at least %v", d, 2*WriteGap)
	}
	start = time.Now()
	for range 3 {
		_, _ = g.ghQuiet(context.Background(), "p", "", "pr", "view", "7")
	}
	if d := time.Since(start); d >= 2*WriteGap {
		t.Fatalf("reads waited for each other: %v", d)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func removeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
