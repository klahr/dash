package tracker_test

import (
	"os"
	"testing"

	"github.com/klahr/dash/internal/store"
)

// TestMain copies new test databases from one migrated once, instead of
// running every migration for each (store.UseMigratedTemplateInTests).
func TestMain(m *testing.M) {
	store.UseMigratedTemplateInTests()
	os.Exit(m.Run())
}
