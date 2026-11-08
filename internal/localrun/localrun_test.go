package localrun

import (
	"errors"
	"testing"
)

func TestCheck(t *testing.T) {
	t.Cleanup(func() { Set(nil) })
	if err := Check("git status"); err != nil || !Allowed() {
		t.Fatalf("by default commands run: %v", err)
	}
	Set(func() bool { return false })
	if err := Check("git status"); !errors.Is(err, ErrHub) || Allowed() {
		t.Fatalf("on the hub: %v", err)
	}
	Set(nil)
	if !Allowed() {
		t.Fatal("Set(nil) allows again")
	}
}
