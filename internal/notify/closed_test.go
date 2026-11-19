package notify

import (
	"testing"

	"github.com/klahr/dash/internal/model"
)

// A task closed without changes is announced as closed, not merged; the
// event stays task.merged for webhooks that listen for it.
func TestClosedTaskHead(t *testing.T) {
	if h := (Notification{Type: model.NotifyMerged, Message: ClosedNoChanges}).Head(); h != "Closed" {
		t.Fatalf("closed: head %q", h)
	}
	if h := (Notification{Type: model.NotifyMerged}).Head(); h != "Merged" {
		t.Fatalf("merged: head %q", h)
	}
}
