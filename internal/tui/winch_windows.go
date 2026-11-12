package tui

import "os"

// notifyResize sends to ch when the terminal is resized. Windows consoles
// have no SIGWINCH: the size is sent once, when attaching.
func notifyResize(chan<- os.Signal) {}
