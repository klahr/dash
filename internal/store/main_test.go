package store

import (
	"os"
	"testing"

	"github.com/klahr/dash/internal/passhash"
)

// TestMain hashes passwords with the cheapest Argon2id setting: at the
// production one, hashing takes much of these tests' time. The passhash
// package's own tests keep the production setting. New databases are copied
// from a template migrated once (UseMigratedTemplateInTests); the migration
// tests migrate for real, with openPath or on older schemas.
func TestMain(m *testing.M) {
	passhash.UseCheapParamsInTests()
	UseMigratedTemplateInTests()
	os.Exit(m.Run())
}
