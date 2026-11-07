package runnerctl

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// State is what a running `dash runner` writes about its connection to the
// hub, for `dash runner status`.
type State struct {
	PID       int       `json:"pid"`
	Version   string    `json:"version"`
	Connected bool      `json:"connected"`
	Since     time.Time `json:"since"` // when Connected last changed
}

// StatePath is the state file in a data directory.
func StatePath(dataDir string) string { return filepath.Join(dataDir, "runner-state.json") }

// WriteState saves st at path (best effort: status is a convenience).
func WriteState(path string, st State) error {
	b, _ := json.Marshal(st)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadState reads the state file; ok is false when there is none or the
// runner that wrote it is no longer running.
func ReadState(path string) (st State, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &st) != nil || st.PID <= 0 {
		return st, false
	}
	return st, alive(st.PID)
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
