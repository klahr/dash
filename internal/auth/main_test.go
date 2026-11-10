package auth

import (
	"os"
	"testing"
	"time"

	"github.com/klahr/dash/internal/passhash"
)

// TestMain makes password hashing cheap and failed sign-ins quick: the real
// Argon2id parameters and the pause after each failure were most of this
// package's run time.
func TestMain(m *testing.M) {
	passhash.UseWeakParamsForTests()
	failurePause = time.Millisecond
	os.Exit(m.Run())
}
