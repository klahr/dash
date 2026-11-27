package doctor

import "strings"

// System is the runner's operating system, as far as fix commands go.
type System struct {
	GOOS string // runtime.GOOS
	// Family is a Linux distribution's family, which says its package
	// manager: "debian", "fedora", "arch", "alpine", "suse" ("": unknown).
	Family string
}

// Linux distribution families, by package manager.
const (
	FamilyDebian = "debian"
	FamilyFedora = "fedora"
	FamilyArch   = "arch"
	FamilyAlpine = "alpine"
	FamilySUSE   = "suse"
)

// ParseOSRelease is the family of the Linux distribution /etc/os-release
// (its text) describes ("": none known).
func ParseOSRelease(text string) string {
	var ids []string
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || (k != "ID" && k != "ID_LIKE") {
			continue
		}
		ids = append(ids, strings.Fields(strings.ToLower(strings.Trim(v, `"'`)))...)
	}
	families := map[string]string{
		"debian": FamilyDebian, "ubuntu": FamilyDebian, "linuxmint": FamilyDebian, "pop": FamilyDebian, "raspbian": FamilyDebian,
		"fedora": FamilyFedora, "rhel": FamilyFedora, "centos": FamilyFedora, "rocky": FamilyFedora, "almalinux": FamilyFedora, "amzn": FamilyFedora,
		"arch": FamilyArch, "manjaro": FamilyArch, "endeavouros": FamilyArch,
		"alpine": FamilyAlpine,
		"suse":   FamilySUSE, "opensuse": FamilySUSE, "opensuse-leap": FamilySUSE, "opensuse-tumbleweed": FamilySUSE, "sles": FamilySUSE,
	}
	for _, id := range ids {
		if f := families[id]; f != "" {
			return f
		}
	}
	return ""
}

// Name is the system for people: "macOS", "Linux (Debian/Ubuntu)", …
func (s System) Name() string {
	switch s.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "linux":
		if n := map[string]string{FamilyDebian: "Debian/Ubuntu", FamilyFedora: "Fedora/RHEL", FamilyArch: "Arch",
			FamilyAlpine: "Alpine", FamilySUSE: "openSUSE"}[s.Family]; n != "" {
			return "Linux (" + n + ")"
		}
		return "Linux"
	}
	return s.GOOS
}

// pkg is a tool's package name per package manager ("" in a field: the
// tool's own name; "-": none there).
type pkg struct {
	brew, apt, dnf, pacman, apk, zypper, winget string
	// any is one command that installs it on every system but Windows
	// (an installer script), and anyWin Windows's; they win over the
	// package managers.
	any, anyWin string
}

