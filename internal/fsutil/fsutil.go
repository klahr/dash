// Package fsutil holds small file system helpers shared across packages.
package fsutil

import (
	"os"
	"path/filepath"
	"strings"
)

// WriteFileAtomic writes b to path so readers see either the old content
// or the new, never a partial file: it writes path+".tmp" and renames it
// over path. The parent directory is created when missing, with execute
// bits added wherever perm grants read (0600 gives 0700, 0644 gives 0755).
// The temp file is removed when writing or renaming fails.
func WriteFileAtomic(path string, b []byte, perm os.FileMode) error {
	dirPerm := perm | (perm&0o444)>>2
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Within reports whether path is root or inside it, comparing the cleaned
// paths lexically (it doesn't resolve symlinks: do that first when it
// matters). An empty root contains nothing.
func Within(root, path string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// RelInside reports whether rel, a path relative to some root, stays at or
// under that root once cleaned: it isn't absolute or rooted, and isn't ".."
// or below it. Names that only start with dots ("..env") are inside.
func RelInside(rel string) bool {
	rel = filepath.Clean(rel)
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || (rel != "" && os.IsPathSeparator(rel[0])) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
