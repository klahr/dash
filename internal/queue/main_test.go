package queue

import (
	"os"
	"testing"

	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/testgit"
)

// TestMain keeps the git the tests start off this computer's repositories
// and settings (see testgit).
func TestMain(m *testing.M) {
	store.UseMigratedTemplateInTests() // new databases copy one migrated once
	os.Exit(testgit.Run(m))
}
