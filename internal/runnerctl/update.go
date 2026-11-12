package runnerctl

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/klahr/dash/internal/fsutil"
)

// A runner updates itself (`dash runner update`, or when its hub asks):
// it downloads the release's archive for this computer, checks it against
// the release's checksums.txt, makes sure the new binary runs, keeps the
// old one as <bin>.prev and puts the new one in its place. The runner then
// restarts on it (ExitUpdated under a service, else in place). Started,
// the new binary is on trial (BeginTrial): crashing on start again and
// again, or not reaching the hub within TrialTimeout, puts the old one back.

// ErrChecksum means the downloaded archive isn't the one the release lists.
var ErrChecksum = errors.New("checksum mismatch")

// Updater replaces the dash binary Bin with release Version.
type Updater struct {
	Version string // the release to install, e.g. 1.2.0
	Bin     string // the dash binary to replace (symlinks resolved)
	// Data is the runner's data directory: the new binary's trial is kept
	// there (see BeginTrial). "" = no trial.
	Data    string
	Current string // the version running now, for the trial's record
	// Zero values default to the real ones.
	Releases     string
	GOOS, GOARCH string
	Client       *http.Client
}

// maxArchive bounds a release archive's download.
const maxArchive = 512 << 20

// Run downloads, checks and installs the release. Nothing is replaced
// unless every check passed.
func (u Updater) Run(ctx context.Context) error {
	if !releaseRe.MatchString(u.Version) {
		return fmt.Errorf("%q is not a release version", u.Version)
	}
	if u.Releases == "" {
		u.Releases = Releases
	}
	if u.GOOS == "" {
		u.GOOS = runtime.GOOS
	}
	if u.GOARCH == "" {
		u.GOARCH = runtime.GOARCH
	}
	if u.Client == nil {
		u.Client = &http.Client{Timeout: 10 * time.Minute}
	}
	// First whether the binary can be replaced at all: no download for nothing.
	dir := filepath.Dir(u.Bin)
	tmp, err := os.CreateTemp(dir, ".dash.new-*"+exeSuffix(u.GOOS))
	if err != nil {
		return fmt.Errorf("can't replace %s (installed by another user, e.g. root?): update it the way it was installed: %w", u.Bin, err)
	}
	defer os.Remove(tmp.Name()) // nothing left once renamed
	defer tmp.Close()

	asset := Asset(u.Version, u.GOOS, u.GOARCH)
	base := strings.TrimRight(u.Releases, "/") + "/download/" + u.Version + "/"
	sums, err := u.get(ctx, base+"checksums.txt", 1<<20)
	if err != nil {
		return fmt.Errorf("the checksums of Dash %s: %w", u.Version, err)
	}
	want := checksumFor(sums, asset)
	if want == "" {
		return fmt.Errorf("checksums.txt of Dash %s lists no %s", u.Version, asset)
	}
	archive, err := u.get(ctx, base+asset, maxArchive)
	if err != nil {
		return fmt.Errorf("download %s (is there a %s release for %s/%s?): %w", asset, u.Version, u.GOOS, u.GOARCH, err)
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("%w for %s (got %s, want %s): not installing it", ErrChecksum, asset, got, want)
	}
	if err := extractDash(archive, u.GOOS, tmp); err != nil {
		return fmt.Errorf("unpack %s: %w", asset, err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// The new binary must run here (the right processor, not cut off).
	vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	out, err := runFresh(vctx, tmp.Name(), "version")
	cancel()
	if err != nil {
		return fmt.Errorf("the downloaded Dash %s doesn't run here: %w", u.Version, err)
	}
	if got := strings.TrimSpace(string(out)); bare(strings.TrimPrefix(got, "dash ")) != bare(u.Version) {
		return fmt.Errorf("the downloaded Dash says it is %q, not %s", got, u.Version)
	}
	prev := u.Bin + ".prev"
	if err := backup(u.Bin, prev); err != nil {
		return fmt.Errorf("keep the old binary as %s: %w", prev, err)
	}
	if u.Data != "" {
		if err := saveTrial(TrialPath(u.Data), trial{From: u.Current, To: u.Version, Bin: u.Bin, Prev: prev, At: time.Now()}); err != nil {
			return fmt.Errorf("record the update: %w", err)
		}
	}
	// Renamed over it, so a running dash is replaced whole.
	if err := replaceFile(tmp.Name(), u.Bin); err != nil {
		if u.Data != "" {
			os.Remove(TrialPath(u.Data))
		}
		return fmt.Errorf("can't replace %s: %w", u.Bin, err)
	}
	return nil
}

// runFresh runs a binary that was just written. On Linux its exec fails
// with ETXTBSY ("text file busy") while any process still holds the file
// open for writing, which includes a child forked elsewhere in this process
// while the file was open: it keeps the inherited fd until its own exec
// closes it. That window is short, so a busy file is tried again.
func runFresh(ctx context.Context, name string, args ...string) ([]byte, error) {
	for wait := 10 * time.Millisecond; ; wait *= 2 {
		out, err := exec.CommandContext(ctx, name, args...).Output()
		if !errors.Is(err, syscall.ETXTBSY) || wait > time.Second {
			return out, err
		}
		select {
		case <-ctx.Done():
			return out, err
		case <-time.After(wait):
		}
	}
}

func (u Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := u.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", url, limit)
	}
	return b, nil
}

// checksumFor is asset's sha256 in a checksums.txt ("<hex>  <file>").
func checksumFor(sums []byte, asset string) string {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && (f[1] == asset || f[1] == "*"+asset) {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// Asset is the name of a release's archive for goos/goarch:
// dash_1.2.0_linux_amd64.tar.gz, or a .zip for Windows.
func Asset(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("dash_%s_%s_%s%s", strings.TrimPrefix(version, "v"), goos, goarch, ext)
}

// exeSuffix is what executables end with on goos.
func exeSuffix(goos string) string {
	if goos == "windows" {
		return ".exe"
	}
	return ""
}

// extractDash copies the dash binary out of a release's archive for goos
// (a .tar.gz, or a .zip holding dash.exe for Windows) into w.
func extractDash(archive []byte, goos string, w io.Writer) error {
	name := "dash" + exeSuffix(goos)
	if goos == "windows" {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return err
		}
		for _, f := range zr.File {
			if f.Mode().IsRegular() && path.Base(f.Name) == name {
				r, err := f.Open()
				if err != nil {
					return err
				}
				defer r.Close()
				_, err = io.Copy(w, io.LimitReader(r, maxArchive))
				return err
			}
		}
		return errors.New("it holds no " + name)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("it holds no dash binary")
		}
		if err != nil {
			return err
		}
		if hd.Typeflag == tar.TypeReg && path.Base(hd.Name) == name {
			_, err := io.Copy(w, io.LimitReader(tr, maxArchive))
			return err
		}
	}
}

// replaceFile renames src over dst. Windows won't replace a program that
// is running (the runner's own binary), but lets it be renamed: there dst
// is first moved aside to dst.old, removed by the next replacement.
func replaceFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	old := dst + ".old"
	if os.Remove(old) != nil && exists(old) { // an older one still runs
		old = fmt.Sprintf("%s.old-%d", dst, time.Now().UnixNano())
	}
	if os.Rename(dst, old) != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		_ = os.Rename(old, dst)
		return err
	}
	return nil
}

