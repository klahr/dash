package store

import (
	"os"
	"testing"

	"github.com/klahr/dash/internal/passhash"
)

// TestMain hashes passwords with the cheapest Argon2id setting: at the
// production one, hashing takes much of these tests' time. The passhash
// package's own tests keep the production setting.
func TestMain(m *testing.M) {
	passhash.UseCheapParamsInTests()
	os.Exit(m.Run())
}
