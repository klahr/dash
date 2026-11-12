package runnerctl

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// Windows releases are .zip archives holding dash.exe.
func TestWindowsReleaseAsset(t *testing.T) {
	if got := Asset("v1.2.0", "windows", "amd64"); got != "dash_1.2.0_windows_amd64.zip" {
		t.Errorf("windows asset: %s", got)
	}
	if got := Asset("1.2.0", "linux", "arm64"); got != "dash_1.2.0_linux_arm64.tar.gz" {
		t.Errorf("linux asset: %s", got)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"README.md": "hi", "dash-tui.exe": "tui", "dash.exe": "MZ dash"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := extractDash(buf.Bytes(), "windows", &out); err != nil || out.String() != "MZ dash" {
		t.Fatalf("extract: %q %v", out.String(), err)
	}
	if err := extractDash(buf.Bytes(), "linux", &out); err == nil {
		t.Error("a zip read as a .tar.gz")
	}
	empty := new(bytes.Buffer)
	zw = zip.NewWriter(empty)
	_ = zw.Close()
	if err := extractDash(empty.Bytes(), "windows", &out); err == nil || !strings.Contains(err.Error(), "dash.exe") {
		t.Errorf("no dash.exe: %v", err)
	}
}
