// Package fsutil holds small file system helpers shared across packages.
package fsutil

import (
	"os"
	"path/filepath"
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
