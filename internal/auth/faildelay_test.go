package auth

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/klahr/dash/internal/store"
)

// A server made by New waits failedAttemptDelay (300 ms) after a wrong
// password, and a right one answers without it. Other tests turn the wait
// off (NoFailDelayInTests).
func TestFailedLoginWaits(t *testing.T) {
	t.Parallel()
	if failedAttemptDelay != 300*time.Millisecond {
		t.Fatalf("failed attempts wait %v, want 300ms", failedAttemptDelay)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Accounts().Bootstrap("admin", "s3cret"); err != nil {
		t.Fatal(err)
	}
	a := New(st.Accounts()) // the production configuration
	if a.failDelay != failedAttemptDelay {
		t.Fatalf("New waits %v after a failed attempt, want %v", a.failDelay, failedAttemptDelay)
	}
	h := serve(a)

	start := time.Now()
	if w := do(h, "POST", "/api/login", `{"username":"admin","password":"nope"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d %s", w.Code, w.Body)
	}
	if took := time.Since(start); took < failedAttemptDelay {
		t.Errorf("a wrong password answered after %v, before the %v delay", took, failedAttemptDelay)
	}
	start = time.Now()
	if w := do(h, "POST", "/api/login", `{"username":"nobody","password":"nope"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown user: %d %s", w.Code, w.Body)
	}
	if took := time.Since(start); took < failedAttemptDelay {
		t.Errorf("an unknown user answered after %v, before the %v delay", took, failedAttemptDelay)
	}
	if w := do(h, "POST", "/api/login", `{"username":"admin","password":"s3cret"}`); w.Code != http.StatusOK {
		t.Fatalf("right password: %d %s", w.Code, w.Body)
	}
}
