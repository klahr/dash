package disk

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestThresholds(t *testing.T) {
	var s Settings
	// 10% or 10 GB, whichever is smaller.
	if got := s.WarnBelow(50 * GB); got != 5*GB {
		t.Errorf("50 GB disk: warn below %s, want 5 GB", Size(got))
	}
	if got := s.WarnBelow(1000 * GB); got != 10*GB {
		t.Errorf("1 TB disk: warn below %s, want 10 GB", Size(got))
	}
	if s.MinFree() != 2*GB {
		t.Errorf("min free = %s, want 2 GB", Size(s.MinFree()))
	}
	if s.TrimAbove() != 0 {
		t.Error("the Go cache trim must be off by default")
	}
	s = Settings{WarnPercent: 20, WarnGB: 50, MinFreeGB: 5, TrimGoCacheGB: 30}
	if got := s.WarnBelow(100 * GB); got != 20*GB {
		t.Errorf("custom: warn below %s, want 20 GB", Size(got))
	}
	if s.MinFree() != 5*GB || s.TrimAbove() != 30*GB {
		t.Errorf("custom: min %s, trim %s", Size(s.MinFree()), Size(s.TrimAbove()))
	}
	if c := (Settings{WarnPercent: -3, WarnGB: 1e9}).Clean(); c.WarnPercent != 0 || c.WarnGB != 100000 {
		t.Errorf("Clean = %+v", c)
	}

	st := Status{Volumes: []Volume{{Label: "projects", Free: 30 * GB, Total: 100 * GB}, {Label: "cache", Free: GB, Total: 100 * GB}}}
	if v, low := (Settings{}).Low(st); !low || v.Label != "cache" {
		t.Errorf("Low = %+v, %v; want the cache volume", v, low)
	}
	if v, full := (Settings{}).TooFull(st); !full || v.Label != "cache" {
		t.Errorf("TooFull = %+v, %v", v, full)
	}
	st.Volumes[1].Free = 20 * GB
	if _, low := (Settings{}).Low(st); low {
		t.Error("20 and 30 GB free of 100 GB isn't low")
	}
	// A small disk: 10% of it is less than the minimum to start a task,
	// and a runner that starts none must warn.
	small := Status{Volumes: []Volume{{Label: "projects", Free: GB + GB/2, Total: 6 * GB}}}
	if _, low := (Settings{}).Low(small); !low {
		t.Error("too full to start a task, yet not low")
	}
}

func TestMeasureRealDisk(t *testing.T) {
	dir := t.TempDir()
	vs := Measure([]Target{{"projects", dir}, {"cache", filepath.Join(dir, "not", "there", "yet")}})
	if len(vs) != 1 {
		t.Fatalf("one filesystem measured twice, or not at all: %+v", vs)
	}
	if vs[0].Total == 0 || vs[0].Free > vs[0].Total || vs[0].Label != "projects" {
		t.Errorf("measured %+v", vs[0])
	}
}

func TestLargest(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string, n int) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go-build/aa/x", 5000)
	write("go-build/bb/y", 5000)
	write("quadrate/builds/one/z", 9000)
	write("quadrate/meta", 100)
	write("small/a", 10)
	got := Largest(dir, 2, nil)
	if len(got) != 2 {
		t.Fatalf("Largest = %+v", got)
	}
	if got[0].Path != filepath.Join(dir, "go-build") || got[0].Size != 10000 {
		t.Errorf("first = %+v, want go-build with 10000", got[0])
	}
	if got[1].Path != filepath.Join(dir, "quadrate", "builds") || got[1].Size != 9000 {
		t.Errorf("second = %+v, want quadrate/builds (most of quadrate) with 9000", got[1])
	}
}

// fakeDisk is a runner's disk and Go cache for a Monitor.
type fakeDisk struct {
	free, total uint64
	cache       uint64
	busy        bool
	cleaned     int
}

func (f *fakeDisk) monitor() *Monitor {
	return &Monitor{
		Name:    "box",
		Targets: []Target{{"projects", "/p"}},
		Measure: func([]Target) []Volume {
			return []Volume{{Label: "projects", Path: "/p", Free: f.free, Total: f.total}}
		},
		Size: func(dir string) (uint64, error) {
			if dir == "/home/u/.cache/go-build" {
				return f.cache, nil
			}
			return DirSize(dir)
		},
		GoCache: func(context.Context) (string, error) { return "/home/u/.cache/go-build", nil },
		CleanGo: func(context.Context) error { f.cleaned++; f.free += f.cache; f.cache = 0; return nil },
		Busy:    func() bool { return f.busy },
	}
}

