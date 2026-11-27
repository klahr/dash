package screenshot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/testgit"
)

// TestMain keeps the git the tests start off this computer's repositories
// and settings (see testgit).
func TestMain(m *testing.M) { os.Exit(testgit.Run(m)) }

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	es, _ := os.ReadDir(dir)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// Pictures live in Root/<task>/, only under names a capture makes; a new
// run's capture removes the earlier run's, and a deleted task's go.
func TestStorage(t *testing.T) {
	c := &Capturer{Root: t.TempDir()}
	dir, err := c.dir("t1")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"r1-p0-wide-after.webp", "r2-p0-wide-after.webp", "r2-p1-phone-before.webp", "stray"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.clearOld(dir, 2); err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, dir); !slices.Equal(got, []string{"r2-p0-wide-after.webp", "r2-p1-phone-before.webp"}) {
		t.Fatalf("after clearOld(2): %v", got)
	}
	if p, err := c.Path("t1", "r2-p0-wide-after.webp"); err != nil || p != filepath.Join(dir, "r2-p0-wide-after.webp") {
		t.Fatalf("Path: %q %v", p, err)
	}
	for _, bad := range [][2]string{{"t1", "../x.webp"}, {"../t1", "r2-p0-wide-after.webp"}, {"t1", "stray"}, {"", "r2-p0-wide-after.webp"}} {
		if _, err := c.Path(bad[0], bad[1]); err == nil {
			t.Errorf("Path(%q, %q) allowed", bad[0], bad[1])
		}
	}
	if _, err := c.dir("../escape"); err == nil {
		t.Error("dir took a path")
	}

	// Deleting the task removes its folder; other events don't.
	c.Observe(model.Event{Type: model.EvTodoUpdated, TodoID: "t1"})
	c.Observe(model.Event{Type: model.EvTodoDeleted, TodoID: "t1"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the deleted task's pictures are still there")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var nilC *Capturer
	nilC.Remove("t1") // no capturer: nothing to do
}

func TestSetHas(t *testing.T) {
	s := &model.ScreenshotSet{Pages: []model.ShotPage{{WideAfter: "r1-p0-wide-after.webp", PhoneBefore: "r1-p0-phone-before.webp"}}}
	if !s.Has("r1-p0-wide-after.webp") || !s.Has("r1-p0-phone-before.webp") || s.Has("r1-p0-wide-before.webp") || s.Has("") {
		t.Fatal("Has")
	}
	var none *model.ScreenshotSet
	if none.Has("r1-p0-wide-after.webp") || none.Clone() != nil {
		t.Fatal("nil set")
	}
	cp := s.Clone()
	cp.Pages[0].WideAfter = "changed"
	if s.Pages[0].WideAfter == "changed" {
		t.Fatal("Clone shares its pages")
	}
}

// Without Chrome, nothing is started and the set says why.
func TestCaptureWithoutChrome(t *testing.T) {
	c := &Capturer{Root: t.TempDir(), Chrome: filepath.Join(t.TempDir(), "no-chrome")}
	if c.ChromePath() != "" {
		t.Fatal("a missing Chrome counts as one")
	}
	var lines []string
	set := c.Capture(context.Background(), Job{TodoID: "t1", Run: 1, Pages: []model.ScreenshotPage{pg("/", "Home")}, Log: func(s string) { lines = append(lines, s) }})
	if set.State != model.ShotsSkipped || !strings.Contains(set.Note, "no Chrome") || len(set.Pages) != 1 || len(set.Pages[0].Files()) != 0 {
		t.Fatalf("got %+v", set)
	}
	if _, err := os.Stat(filepath.Join(c.Root, "t1")); !os.IsNotExist(err) {
		t.Fatal("made a folder for no pictures")
	}
}

// A server that exits, or never answers, is a short reason with the end of
// its log, and is stopped.
func TestServerThatDoesntStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash scripts")
	}
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := startServer(t.TempDir(), "echo starting; echo 'Error: missing module'; exit 3", nil, port)
	if err != nil {
		t.Fatal(err)
	}
	err = srv.wait(context.Background(), port, 10*time.Second)
	srv.stop()
	if err == nil || !strings.Contains(err.Error(), "exited with code 3") || !strings.Contains(err.Error(), "Error: missing module") {
		t.Fatalf("got %v", err)
	}

	srv, err = startServer(t.TempDir(), "echo $PORT; sleep 30", nil, port)
	if err != nil {
		t.Fatal(err)
	}
	// A login shell can take a while to read its profile (on CI longer
	// than the limit below): wait until the script runs, so the limit is
	// spent on the server not answering.
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(srv.log.last(), strconv.Itoa(port)); {
		if time.Now().After(deadline) {
			srv.stop()
			t.Fatalf("the script didn't start: %q", srv.log.last())
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	err = srv.wait(context.Background(), port, 500*time.Millisecond)
	srv.stop()
	if err == nil || !strings.Contains(err.Error(), "didn't answer") || !strings.Contains(err.Error(), strconv.Itoa(port)) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 8*time.Second {
		t.Fatalf("stopping the server took %s", time.Since(start))
	}
	select {
	case <-srv.exited:
	default:
		t.Fatal("the server still runs")
	}
}

// The whole capture, in headless Chrome: a static site (python3 -m
// http.server) changed by the task; the before side runs at the base
// commit, in a worktree of its own that is removed afterwards. It needs
// Chrome or Chromium ($DASH_TEST_CHROME, or one on $PATH) and python3.
func TestCaptureBeforeAndAfter(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	chrome := os.Getenv("DASH_TEST_CHROME")
	if chrome == "" {
		chrome = FindChrome()
	}
	if chrome == "" {
		if os.Getenv("DASH_TEST_BROWSER") == "require" {
			t.Fatal("no Chrome or Chromium found: set DASH_TEST_CHROME")
		}
		t.Skip("needs Chrome or Chromium (set DASH_TEST_CHROME)")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("needs python3 for a static server")
	}
	ctx := context.Background()
	g := gitops.New(nil)
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := g.InitRepo(ctx, "p", repo, "main"); err != nil {
		t.Fatal(err)
	}
	git := func(d string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", d, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(d, name, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(d, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(repo, "web/index.html", `<!doctype html><title>x</title><body style="background:#fff"><h1>Before</h1>`)
	git(repo, "add", "-A")
	git(repo, "commit", "-qm", "site")
	base := git(repo, "rev-parse", "HEAD")
	wt := filepath.Join(dir, "wt")
	git(repo, "worktree", "add", "-q", "-b", "dash/t1", wt)
	write(wt, "web/index.html", `<!doctype html><title>x</title><body style="background:#cfc"><h1>After</h1>`)
	git(wt, "commit", "-qam", "change")

	c := &Capturer{Root: filepath.Join(dir, "shots"), Chrome: chrome, Git: g, Timeout: 2 * time.Minute}
	p := model.Project{ID: "p", Path: repo, RunCommand: "exec python3 -m http.server $PORT --bind 127.0.0.1 --directory web",
		Screenshots: &model.ScreenshotSettings{On: true, SetupCommand: `echo "Set-Cookie: seen=yes"`}}
	set := c.Capture(ctx, Job{Project: p, TodoID: "t1", Run: 2, Dir: wt, Base: base, Pages: []model.ScreenshotPage{pg("/", "Home"), pg("/missing.html", "Missing")}})
	if set.State != model.ShotsDone {
		t.Fatalf("got %+v", set)
	}
	for i, pg := range set.Pages {
		want := []string{shotName(2, i, "wide", "before"), shotName(2, i, "wide", "after"), shotName(2, i, "phone", "before"), shotName(2, i, "phone", "after")}
		if !slices.Equal(sorted(pg.Files()), sorted(want)) {
			t.Fatalf("page %d: %+v", i, pg)
		}
		for _, f := range want {
			b, err := os.ReadFile(filepath.Join(c.Root, "t1", f))
			if err != nil || len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
				t.Fatalf("%s isn't a WebP picture (%d bytes, %v)", f, len(b), err)
			}
			if len(b) > 1<<20 {
				t.Fatalf("%s is %d bytes", f, len(b))
			}
		}
	}
	before, _ := os.ReadFile(filepath.Join(c.Root, "t1", set.Pages[0].WideBefore))
	after, _ := os.ReadFile(filepath.Join(c.Root, "t1", set.Pages[0].WideAfter))
	if string(before) == string(after) {
		t.Fatal("before and after are the same picture")
	}
	if _, err := os.Stat(filepath.Join(c.Root, ".work", "t1")); !os.IsNotExist(err) {
		t.Fatal("the before side's worktree is still there")
	}
	if strings.Contains(git(repo, "worktree", "list"), filepath.Join(".work", "t1")) {
		t.Fatal("git still lists the before side's worktree")
	}
}

func sorted(s []string) []string { s = slices.Clone(s); slices.Sort(s); return s }
