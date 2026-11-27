package screenshot

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/klahr/dash/internal/fsutil"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/localrun"
	"github.com/klahr/dash/internal/model"
)

// Defaults of a Capturer's limits.
const (
	DefaultTimeout      = 6 * time.Minute  // one task's whole capture, both sides
	DefaultStartTimeout = 3 * time.Minute  // a server to answer (the before side installs first)
	DefaultPageTimeout  = 25 * time.Second // one page at one size
	setupTimeout        = 2 * time.Minute  // the screenshot setup command
)

// size is a viewport a page is captured at. maxH caps the picture's height
// (CSS pixels): a long page is cut off there.
type size struct {
	name   string
	w, h   int64
	scale  float64
	mobile bool
	maxH   int64
}

var sizes = []size{
	{name: "wide", w: 1280, h: 800, scale: 1, maxH: 1600},
	{name: "phone", w: 390, h: 844, scale: 2, mobile: true, maxH: 1266},
}

// webpQuality keeps the pictures small: a wide page is some 50–150 KB.
const webpQuality = 70

// Capturer takes the screenshots of finished tasks on this computer, one
// task at a time, and keeps them under Root/<task id>/.
type Capturer struct {
	Root string
	// Chrome is the browser to run ("": FindChrome's). A path that isn't
	// there means none.
	Chrome string
	Git    *gitops.Git
	// Limits (0: the defaults above).
	Timeout, StartTimeout, PageTimeout time.Duration

	once   sync.Once
	chrome string
	sem    chan struct{}
}

// ChromePath is the browser captures run, or "" when there is none.
func (c *Capturer) ChromePath() string {
	c.once.Do(func() {
		if c.Chrome == "" {
			c.chrome = FindChrome()
		} else if p, err := exec.LookPath(c.Chrome); err == nil {
			c.chrome = p
		}
		c.sem = make(chan struct{}, 1)
	})
	return c.chrome
}

// FindChrome is $DASH_CHROME, or a Chrome or Chromium installed here ("":
// none).
func FindChrome() string {
	if p := strings.TrimSpace(os.Getenv("DASH_CHROME")); p != "" {
		if found, err := exec.LookPath(p); err == nil {
			return found
		}
		return ""
	}
	names := []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "headless_shell", "headless-shell",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium",
		`C:\Program Files\Google\Chrome\Application\chrome.exe`, `C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`, "msedge"}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// Job is one task's capture.
type Job struct {
	Project model.Project
	TodoID  string
	Run     int // the task's run (model.Todo.Runs), in the file names
	// Dir is the task's worktree: the after side runs there, as it is (its
	// setup command ran when it was made).
	Dir string
	// Base is the commit the task started from, for the before side: it
	// runs in a throwaway worktree there, after the project's setup
	// command. Empty: no before side.
	Base  string
	Pages []model.ScreenshotPage
	// Env is the environment of the project's commands (nil: this process's).
	Env []string
	Log func(string)
}

func (j Job) log(s string) {
	if j.Log != nil {
		j.Log(s)
	}
}

