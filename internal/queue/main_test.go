package queue

import (
	"os"
	"testing"

	"github.com/klahr/dash/internal/testgit"
)

// TestMain keeps the git the tests start off this computer's repositories
// and settings (see testgit).
func TestMain(m *testing.M) { os.Exit(testgit.Run(m)) }
