// Package disk measures a runner's free disk space, decides when it is low,
// and keeps the Go build cache its tasks fill under a limit when asked to.
//
// Every task builds and tests in a worktree of its own, and the build
// caches those runs fill (Go's above all: it never trims itself on a
// schedule of its own) can grow until the disk is full. The runner reports
// what it measures here to its hub with its heartbeat; the hub warns when
// space runs low, and a runner too full doesn't start a task.
package disk

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// GB is the unit sizes are set and shown in (a gibibyte, as df -h counts).
const GB = 1 << 30

// Defaults for Settings left at 0.
const (
	DefaultWarnPercent = 10 // warn below 10% free…
	DefaultWarnGB      = 10 // …or 10 GB, whichever is smaller
	DefaultMinFreeGB   = 2  // don't start a task below this
)

// Settings are a runner's disk thresholds, set on the hub (Runners → the
// runner → Disk) and sent to the runner with the hub's answer to its
// reports. 0 is the default.
type Settings struct {
	// WarnPercent and WarnGB: the hub warns when a disk has less free than
	// the smaller of WarnPercent of its size and WarnGB.
	WarnPercent float64 `json:"warn_percent,omitempty"`
	WarnGB      float64 `json:"warn_gb,omitempty"`
	// MinFreeGB: below it, the runner starts no task.
	MinFreeGB float64 `json:"min_free_gb,omitempty"`
	// TrimGoCacheGB: when set, the runner runs `go clean -cache` once the
	// Go build cache is bigger than this, while no task builds. 0: off.
	TrimGoCacheGB float64 `json:"trim_go_cache_gb,omitempty"`
}

// Clean bounds s to sensible values (negative ones are the default).
func (s Settings) Clean() Settings {
	clamp := func(v, hi float64) float64 {
		if v < 0 || v != v { // NaN too
			return 0
		}
		return min(v, hi)
	}
	return Settings{
		WarnPercent:   clamp(s.WarnPercent, 99),
		WarnGB:        clamp(s.WarnGB, 100000),
		MinFreeGB:     clamp(s.MinFreeGB, 100000),
		TrimGoCacheGB: clamp(s.TrimGoCacheGB, 100000),
	}
}

// WarnBelow is how many free bytes a disk of total bytes may have before
// it is low.
func (s Settings) WarnBelow(total uint64) uint64 {
	pct, gb := s.WarnPercent, s.WarnGB
	if pct <= 0 {
		pct = DefaultWarnPercent
	}
	if gb <= 0 {
		gb = DefaultWarnGB
	}
	return min(uint64(float64(total)*pct/100), uint64(gb*GB))
}

// MinFree is the free space a runner needs to start a task.
func (s Settings) MinFree() uint64 {
	gb := s.MinFreeGB
	if gb <= 0 {
		gb = DefaultMinFreeGB
	}
	return uint64(gb * GB)
}

// TrimAbove is the Go build cache size past which it is trimmed (0: never).
func (s Settings) TrimAbove() uint64 {
	if s.TrimGoCacheGB <= 0 {
		return 0
	}
	return uint64(s.TrimGoCacheGB * GB)
}

// Volume is a filesystem a runner measured: the one its projects are on,
// its tasks' worktrees' and its cache folder's when those are others.
type Volume struct {
	// Label says what is on it: "projects", "worktrees" (the tasks'), or
	// "cache" (the user's cache folder, where build caches go).
	Label string `json:"label"`
	// Path is the folder measured.
	Path  string `json:"path"`
	Free  uint64 `json:"free"`
	Total uint64 `json:"total"`
}

// Folder is a folder and how much it holds.
type Folder struct {
	Path string `json:"path"`
	Size uint64 `json:"size"`
}

// GoCache is the Go build cache of the runner's user (`go env GOCACHE`).
type GoCache struct {
	Path string `json:"path"`
	Size uint64 `json:"size"`
	// Trimmed is when the runner last ran `go clean -cache` (zero: never
	// since it started), and Freed how much that freed.
	Trimmed string `json:"trimmed,omitempty"`
	Freed   uint64 `json:"freed,omitempty"`
	// Error is why measuring or trimming it last failed.
	Error string `json:"error,omitempty"`
}