// Capture takes j's screenshots and returns them as the task's set. It
// never fails: what went wrong is the set's Note (and, with nothing
// taken, its state is ShotsSkipped). ctx ending stops it.
func (c *Capturer) Capture(ctx context.Context, j Job) model.ScreenshotSet {
	set := model.ScreenshotSet{State: model.ShotsSkipped, Run: j.Run, Pages: make([]model.ShotPage, len(j.Pages))}
	for i, p := range j.Pages {
		set.Pages[i].ScreenshotPage = p
	}
	done := func(notes ...string) model.ScreenshotSet {
		set.At = time.Now()
		for _, p := range set.Pages {
			if len(p.Files()) > 0 {
				set.State = model.ShotsDone
			}
		}
		set.Note = strings.Join(notes, " ")
		return set
	}
	if err := localrun.Check("screenshots"); err != nil {
		return done("No screenshots: " + err.Error())
	}
	chrome := c.ChromePath()
	if chrome == "" {
		return done("No screenshots: this computer has no Chrome or Chromium (set DASH_CHROME to its path).")
	}
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return done("No screenshots: the task moved on before they were taken.")
	}
	ctx, cancel := context.WithTimeout(ctx, or(c.Timeout, DefaultTimeout))
	defer cancel()

	dir, err := c.dir(j.TodoID)
	if err == nil {
		err = c.clearOld(dir, j.Run)
	}
	if err != nil {
		return done("No screenshots: " + err.Error())
	}
	profile, err := os.MkdirTemp("", "dash-shots-")
	if err != nil {
		return done("No screenshots: " + err.Error())
	}
	defer os.RemoveAll(profile)
	actx, stopAlloc := chromedp.NewExecAllocator(ctx, append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome),
		chromedp.UserDataDir(profile),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("no-sandbox", os.Geteuid() == 0), // Chrome refuses to run as root with its sandbox
	)...)
	defer stopAlloc()
	bctx, stopBrowser := chromedp.NewContext(actx)
	defer stopBrowser()
	if err := chromedp.Do(bctx, chromedp.Navigate("about:blank")); err != nil {
		return done("No screenshots: Chrome didn't start (" + firstLine(err.Error()) + ").")
	}

	var notes []string
	j.log("📸 taking screenshots of " + plural(len(j.Pages), "page") + ": after the change")
	if err := c.side(ctx, bctx, j, "after", j.Dir, j.Project.RunCommand, dir, set.Pages); err != nil {
		notes = append(notes, "After: "+err.Error())
	}
	if j.Base != "" && ctx.Err() == nil {
		j.log("📸 screenshots: before the change, at " + gitops.ShortSHA(j.Base))
		if err := c.before(ctx, bctx, j, dir, set.Pages); err != nil {
			notes = append(notes, "Before: "+err.Error())
		}
	}
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		notes = append(notes, fmt.Sprintf("Stopped after %s.", or(c.Timeout, DefaultTimeout)))
	}
	out := done(notes...)
	if out.State == model.ShotsDone {
		j.log("📸 screenshots taken" + noteSuffix(out.Note))
	} else {
		j.log("📸 no screenshots" + noteSuffix(out.Note))
	}
	return out
}

func noteSuffix(n string) string {
	if n == "" {
		return ""
	}
	return ": " + n
}

// before runs the project at j.Base in a throwaway worktree (its copied
// files and setup command first, as a commit preview does) and captures it.
func (c *Capturer) before(ctx, bctx context.Context, j Job, dir string, pages []model.ShotPage) error {
	p := j.Project
	if c.Git == nil {
		return errors.New("no git to check the commit out with")
	}
	if why := p.Held(model.CmdSetup); why != "" {
		return errors.New(why)
	}
	wt := filepath.Join(c.Root, ".work", j.TodoID)
	c.Git.RemoveDetachedWorktree(context.WithoutCancel(ctx), p.ID, p.Path, wt) // a leftover from a capture cut short
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		return err
	}
	if err := c.Git.AddDetachedWorktree(ctx, p.ID, p.Path, wt, j.Base); err != nil {
		return fmt.Errorf("couldn't check out %s: %s", gitops.ShortSHA(j.Base), firstLine(err.Error()))
	}
	defer func() {
		bg := context.WithoutCancel(ctx)
		c.Git.RemoveDetachedWorktree(bg, p.ID, p.Path, wt)
		_ = os.RemoveAll(wt)
		c.Git.PruneWorktrees(bg, p.ID, p.Path)
	}()
	for _, f := range p.CopyFiles {
		f = filepath.Clean(strings.TrimSpace(f))
		if f == "" || f == "." || !fsutil.RelInside(f) {
			continue
		}
		if err := fsutil.CopyPath(filepath.Join(p.Path, f), filepath.Join(wt, f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			j.log("📸 couldn't copy " + f + " for the before screenshots: " + err.Error())
		}
	}
	script := p.RunCommand
	if setup := strings.TrimSpace(p.SetupCommand); setup != "" {
		script = "(\n" + setup + "\n) || exit $?\n" + p.RunCommand
	}
	return c.side(ctx, bctx, j, "before", wt, script, dir, pages)
}

