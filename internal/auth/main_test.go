package auth

import (
	"os"
	"testing"

	"github.com/klahr/dash/internal/passhash"
	"github.com/klahr/dash/internal/store"
)

// TestMain hashes passwords with the cheapest Argon2id setting: at the
// production one, hashing takes most of these tests' time. The passhash
// package's own tests keep the production setting.
func TestMain(m *testing.M) {
	passhash.UseCheapParamsInTests()
	store.UseMigratedTemplateInTests()
	os.Exit(m.Run())
}
