package localrun

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// binDirs are where agent CLIs (Claude Code, Codex, Gemini CLI, …) are
// often installed, relative to the home folder ("~/") or absolute: npm's
// global folder, Claude Code's native installer, Homebrew, and so on. A
// shell's PATH has them; a service's, or a desktop app's, often doesn't.
var binDirs = []string{
	"~/.local/bin", "~/.claude/local", "~/.npm-global/bin", "~/.npm/bin",
	"~/.bun/bin", "~/.volta/bin", "~/.yarn/bin", "~/bin",
	"/opt/homebrew/bin", "/usr/local/bin",
}

// windowsBinDirs are binDirs on Windows: npm's global folder.
var windowsBinDirs = []string{"~/AppData/Roaming/npm", "~/.local/bin", "~/.bun/bin"}

// AgentPath is path (a PATH value) with the usual install folders of
// agent CLIs that exist under home added at its end, so a CLI installed
// there is found without a flag naming its binary. Folders path has
// already keep their place.
func AgentPath(path, home string) string {
	dirs := binDirs
	if runtime.GOOS == "windows" {
		dirs = windowsBinDirs
	}
	sep := string(os.PathListSeparator)
	parts := slices.DeleteFunc(strings.Split(path, sep), func(s string) bool { return s == "" })
	for _, d := range dirs {
		if rest, ok := strings.CutPrefix(d, "~/"); ok {
			if home == "" {
				continue
			}
			d = filepath.Join(home, filepath.FromSlash(rest))
		}
		if st, err := os.Stat(d); err != nil || !st.IsDir() || slices.Contains(parts, d) {
			continue
		}
		parts = append(parts, d)
	}
	return strings.Join(parts, sep)
}
