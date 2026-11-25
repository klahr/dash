package dispatch

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/tunnel"
)

func (fx *fixture) linksTo(id string) int {
	n := 0
	for _, c := range fx.fleet.callsTo(id) {
		if strings.Contains(c, " PUT /api/hub/projects/") {
			n++
		}
	}
	return n
}

// The hub links a project to a runner once, not before every task or
// forwarded request: again only when the link's settings change, the
// runner reconnects, or the project is unlinked.
func TestLinkSentOnlyWhenItChanges(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	p := fx.project(t, fx.orgA, "laptop")
	ctx := context.Background()

	for i := range 3 {
		out, err := fx.d.Link(ctx, "laptop", p)
		if err != nil || out.ProjectID != "on-laptop" {
			t.Fatalf("link %d = %+v %v", i, out, err)
		}
	}
	if _, _, err := fx.d.Dispatch(ctx, p, fx.todo(t, p, nil)); err != nil {
		t.Fatal(err)
	}
	if n := fx.linksTo("laptop"); n != 1 {
		t.Fatalf("%d links sent, want 1", n)
	}

	p.TestCommand = "go test ./..."
	if _, err := fx.d.Link(ctx, "laptop", p); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.d.Link(ctx, "laptop", p); err != nil {
		t.Fatal(err)
	}
	if n := fx.linksTo("laptop"); n != 2 {
		t.Fatalf("%d links sent after a settings change, want 2", n)
	}

	fx.d.State("laptop", true) // reconnected: it may have lost the project
	if _, err := fx.d.Link(ctx, "laptop", p); err != nil {
		t.Fatal(err)
	}
	if n := fx.linksTo("laptop"); n != 3 {
		t.Fatalf("%d links sent after a reconnect, want 3", n)
	}

	fx.d.Relink("laptop", p.ID) // it said it doesn't have the project
	if _, err := fx.d.Link(ctx, "laptop", p); err != nil {
		t.Fatal(err)
	}
	if n := fx.linksTo("laptop"); n != 4 {
		t.Fatalf("%d links sent after Relink, want 4", n)
	}
}

// Two links of a project to a runner at once (requests that link it while
// it clones, one from before a settings change) go one after the other: the
// one the runner got last is the one the hub remembers, so the new settings
// reach it, however the answers come back.
func TestLinksAtOnceKeepTheNewSettings(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	old := fx.project(t, fx.orgA, "laptop")
	cur := old
	cur.PreviewKind, cur.PreviewCommand = "terminal", "echo hi; exec sh"
	ctx := context.Background()

	// The new link reaches the runner first and its answer is slow; the old
	// one is sent meanwhile.
	sent, release := make(chan struct{}), make(chan struct{})
	fx.fleet.linked = func(l Link) {
		if l.PreviewCommand != "" {
			close(sent)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := fx.d.Link(ctx, "laptop", cur)
		done <- err
	}()
	<-sent
	oldDone := make(chan error, 1)
	go func() {
		_, err := fx.d.Link(ctx, "laptop", old)
		oldDone <- err
	}()
	select {
	case <-oldDone:
		t.Fatal("the old link was sent while the new one was")
	case <-time.After(50 * time.Millisecond):
	}
	fx.fleet.mu.Lock()
	fx.fleet.linked = nil
	fx.fleet.mu.Unlock()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}

	// The runner got the old settings last: the next link sends the new ones.
	if _, err := fx.d.Link(ctx, "laptop", cur); err != nil {
		t.Fatal(err)
	}
	fx.fleet.mu.Lock()
	defer fx.fleet.mu.Unlock()
	if last := fx.fleet.links[len(fx.fleet.links)-1]; last.PreviewCommand != cur.PreviewCommand {
		t.Fatalf("the runner was left with preview command %q, want %q", last.PreviewCommand, cur.PreviewCommand)
	}
}

// A runner that refuses a task because it no longer has the project gets
// the project's link again on the next try.
func TestTaskRefusedAsNotLinkedRelinks(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	p := fx.project(t, fx.orgA, "laptop")
	td := fx.todo(t, p, nil)
	fx.fleet.answer = func(string, Task, *Accepted) error {
		return &tunnel.CallError{Status: http.StatusForbidden, Msg: NotLinked}
	}
	if _, _, err := fx.d.Dispatch(context.Background(), p, td); err == nil {
		t.Fatal("dispatch took a refused task")
	}
	fx.fleet.mu.Lock()
	fx.fleet.answer = nil
	fx.fleet.mu.Unlock()
	if _, _, err := fx.d.Dispatch(context.Background(), p, td); err != nil {
		t.Fatal(err)
	}
	if n := fx.linksTo("laptop"); n != 2 {
		t.Fatalf("%d links sent, want 2", n)
	}
}

// Tasks claimed one after the other count toward their runner's load
// before they are sent, so sending them side by side doesn't overfill it.
func TestClaimCountsTowardTheRunnersLoad(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	fx.fleet.set(runnerOf("laptop", fx.orgA, true)) // takes 2 at once
	p := fx.project(t, fx.orgA, "laptop")
	var sends []func(context.Context) (string, string, error)
	for i := range 2 {
		rid, send, err := fx.d.Claim(p, fx.todo(t, p, nil))
		if err != nil || rid != "laptop" {
			t.Fatalf("claim %d = %q %v", i, rid, err)
		}
		sends = append(sends, send)
	}
	if _, _, err := fx.d.Claim(p, fx.todo(t, p, nil)); err == nil || !strings.Contains(err.Error(), "busy (2 of 2") {
		t.Fatalf("third claim = %v, want laptop busy", err)
	}

	// A send that fails frees its claim.
	fx.fleet.set(runnerOf("laptop", fx.orgA, false))
	if _, _, err := sends[0](context.Background()); err == nil {
		t.Fatal("sent to an offline runner")
	}
	fx.fleet.set(runnerOf("laptop", fx.orgA, true))
	if _, _, err := fx.d.Claim(p, fx.todo(t, p, nil)); err != nil {
		t.Fatalf("claim after a failed send = %v", err)
	}
}
