package store

import (
	"os"
	"testing"

	"github.com/klahr/dash/internal/passhash"
)

// TestMain makes password hashing cheap: the real Argon2id parameters were
// most of this package's run time.
func TestMain(m *testing.M) {
	passhash.UseWeakParamsForTests()
	os.Exit(m.Run())
}