func TestCanStart(t *testing.T) {
	f := &fakeDisk{free: GB + GB/5, total: 500 * GB}
	m := f.monitor()
	err := m.CanStart(nil)
	var full *FullError
	if !errors.As(err, &full) {
		t.Fatalf("1.2 GB free: CanStart = %v, want a FullError", err)
	}
	if !strings.Contains(err.Error(), "runner box has only 1.2 GB free") {
		t.Errorf("message = %q", err)
	}
	f.free = 3 * GB
	if err := m.CanStart(nil); err != nil {
		t.Errorf("3 GB free: %v", err)
	}
	m.SetSettings(Settings{MinFreeGB: 5})
	if err := m.CanStart(nil); err == nil {
		t.Error("3 GB free with a 5 GB minimum must not start")
	}
	// The thresholds sent with a task are the hub's latest: they count.
	if err := m.CanStart(&Settings{MinFreeGB: 1}); err != nil {
		t.Errorf("3 GB free with the task's 1 GB minimum: %v", err)
	}
}

func TestTrimGoCache(t *testing.T) {
	f := &fakeDisk{free: 100 * GB, total: 500 * GB, cache: 40 * GB}
	m := f.monitor()
	ctx := context.Background()

	// Off by default.
	if m.TrimGoCache(ctx) || f.cleaned != 0 {
		t.Fatal("trimmed with the setting off")
	}
	// Under the limit: nothing.
	m.SetSettings(Settings{TrimGoCacheGB: 50})
	if m.TrimGoCache(ctx) || f.cleaned != 0 {
		t.Fatal("trimmed under the limit")
	}
	if st := m.Status(); st.GoCache == nil || st.GoCache.Size != 40*GB {
		t.Errorf("Go cache status = %+v", st.GoCache)
	}
	// Over it, but a task is building: not now…
	f.cache = 60 * GB
	f.busy = true
	if m.TrimGoCache(ctx) || f.cleaned != 0 {
		t.Fatal("trimmed while a task was running")
	}
	// …but as soon as it is done, at the next check.
	f.busy = false
	m.Check(ctx)
	if f.cleaned != 1 {
		t.Fatalf("the trim held up by a task didn't run once it ended (cleaned %d times)", f.cleaned)
	}
	st := m.Status()
	if st.GoCache == nil || st.GoCache.Size != 0 || st.GoCache.Freed != 60*GB || st.GoCache.Trimmed == "" {
		t.Errorf("after the trim: %+v", st.GoCache)
	}
	// Checked again a day later, not every minute.
	f.cache = 70 * GB
	m.Check(ctx)
	if f.cleaned != 1 {
		t.Error("trimmed again within the day")
	}
}

func TestLowSpaceTrimsAndMeasures(t *testing.T) {
	cache := t.TempDir()
	for _, d := range []string{"quadrate/builds", "quadrate/logs"} {
		if err := os.MkdirAll(filepath.Join(cache, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cache, d, "f"), make([]byte, 1000), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeDisk{free: 50 * GB, total: 200 * GB, cache: 30 * GB}
	m := f.monitor()
	m.CacheDir = cache
	now := time.Now()
	m.Now = func() time.Time { return now }
	m.SetSettings(Settings{TrimGoCacheGB: 20})
	ctx := context.Background()
	m.Check(ctx) // the first check: over the limit, trimmed
	if f.cleaned != 1 {
		t.Fatalf("cleaned %d times", f.cleaned)
	}
	if st := m.Status(); len(st.Largest) != 0 {
		t.Errorf("largest folders listed while space isn't low: %+v", st.Largest)
	}
	// Space runs low (something else filled it): the cache is checked at
	// once, and the largest folders measured.
	f.cache, f.free = 25*GB, 5*GB
	now = now.Add(time.Hour)
	m.Check(ctx)
	if f.cleaned != 2 {
		t.Errorf("running low didn't check the Go cache (cleaned %d times)", f.cleaned)
	}
	f.free = 5 * GB // still low after the trim (fake)
	st := m.Status()
	if len(st.Largest) != 1 || st.Largest[0].Path != filepath.Join(cache, "quadrate") {
		t.Errorf("largest = %+v", st.Largest)
	}
}
