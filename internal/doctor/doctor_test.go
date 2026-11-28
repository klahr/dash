package doctor

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCommandNames(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		want []string
	}{
		{"go test ./...", []string{"go"}},
		{"./scripts/check.sh", nil},
		{"make test && npm run lint", []string{"make", "npm"}},
		{"cd web && npm ci; npx playwright test", []string{"npm", "npx"}},
		{"CGO_ENABLED=0 go build ./... | tee out.log", []string{"go", "tee"}},
		{"env -i FOO=1 cargo test", []string{"cargo"}},
		{"echo hi; export X=1; source .env; . venv/bin/activate; pytest", []string{"pytest"}},
		{"(cd api && go test ./...) || exit 1", []string{"go"}},
		{"if [ -f x ]; then make; fi", []string{"make"}},
		{"$GO test ./...", nil},
		{"/usr/local/bin/node server.js", nil},
		{"bin/run", nil},
		{"npm run dev # serves on $PORT\n# just a comment\nyarn build", []string{"npm", "yarn"}},
		{"echo 'a && b; c' && go vet ./...", []string{"go"}},
		{"go test \\\n  ./...", []string{"go"}},
		{"python3 -m pytest && python3 x.py", []string{"python3"}},
		{"", nil},
		{"sudo -E docker compose up", []string{"docker"}},
		{"nice -n 5 make", []string{"make"}},
	} {
		if got := CommandNames(c.cmd); !slices.Equal(got, c.want) {
			t.Errorf("CommandNames(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

func TestParseOSRelease(t *testing.T) {
	for text, want := range map[string]string{
		"NAME=\"Ubuntu\"\nID=ubuntu\nID_LIKE=debian\n":   FamilyDebian,
		"ID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\n": FamilyFedora,
		"ID=arch\n":   FamilyArch,
		"ID=alpine\n": FamilyAlpine,
		"ID=\"opensuse-tumbleweed\"\nID_LIKE=\"opensuse\"": FamilySUSE,
		"ID=nixos\n": "",
		"":           "",
	} {
		if got := ParseOSRelease(text); got != want {
			t.Errorf("ParseOSRelease(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestInstall(t *testing.T) {
	mac, deb, fed, win := System{GOOS: "darwin"}, System{GOOS: "linux", Family: FamilyDebian}, System{GOOS: "linux", Family: FamilyFedora}, System{GOOS: "windows"}
	for _, c := range []struct {
		tool string
		sys  System
		want string
	}{
		{"git", mac, "xcode-select --install"},
		{"git", deb, "sudo apt install -y git"},
		{"git", win, "winget install Git.Git"},
		{"gh", mac, "brew install gh"},
		{"gh", fed, "sudo dnf install -y gh"},
		{"gh", System{GOOS: "linux", Family: FamilyArch}, "sudo pacman -S --needed github-cli"},
		{"chrome", mac, "brew install --cask google-chrome"},
		{"chrome", deb, "sudo apt install -y chromium"},
		{"go", deb, "sudo apt install -y golang-go"},
		{"claude", deb, "curl -fsSL https://claude.ai/install.sh | bash"},
		{"claude", win, "irm https://claude.ai/install.ps1 | iex"},
		{"make", mac, "xcode-select --install"},
		{"ripgrep", deb, "sudo apt install -y ripgrep"}, // unknown: the tool's own name
		{"ripgrep", win, "winget search ripgrep"},
		{"git", System{GOOS: "linux"}, ""}, // unknown distribution: no guess
		{"gcc", win, ""},
	} {
		if got := Install(c.tool, c.sys); got != c.want {
			t.Errorf("Install(%q, %s) = %q, want %q", c.tool, c.sys.Name(), got, c.want)
		}
	}
	if n := deb.Name(); n != "Linux (Debian/Ubuntu)" {
		t.Errorf("Name = %q", n)
	}
}

func TestReport(t *testing.T) {
	var none *Report
	if none.ProjectMissing("p") != nil {
		t.Error("a nil report should miss nothing")
	}
	if _, known := none.Has(CheckChrome); known {
		t.Error("a nil report knows nothing")
	}
	r := &Report{Checks: []Check{
		{Group: GroupRequired, Name: CheckGit, Status: OK},
		{Group: GroupFeatures, Name: CheckChrome, Status: Warning},
		{Group: GroupFeatures, Name: CheckGH, Status: OK},
		{Group: GroupProject, Name: "go", Project: "p1", Status: Warning},
		{Group: GroupProject, Name: "node", Project: "p1", Status: OK},
		{Group: GroupProject, Name: "gh", Project: "p2", Status: Warning},
	}}
	if has, known := r.Has(CheckChrome); has || !known {
		t.Errorf("Has(chrome) = %v %v", has, known)
	}
	if has, _ := r.Has(CheckGH); !has {
		t.Error("gh should be had (a project's gh check doesn't count)")
	}
	if got := r.ProjectMissing("p1"); !slices.Equal(got, []string{"go"}) {
		t.Errorf("ProjectMissing(p1) = %q", got)
	}
}

func TestClean(t *testing.T) {
	at := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	in := Report{At: at, System: "macOS", Checks: []Check{
		{Group: GroupRequired, Name: " git ", Status: OK, Project: "dropped"},
		{Group: "other", Name: "x", Status: OK},
		{Group: GroupFeatures, Name: "y", Status: "fine"},
		{Group: GroupFeatures, Name: "", Status: OK},
		{Group: GroupProject, Name: "go", Project: "p", ProjectName: "P", Status: Warning, Message: strings.Repeat("m", 2000), Fix: strings.Repeat("f", 2000)},
	}}
	for range 200 {
		in.Checks = append(in.Checks, Check{Group: GroupProject, Name: "n", Project: "p", Status: OK})
	}
	out := Clean(in)
	if !out.At.Equal(at) || out.System != "macOS" || len(out.Checks) != maxChecks {
		t.Fatalf("Clean: %v %q %d checks", out.At, out.System, len(out.Checks))
	}
	if c := out.Checks[0]; c.Name != "git" || c.Project != "" {
		t.Errorf("first check = %+v", c)
	}
	if c := out.Checks[1]; c.Name != "go" || len(c.Message) > maxMessage+4 || len(c.Fix) > maxFix+4 || c.ProjectName != "P" {
		t.Errorf("second check = %q %d %d", c.Name, len(c.Message), len(c.Fix))
	}
}
