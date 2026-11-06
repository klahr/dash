package queue

import (
	"os"
	"testing"
)

// TestMain keeps the developer's git config from breaking test commits:
// with commit.gpgsign on, git would ask a pinentry no test can answer.
// GIT_CONFIG_* reaches every git the tests start, also via sh -c.
func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_COUNT", "1")
	os.Setenv("GIT_CONFIG_KEY_0", "commit.gpgsign")
	os.Setenv("GIT_CONFIG_VALUE_0", "false")
	os.Exit(m.Run())
}