// side starts script (the server, with $PORT) in workDir, runs the
// screenshot setup command once it answers, and captures every page at
// every size into dir, naming the files in pages. The error says what
// went wrong, briefly; pictures taken before it are kept.
func (c *Capturer) side(ctx, bctx context.Context, j Job, side, workDir, script, dir string, pages []model.ShotPage) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	srv, err := startServer(workDir, script, j.Env, port)
	if err != nil {
		return fmt.Errorf("the server didn't start: %s", firstLine(err.Error()))
	}
	defer srv.stop()
	if err := srv.wait(ctx, port, or(c.StartTimeout, DefaultStartTimeout)); err != nil {
		return err
	}
	base := fmt.Sprintf("http://localhost:%d", port)
	var problems []string
	cookies, err := runSetup(ctx, j, workDir, port, base)
	if err != nil {
		j.log("📸 screenshot setup: " + err.Error())
		problems = append(problems, "the setup command "+err.Error()+".")
	}
	// A browser context of its own per side: the sides' cookies (both on
	// localhost) and storage don't mix.
	tctx, closeTab := chromedp.NewContext(bctx, chromedp.WithNewBrowserContext())
	defer closeTab()
	if len(cookies) > 0 {
		if err := chromedp.Do(tctx, setCookies(base, cookies)); err != nil {
			j.log("📸 couldn't set the setup command's cookies: " + err.Error())
		}
	}
	var failed []string
	for i, pg := range j.Pages {
		for _, s := range sizes {
			if ctx.Err() != nil {
				return errors.New("ran out of time")
			}
			b, err := shoot(tctx, base+pg.Path, s, or(c.PageTimeout, DefaultPageTimeout))
			if err == nil {
				name := shotName(j.Run, i, s.name, side)
				if err = os.WriteFile(filepath.Join(dir, name), b, 0o644); err == nil {
					setShot(&pages[i], s.name, side, name)
					continue
				}
			}
			slog.Debug("screenshot", "todo", j.TodoID, "page", pg.Path, "size", s.name, "side", side, "err", err)
			failed = append(failed, fmt.Sprintf("%s (%s): %s", pg.Path, s.name, firstLine(err.Error())))
		}
	}
	if n := len(failed); n > 0 {
		if n > 2 {
			failed = append(failed[:2], fmt.Sprintf("and %d more", n-2))
		}
		problems = append(problems, fmt.Sprintf("%s failed: %s.", plural(n, "picture"), strings.Join(failed, "; ")))
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, " "))
}

func setShot(p *model.ShotPage, size, side, name string) {
	switch size + "-" + side {
	case "wide-before":
		p.WideBefore = name
	case "wide-after":
		p.WideAfter = name
	case "phone-before":
		p.PhoneBefore = name
	case "phone-after":
		p.PhoneAfter = name
	}
}

// shoot opens url at size s and returns the page as a WebP picture, as
// tall as the page up to s.maxH.
func shoot(ctx context.Context, url string, s size, limit time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	opts := []chromedp.EmulateViewportOption{chromedp.EmulateScale(s.scale)}
	if s.mobile {
		opts = append(opts, chromedp.EmulateMobile, chromedp.EmulateTouch, chromedp.EmulatePortrait)
	}
	if err := chromedp.Do(ctx, chromedp.EmulateViewport(s.w, s.h, opts...), chromedp.Navigate(url)); err != nil {
		return nil, err
	}
	// Let web fonts load and the app draw what it fetched.
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[bool](
		`(document.fonts ? document.fonts.ready : Promise.resolve()).then(() => new Promise((ok) => setTimeout(() => ok(true), 600)))`,
		chromedp.EvalAwaitPromise)); err != nil {
		return nil, err
	}
	height, err := chromedp.Run(ctx, chromedp.Evaluate[float64](`Math.max(document.documentElement.scrollHeight, document.body ? document.body.scrollHeight : 0)`))
	if err != nil {
		return nil, err
	}
	h := min(max(int64(height), s.h), s.maxH)
	q, yes := int64(webpQuality), true
	return chromedp.Run(ctx, chromedp.Action[[]byte](func(ctx context.Context, t *chromedp.Target) ([]byte, error) {
		r, err := cdp.Call(ctx, t, page.CaptureScreenshot, page.CaptureScreenshotParams{
			Format: page.CaptureScreenshotFormatWebp, Quality: &q, FromSurface: &yes, CaptureBeyondViewport: &yes,
			Clip: &page.Viewport{Width: float64(s.w), Height: float64(h), Scale: 1},
		})
		if err != nil {
			return nil, err
		}
		return r.Data, nil
	}))
}

