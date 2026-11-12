package localrun

import "os"

// Git for Windows' login shell (bash -l, as project commands run) changes
// to $HOME unless CHERE_INVOKING is set: set, it stays in the directory it
// was started in. Every bash Dash and Claude start inherits it.
func init() {
	if os.Getenv("CHERE_INVOKING") == "" {
		_ = os.Setenv("CHERE_INVOKING", "1")
	}
}