var packages = map[string]pkg{
	"git":      {brew: "@xcode", winget: "Git.Git"},
	"gh":       {pacman: "github-cli", apk: "github-cli", winget: "GitHub.cli"},
	"chrome":   {brew: "--cask google-chrome", apt: "chromium", dnf: "chromium", pacman: "chromium", apk: "chromium", zypper: "chromium", winget: "Google.Chrome"},
	"claude":   {any: "curl -fsSL https://claude.ai/install.sh | bash", anyWin: "irm https://claude.ai/install.ps1 | iex"},
	"go":       {apt: "golang-go", dnf: "golang", winget: "GoLang.Go"},
	"gofmt":    {brew: "go", apt: "golang-go", dnf: "golang", pacman: "go", apk: "go", zypper: "go", winget: "GoLang.Go"},
	"node":     {apt: "nodejs", dnf: "nodejs", pacman: "nodejs", apk: "nodejs", zypper: "nodejs", winget: "OpenJS.NodeJS.LTS"},
	"npm":      {brew: "node", apt: "npm", dnf: "nodejs", pacman: "npm", apk: "npm", zypper: "npm", winget: "OpenJS.NodeJS.LTS"},
	"npx":      {brew: "node", apt: "npm", dnf: "nodejs", pacman: "npm", apk: "npm", zypper: "npm", winget: "OpenJS.NodeJS.LTS"},
	"yarn":     {any: "npm install -g yarn", anyWin: "npm install -g yarn"},
	"pnpm":     {any: "npm install -g pnpm", anyWin: "npm install -g pnpm"},
	"bun":      {any: "curl -fsSL https://bun.sh/install | bash", anyWin: "powershell -c \"irm bun.sh/install.ps1 | iex\""},
	"deno":     {any: "curl -fsSL https://deno.land/install.sh | sh", anyWin: "winget install DenoLand.Deno"},
	"python3":  {brew: "python", apt: "python3", dnf: "python3", pacman: "python", apk: "python3", zypper: "python3", winget: "Python.Python.3.12"},
	"python":   {apt: "python-is-python3", dnf: "python3", apk: "python3", zypper: "python3", winget: "Python.Python.3.12"},
	"pip3":     {brew: "python", apt: "python3-pip", dnf: "python3-pip", pacman: "python-pip", apk: "py3-pip", zypper: "python3-pip", winget: "Python.Python.3.12"},
	"pip":      {brew: "python", apt: "python3-pip", dnf: "python3-pip", pacman: "python-pip", apk: "py3-pip", zypper: "python3-pip", winget: "Python.Python.3.12"},
	"uv":       {any: "curl -LsSf https://astral.sh/uv/install.sh | sh", anyWin: "powershell -c \"irm https://astral.sh/uv/install.ps1 | iex\""},
	"cargo":    {any: "curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh", anyWin: "winget install Rustlang.Rustup"},
	"rustc":    {any: "curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh", anyWin: "winget install Rustlang.Rustup"},
	"make":     {brew: "@xcode", apt: "build-essential", winget: "ezwinports.make"},
	"gcc":      {brew: "@xcode", apt: "build-essential", winget: "-"},
	"cmake":    {winget: "Kitware.CMake"},
	"docker":   {brew: "--cask docker", apt: "docker.io", dnf: "docker", winget: "Docker.DockerDesktop"},
	"java":     {brew: "openjdk", apt: "default-jdk", dnf: "java-latest-openjdk", pacman: "jdk-openjdk", apk: "openjdk21", zypper: "java-21-openjdk", winget: "Microsoft.OpenJDK.21"},
	"mvn":      {brew: "maven", apt: "maven", dnf: "maven", pacman: "maven", apk: "maven", zypper: "maven", winget: "-"},
	"gradle":   {winget: "-"},
	"dotnet":   {brew: "--cask dotnet-sdk", apt: "dotnet-sdk-8.0", dnf: "dotnet-sdk-8.0", pacman: "dotnet-sdk", apk: "dotnet8-sdk", zypper: "-", winget: "Microsoft.DotNet.SDK.8"},
	"ruby":     {winget: "RubyInstallerTeam.Ruby.3.2"},
	"bundle":   {brew: "ruby", apt: "ruby-bundler", dnf: "rubygem-bundler", pacman: "ruby-bundler", apk: "ruby-bundler", zypper: "ruby", winget: "RubyInstallerTeam.Ruby.3.2"},
	"php":      {winget: "-"},
	"composer": {winget: "-"},
	"jq":       {winget: "jqlang.jq"},
	"curl":     {winget: "cURL.cURL"},
}

// Install is a command that installs tool on s ("" when there is none to
// suggest). It is shown to copy: the doctor never runs it.
func Install(tool string, s System) string {
	p := packages[tool]
	if s.GOOS == "windows" {
		switch {
		case p.anyWin != "":
			return p.anyWin
		case p.winget == "-":
			return ""
		case p.winget != "":
			return "winget install " + p.winget
		}
		return "winget search " + tool
	}
	if p.any != "" {
		return p.any
	}
	name := func(n string) string {
		if n == "" {
			return tool
		}
		return n
	}
	switch s.GOOS {
	case "darwin":
		switch p.brew {
		case "-":
			return ""
		case "@xcode":
			return "xcode-select --install"
		}
		return "brew install " + name(p.brew)
	case "linux":
		manager := map[string]struct{ cmd, pkg string }{
			FamilyDebian: {"sudo apt install -y", p.apt},
			FamilyFedora: {"sudo dnf install -y", p.dnf},
			FamilyArch:   {"sudo pacman -S --needed", p.pacman},
			FamilyAlpine: {"sudo apk add", p.apk},
			FamilySUSE:   {"sudo zypper install -y", p.zypper},
		}[s.Family]
		if manager.cmd == "" || manager.pkg == "-" {
			return ""
		}
		return manager.cmd + " " + name(manager.pkg)
	}
	return ""
}