// setCookies sets cookies (name=value) for base in the tab's browser context.
func setCookies(base string, cookies [][2]string) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		for _, ck := range cookies {
			if _, err := cdp.Call(ctx, t, network.SetCookie, network.SetCookieParams{Name: ck[0], Value: ck[1], URL: base + "/"}); err != nil {
				return err
			}
		}
		return nil
	})
}

var cookieRE = regexp.MustCompile(`(?i)^\s*set-cookie:\s*([^=;\s]+)=([^;\r\n]*)`)

// parseCookies reads the "Set-Cookie: name=value" lines of out.
func parseCookies(out []byte) [][2]string {
	var cs [][2]string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if m := cookieRE.FindStringSubmatch(sc.Text()); m != nil {
			cs = append(cs, [2]string{m[1], strings.TrimSpace(m[2])})
		}
	}
	return cs
}

// runSetup runs the project's screenshot setup command in dir once the
// server answers at base, and returns the cookies it printed.
func runSetup(ctx context.Context, j Job, dir string, port int, base string) ([][2]string, error) {
	s := j.Project.Screenshots
	if s == nil || strings.TrimSpace(s.SetupCommand) == "" {
		return nil, nil
	}
	if err := localrun.Check("screenshots: setup: " + s.SetupCommand); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, localrun.Bash(), "-lc", s.SetupCommand)
	cmd.Dir = dir
	cmd.Env = append(environ(j.Env), fmt.Sprintf("PORT=%d", port), "DASH_APP_URL="+base)
	localrun.Group(cmd, syscall.SIGKILL, 2*time.Second)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &tail{max: 4 << 10}
	err := cmd.Run()
	localrun.Reap(cmd)
	if err != nil {
		if code, ok := localrun.ExitStatus(err); ok {
			err = fmt.Errorf("exited %d: %s", code, firstLine(cmd.Stderr.(*tail).last()))
		}
	}
	return parseCookies(out.Bytes()), err
}

func environ(env []string) []string {
	if env == nil {
		return localrun.Environ()
	}
	return append([]string(nil), env...)
}

// server is a project's start server command, run for a capture.
type server struct {
	cmd    *exec.Cmd
	exited chan struct{}
	log    *tail
	cancel context.CancelFunc
}

func startServer(dir, script string, env []string, port int) (*server, error) {
	if err := localrun.Check("screenshots: start server: " + script); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, localrun.Bash(), "-lc", script)
	cmd.Dir = dir
	cmd.Env = append(environ(env), fmt.Sprintf("PORT=%d", port))
	localrun.Group(cmd, syscall.SIGKILL, 3*time.Second) // the whole group goes: a dev server's children too
	log := &tail{max: 8 << 10}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	s := &server{cmd: cmd, exited: make(chan struct{}), log: log, cancel: cancel}
	go func() { _ = cmd.Wait(); localrun.Reap(cmd); close(s.exited) }()
	return s, nil
}

// wait waits for the server to answer on port, for at most limit.
func (s *server) wait(ctx context.Context, port int, limit time.Duration) error {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return errors.New("ran out of time while the server started")
		case <-timer.C:
			return fmt.Errorf("the server didn't answer on port %d within %s%s", port, limit, s.lastLog())
		case <-s.exited:
			code := s.cmd.ProcessState.ExitCode()
			return fmt.Errorf("the server exited with code %d%s", code, s.lastLog())
		case <-tick.C:
			if answers(port) {
				return nil
			}
		}
	}
}

func (s *server) lastLog() string {
	if l := firstLine(lastLine(s.log.last())); l != "" {
		return ": " + l
	}
	return ""
}

// stop ends the server and everything it started, and waits for it.
func (s *server) stop() {
	s.cancel()
	select {
	case <-s.exited:
	case <-time.After(10 * time.Second):
	}
}

// answers reports whether something listens on port on a loopback address
// (dev servers often listen on ::1 only).
func answers(port int) bool {
	for _, host := range []string{"127.0.0.1", "::1"} {
		if c, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(port)), 200*time.Millisecond); err == nil {
			c.Close()
			return true
		}
	}
	return false
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// firstLine is s's first line, at most 160 characters: for a short note.
func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 160 {
		s = string(r[:159]) + "…"
	}
	return s
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}

func or(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}