// Status is what a runner reports about its disks.
type Status struct {
	Volumes []Volume `json:"volumes"`
	// Largest are the biggest folders under the user's cache folder,
	// measured when space ran low (none while it isn't).
	Largest []Folder `json:"largest,omitempty"`
	// GoCache is the Go build cache, as last measured (nil: not yet, or no go).
	GoCache *GoCache `json:"go_cache,omitempty"`
}

// Lowest is the volume with the least free space (ok false: none measured).
func (st Status) Lowest() (Volume, bool) {
	if len(st.Volumes) == 0 {
		return Volume{}, false
	}
	return slices.MinFunc(st.Volumes, func(a, b Volume) int {
		switch {
		case a.Free < b.Free:
			return -1
		case a.Free > b.Free:
			return 1
		}
		return 0
	}), true
}

// Low is the first volume whose free space is below s's warning threshold,
// or below the minimum to start a task (on a small disk that is the higher
// of the two: a runner that starts no task always warns).
func (s Settings) Low(st Status) (Volume, bool) {
	for _, v := range st.Volumes {
		if v.Total > 0 && v.Free < max(s.WarnBelow(v.Total), s.MinFree()) {
			return v, true
		}
	}
	return Volume{}, false
}

// TooFull is the first volume with less free than a task needs to start.
func (s Settings) TooFull(st Status) (Volume, bool) {
	for _, v := range st.Volumes {
		if v.Total > 0 && v.Free < s.MinFree() {
			return v, true
		}
	}
	return Volume{}, false
}

// Size is n bytes for people: "1.2 GB", "640 MB".
func Size(n uint64) string {
	switch {
	case n >= 100*GB:
		return fmt.Sprintf("%.0f GB", float64(n)/GB)
	case n >= GB:
		return fmt.Sprintf("%.1f GB", float64(n)/GB)
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// Measure measures the filesystems of the folders in targets (label →
// folder), each once: a folder on a filesystem already measured is left
// out. Folders that don't exist yet are measured at their nearest parent.
func Measure(targets []Target) []Volume {
	var out []Volume
	var seen []string
	for _, t := range targets {
		if t.Path == "" {
			continue
		}
		p := existing(t.Path)
		id, err := deviceID(p)
		if err == nil && slices.Contains(seen, id) {
			continue
		}
		free, total, err := usage(p)
		if err != nil {
			continue
		}
		if id != "" {
			seen = append(seen, id)
		}
		out = append(out, Volume{Label: t.Label, Path: t.Path, Free: free, Total: total})
	}
	return out
}

// Target is a folder to measure the filesystem of.
type Target struct {
	Label string
	Path  string
}

// existing is p, or its nearest parent that exists.
func existing(p string) string {
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

// DirSize is how many bytes the files under dir take. Symbolic links aren't
// followed, and unreadable folders are skipped.
func DirSize(dir string) (uint64, error) {
	var n uint64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += uint64(info.Size())
			}
		}
		return nil
	})
	return n, err
}

// Largest lists the n biggest folders under dir, biggest first. It looks
// two levels down: a folder whose space is mostly in one of its own
// (~/.cache/quadrate in ~/.cache/quadrate/builds) is shown as that one,
// which says better what is using it.
func Largest(dir string, n int, size func(string) (uint64, error)) []Folder {
	if size == nil {
		size = DirSize
	}
	var out []Folder
	for _, top := range subdirs(dir) {
		total, err := size(top)
		if err != nil || total == 0 {
			continue
		}
		best := Folder{Path: top, Size: total}
		for _, sub := range subdirs(top) {
			if s, err := size(sub); err == nil && s*10 >= total*8 && s > 0 {
				best = Folder{Path: sub, Size: s}
				break
			}
		}
		out = append(out, best)
	}
	slices.SortFunc(out, func(a, b Folder) int {
		switch {
		case a.Size > b.Size:
			return -1
		case a.Size < b.Size:
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func subdirs(dir string) []string {
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range es {
		if e.IsDir() {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}