// backup makes prev a copy of bin: a hard link when it can.
func backup(bin, prev string) error {
	if err := os.Remove(prev); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if os.Link(bin, prev) == nil {
		return nil
	}
	src, err := os.Open(bin)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(prev, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(prev)
		return err
	}
	return dst.Close()
}

func bare(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

// SameVersion compares two versions, ignoring a leading "v" on either.
func SameVersion(a, b string) bool { return bare(a) == bare(b) }

// ---- the new binary's trial ----

// TrialTimeout is how long a just-updated runner has to reach its hub
// before the old binary is put back; trialStarts is how often it may start
// without reaching it (a crash loop) before that.
const (
	TrialTimeout = 2 * time.Minute
	trialStarts  = 3
)

// ErrRolledBack means the new binary failed its trial and the old one is
// back in its place: restart to run it.
var ErrRolledBack = errors.New("the updated Dash failed to start; the previous version was put back")

// trial is an update not yet known to work, kept in TrialPath.
type trial struct {
	From   string    `json:"from"`
	To     string    `json:"to"`
	Bin    string    `json:"bin"`
	Prev   string    `json:"prev"`
	At     time.Time `json:"at"`
	Starts int       `json:"starts"`
}

// TrialPath is where a runner keeps its update on trial.
func TrialPath(data string) string { return filepath.Join(data, "update.json") }

func saveTrial(p string, t trial) error {
	b, _ := json.MarshalIndent(t, "", "  ")
	return fsutil.WriteFileAtomic(p, b, 0o600)
}

// Trial is a just-updated runner's new binary on trial.
type Trial struct {
	path string
	t    trial
}

// BeginTrial is called when the runner starts, running version: if it
// was just updated to it, the update is on trial (nil when it wasn't). A
// trial already started trialStarts times is a crash loop: the old binary
// is put back and ErrRolledBack returned.
func BeginTrial(data, version string) (*Trial, error) {
	p := TrialPath(data)
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var t trial
	if err == nil {
		err = json.Unmarshal(b, &t)
	}
	if err != nil || !SameVersion(t.To, version) {
		// Unreadable, or the update didn't take (the old binary runs).
		os.Remove(p)
		return nil, nil
	}
	tr := &Trial{path: p, t: t}
	t.Starts++
	if t.Starts > trialStarts {
		if err := tr.Rollback(); err != nil {
			return nil, err
		}
		return nil, ErrRolledBack
	}
	tr.t = t
	return tr, saveTrial(p, t)
}

// From is the version the trial would go back to.
func (t *Trial) From() string { return t.t.From }

// Confirm ends the trial: the new binary reached the hub. The old one is
// kept as the backup.
func (t *Trial) Confirm() { os.Remove(t.path) }

// Rollback puts the old binary back in place and ends the trial.
func (t *Trial) Rollback() error {
	defer os.Remove(t.path)
	if t.t.Prev == "" || t.t.Bin == "" {
		return errors.New("the update's record names no previous binary")
	}
	if err := replaceFile(t.t.Prev, t.t.Bin); err != nil {
		return fmt.Errorf("put %s back: %w", t.t.Prev, err)
	}
	return nil
}

// UnderService reports whether this process runs as a service that
// restarts it when it exits with ExitUpdated (systemd, launchd).
func UnderService() bool {
	switch runtime.GOOS {
	case "linux":
		return os.Getenv("INVOCATION_ID") != ""
	case "darwin":
		return os.Getenv("XPC_SERVICE_NAME") == LaunchdLabel
	}
	// Windows: the scheduled task doesn't restart a runner that exited
	// cleanly, so the runner restarts itself in place.
	return false
}
