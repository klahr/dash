package runnerctl

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"https://dash.example.com": "https://dash.example.com",
		"ABCD-EFGH":                "ABCD-EFGH",
		"":                         "''",
		"a b":                      "'a b'",
		"it's":                     `'it'\''s'`,
		"$(rm -rf ~)":              "'$(rm -rf ~)'",
		"`x`;y":                    "'`x`;y'",
	} {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %s, want %s", in, got, want)
		}
		// And the shell reads it back as the same word.
		out, err := exec.Command("sh", "-c", "printf '%s' "+ShellQuote(in)).Output()
		if err != nil || string(out) != in {
			t.Errorf("sh read %s as %q (%v), want %q", ShellQuote(in), out, err, in)
		}
	}
}

func TestScriptRendering(t *testing.T) {
	b, err := Script(ScriptParams{Hub: "https://dash.example.com/", Version: "v1.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"HUB=https://dash.example.com\n",
		"VERSION=v1.2.0\n",
		"RELEASES=" + Releases + "\n",
		"LATEST_API=" + LatestAPI + "\n",
		"curl -fsSL https://dash.example.com/runner/install.sh | sh -s -- --code ABCD-EFGH",
		`main "$@"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "sudo") && !strings.HasPrefix(strings.TrimSpace(l), "#") {
			t.Errorf("script mentions sudo outside a comment: %s", l)
		}
	}
	if out, err := exec.Command("sh", "-n", "-c", s).CombinedOutput(); err != nil {
		t.Fatalf("script doesn't parse: %v\n%s", err, out)
	}

	// The latest release when the hub isn't one.
	b, _ = Script(ScriptParams{Hub: "https://dash.example.com", Version: ReleaseVersion("dev")})
	if !strings.Contains(string(b), "VERSION=''\n") {
		t.Error("a dev hub should install the latest release")
	}
	for v, want := range map[string]string{
		"1.2.0": "1.2.0", "1.2.0-rc.1": "1.2.0-rc.1", "v1.2.0": "v1.2.0", "v1.2.0-rc.1": "v1.2.0-rc.1",
		"dev": "", "1.2.0-3-gabc123": "", "v1.2.0-3-gabc123": "", "1.2": "", "x1.2.0": "",
	} {
		if got := ReleaseVersion(v); got != want {
			t.Errorf("ReleaseVersion(%q) = %q, want %q", v, got, want)
		}
	}

	// Runners only talk HTTPS (or to localhost).
	if _, err := Script(ScriptParams{Hub: "http://dash.example.com"}); err == nil {
		t.Error("plain http hub accepted")
	}
	if _, err := Script(ScriptParams{Hub: "https://x", Version: "v1; rm -rf ~"}); err == nil {
		t.Error("odd version accepted")
	}
}

func TestScriptHandler(t *testing.T) {
	get := func(h http.Handler, host string, hdr map[string]string) string {
		r := httptest.NewRequest("GET", "/runner/install.sh", nil)
		r.Host = host
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Body.String()
	}
	secure := func(r *http.Request) bool { return r.Header.Get("X-Forwarded-Proto") == "https" }

	// -public-url wins over whatever the request says.
	h := ScriptHandler("https://dash.example.com/", "v1.2.0", secure, true)
	if s := get(h, "evil.example", map[string]string{"X-Forwarded-Host": "evil2.example"}); !strings.Contains(s, "HUB=https://dash.example.com\n") || !strings.Contains(s, "VERSION=v1.2.0\n") {
		t.Errorf("public URL not used:\n%s", s)
	}
	// Else the origin the request reached, through a trusted proxy.
	h = ScriptHandler("", "dev", secure, true)
	if s := get(h, "127.0.0.1:7788", map[string]string{"X-Forwarded-Host": "dash.example.org", "X-Forwarded-Proto": "https"}); !strings.Contains(s, "HUB=https://dash.example.org\n") {
		t.Errorf("forwarded host not used:\n%s", s)
	}
	// Plain HTTP to a public name: a script that explains, not one that fails later.
	if s := get(h, "dash.example.org", nil); !strings.Contains(s, "exit 1") || !strings.Contains(s, "HTTPS") || strings.Contains(s, "main \"$@\"") {
		t.Errorf("plain http hub got:\n%s", s)
	}
	// A Host header can't inject shell.
	if s := get(h, "x$(touch /tmp/pwned)", map[string]string{"X-Forwarded-Host": "a'b", "X-Forwarded-Proto": "https"}); strings.Contains(s, "main \"$@\"") {
		t.Errorf("odd host rendered a script:\n%s", s)
	}
	if out, err := exec.Command("sh", "-c", string(ErrorScript("it's $(broken)"))).CombinedOutput(); err == nil || !strings.Contains(string(out), "it's $(broken)") {
		t.Errorf("error script: %v %s", err, out)
	}
}

// fakeRelease serves GitHub-like release downloads of a fake dash that
// logs its arguments.
type fakeRelease struct {
	srv       *httptest.Server
	downloads atomic.Int32
	asset     string
	badSum    bool
}

const fakeDash = `#!/bin/sh
if [ "$1" = version ]; then echo "dash v1.2.3"; exit 0; fi
{ for a in "$@"; do printf '[%s]' "$a"; done; echo; } >> "$DASH_TEST_LOG"
`

func newFakeRelease(t *testing.T) *fakeRelease {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"dash": fakeDash, "README.md": "hi"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)
	f := &fakeRelease{asset: fmt.Sprintf("dash_1.2.3_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "{\n  \"url\": \"x\",\n  \"tag_name\": \"v1.2.3\",\n  \"name\": \"v1.2.3\"\n}\n")
	})
	mux.HandleFunc("GET /download/v1.2.3/"+f.asset, func(w http.ResponseWriter, r *http.Request) {
		f.downloads.Add(1)
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("GET /download/v1.2.3/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		s := hex.EncodeToString(sum[:])
		if f.badSum {
			s = strings.Repeat("0", 64)
		}
		fmt.Fprintf(w, "%s  dash_1.2.3_other_os.tar.gz\n%s  %s\n", strings.Repeat("1", 64), s, f.asset)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

type scriptRun struct {
	out  string
	err  error
	log  string // the fake dash's calls, one per line
	home string
}

func runScript(t *testing.T, f *fakeRelease, version, home string, args ...string) scriptRun {
	t.Helper()
	for _, tool := range []string{"sh", "tar", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("needs", tool)
		}
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("the installer is for Linux and macOS")
	}
	script, err := Script(ScriptParams{Hub: "http://127.0.0.1:7788", Version: version, Releases: f.srv.URL, LatestAPI: f.srv.URL + "/latest"})
	if err != nil {
		t.Fatal(err)
	}
	logf := filepath.Join(home, "calls.log")
	cmd := exec.Command("sh", append([]string{"-s", "--"}, args...)...)
	cmd.Stdin = bytes.NewReader(script)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "DASH_TEST_LOG=" + logf}
	out, err := cmd.CombinedOutput()
	log, _ := os.ReadFile(logf)
	_ = os.Remove(logf)
	return scriptRun{out: string(out), err: err, log: string(log), home: home}
}

func TestScriptInstallsAndRegisters(t *testing.T) {
	f := newFakeRelease(t)
	home := t.TempDir()
	r := runScript(t, f, "v1.2.3", home, "--code", "AB'CD $(x)", "--name", "work pc", "--projects", "/srv/my projects")
	if r.err != nil {
		t.Fatalf("install failed: %v\n%s", r.err, r.out)
	}
	bin := filepath.Join(home, ".local", "bin", "dash")
	if st, err := os.Stat(bin); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("dash not installed executable at %s: %v", bin, err)
	}
	// Every value reaches dash as one argument, unchanged.
	want := "[runner][setup][--server][http://127.0.0.1:7788][--code][AB'CD $(x)][--name][work pc][--projects][/srv/my projects]\n"
	if r.log != want {
		t.Fatalf("dash called with\n%s\nwant\n%s\noutput:\n%s", r.log, want, r.out)
	}
	if !strings.Contains(r.out, "not on your PATH") {
		t.Errorf("no PATH hint:\n%s", r.out)
	}

	// Again, to update: no code, the same version is not downloaded again,
	// and setup keeps the registration.
	r = runScript(t, f, "v1.2.3", home)
	if r.err != nil {
		t.Fatalf("re-install failed: %v\n%s", r.err, r.out)
	}
	if n := f.downloads.Load(); n != 1 {
		t.Errorf("downloaded %d times, want 1", n)
	}
	if !strings.Contains(r.out, "already installed") {
		t.Errorf("re-install output:\n%s", r.out)
	}
	if want := "[runner][setup][--server][http://127.0.0.1:7788]\n"; r.log != want {
		t.Errorf("re-install called %s, want %s", r.log, want)
	}

	// --no-service registers without a service, then runs the runner here.
	r = runScript(t, f, "v1.2.3", home, "--no-service", "--skip-checks", "--prefix", filepath.Join(home, ".local", "bin"))
	if want := "[runner][setup][--server][http://127.0.0.1:7788][--skip-checks][--service=false]\n[runner]\n"; r.err != nil || r.log != want {
		t.Errorf("--no-service called %s (%v), want %s\n%s", r.log, r.err, want, r.out)
	}
}

func TestScriptInstallsTheLatestReleaseForADevHub(t *testing.T) {
	f := newFakeRelease(t)
	home := t.TempDir()
	prefix := filepath.Join(home, "bin dir")
	r := runScript(t, f, "", home, "--code=ABCD-EFGH", "--prefix="+prefix)
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	if !strings.Contains(r.out, "Dash v1.2.3") {
		t.Errorf("output:\n%s", r.out)
	}
	if _, err := os.Stat(filepath.Join(prefix, "dash")); err != nil {
		t.Fatal(err)
	}
}

func TestScriptRefusesABadChecksum(t *testing.T) {
	f := newFakeRelease(t)
	f.badSum = true
	home := t.TempDir()
	r := runScript(t, f, "v1.2.3", home, "--code", "ABCD-EFGH")
	if r.err == nil || !strings.Contains(r.out, "checksum mismatch") {
		t.Fatalf("want a checksum failure, got %v:\n%s", r.err, r.out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "dash")); err == nil {
		t.Error("installed a binary that failed its checksum")
	}
	if r.log != "" {
		t.Errorf("ran dash: %s", r.log)
	}
}

func TestScriptFailsClearly(t *testing.T) {
	f := newFakeRelease(t)
	home := t.TempDir()
	if r := runScript(t, f, "v1.2.3", home, "--bogus"); r.err == nil || !strings.Contains(r.out, "unknown option --bogus") {
		t.Errorf("unknown option: %v\n%s", r.err, r.out)
	}
	if r := runScript(t, f, "v1.2.3", home, "--code"); r.err == nil || !strings.Contains(r.out, "--code needs a value") {
		t.Errorf("missing value: %v\n%s", r.err, r.out)
	}
	if r := runScript(t, f, "v9.9.9", home, "--code", "X"); r.err == nil || !strings.Contains(r.out, "couldn't download") {
		t.Errorf("missing release: %v\n%s", r.err, r.out)
	}
	if r := runScript(t, f, "v1.2.3", home, "--help"); r.err != nil || !strings.Contains(r.out, "--no-service") {
		t.Errorf("help: %v\n%s", r.err, r.out)
	}
}
