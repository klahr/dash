package queue

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A long job in one project (say, its tests running after a merge) doesn't
// hold up the merge worker of another, while one project's jobs still run
// one at a time, in order.
func TestEachProjectHasItsOwnMergeWorker(t *testing.T) {
	mq := NewMergeQueue(nil, nil, nopPub{}, nil, 4)
	slow := make(chan struct{})
	started := make(chan struct{})
	// Queued before Run, as Resume does at a start.
	go func() {
		_ = mq.Do(context.Background(), "a", func(context.Context) { close(started); <-slow })
	}()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); mq.Run(ctx) }()
	defer func() { cancel(); wg.Wait() }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("project a's job never started")
	}

	// Project b's job runs while a's is still in hand.
	dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dcancel()
	ran := false
	if err := mq.Do(dctx, "b", func(context.Context) { ran = true }); err != nil || !ran {
		t.Fatalf("project b waited for project a: ran %v, err %v", ran, err)
	}

	// Project a's next job waits for the one in hand.
	second := make(chan struct{})
	go func() { _ = mq.Do(context.Background(), "a", func(context.Context) { close(second) }) }()
	select {
	case <-second:
		t.Fatal("project a ran two jobs at once")
	case <-time.After(200 * time.Millisecond):
	}
	if got := mq.Pending(); got != 2 {
		t.Errorf("pending = %d, want 2 (both of a's jobs)", got)
	}
	close(slow)
	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("project a's second job never ran")
	}
	cancel()
	if !mq.Wait(5 * time.Second) {
		t.Fatal("the workers didn't stop")
	}
}

// Each project's queue is full on its own; another project can still queue.
func TestMergeQueueFullPerProject(t *testing.T) {
	mq := NewMergeQueue(nil, nil, nopPub{}, nil, 1)
	if err := mq.push("a", mergeItem{fn: func(context.Context) {}}); err != nil {
		t.Fatal(err)
	}
	if err := mq.push("a", mergeItem{fn: func(context.Context) {}}); err != ErrMergeQueueFull {
		t.Fatalf("second job in a: err %v, want ErrMergeQueueFull", err)
	}
	if err := mq.push("b", mergeItem{fn: func(context.Context) {}}); err != nil {
		t.Fatalf("project b: %v", err)
	}
	if got := mq.Pending(); got != 2 {
		t.Errorf("pending = %d, want 2", got)
	}
}
