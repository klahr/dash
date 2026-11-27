package screenshot

import (
	"reflect"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/model"
)

func pg(path, label string) model.ScreenshotPage {
	return model.ScreenshotPage{Path: path, Label: label}
}

func TestParsePages(t *testing.T) {
	for _, c := range []struct {
		name, report string
		want         []model.ScreenshotPage
	}{
		{"none", "Added a button.", nil},
		{"dash and em dash", "Done.\n\nScreenshot pages:\n- /settings — Settings → Board\n- /board - Board\n",
			[]model.ScreenshotPage{pg("/settings", "Settings → Board"), pg("/board", "Board")}},
		{"backticks, bold heading, numbered", "**Screenshot pages:**\n\n1. `/p/x/history?tab=deploys` Deploys\n2) `/` Home",
			[]model.ScreenshotPage{pg("/p/x/history?tab=deploys", "Deploys"), pg("/", "Home")}},
		{"markdown heading, label first", "## Screenshot pages\n* Settings: `/settings#board`\n",
			[]model.ScreenshotPage{pg("/settings#board", "Settings")}},
		{"no label: the path", "Screenshot pages:\n- /about\n", []model.ScreenshotPage{pg("/about", "/about")}},
		{"the list ends at other text", "Screenshot pages:\n- /a — A\nThat's all.\n- /b — B\n", []model.ScreenshotPage{pg("/a", "A")}},
		{"bad paths skipped, repeats once", "Screenshot pages:\n- https://example.com/x — Full URL\n- //example.com — Other host\n- `/a b` — Space\n- /ok — OK\n- /ok — Again\n",
			[]model.ScreenshotPage{pg("/ok", "OK")}},
		{"the last list counts", "Screenshot pages:\n- /old — Old\n\nAfter the fix:\n\nScreenshot pages:\n- /new — New\n", []model.ScreenshotPage{pg("/new", "New")}},
		{"at most MaxListed", "Screenshot pages:\n- /1\n- /2\n- /3\n- /4\n- /5\n- /6\n",
			[]model.ScreenshotPage{pg("/1", "/1"), pg("/2", "/2"), pg("/3", "/3"), pg("/4", "/4")}},
		{"Windows line ends", "Screenshot pages:\r\n- /w — W\r\n", []model.ScreenshotPage{pg("/w", "W")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := ParsePages(c.report); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func TestCleanPathAndLabel(t *testing.T) {
	for p, ok := range map[string]bool{
		"/": true, "/a/b?c=d#e": true, " /x ": true,
		"": false, "x": false, "//host/x": false, "/\\host": false, "/a b": false, "/a\tb": false,
		"http://example.com/": false, "/" + strings.Repeat("a", maxPath): false,
	} {
		if _, got := CleanPath(p); got != ok {
			t.Errorf("CleanPath(%q) ok = %v, want %v", p, got, ok)
		}
	}
	if got := CleanLabel("  Settings \n → Board ", "/s"); got != "Settings → Board" {
		t.Errorf("label: %q", got)
	}
	if got := CleanLabel("", "/s"); got != "/s" {
		t.Errorf("empty label: %q", got)
	}
	if got := []rune(CleanLabel(strings.Repeat("é", 200), "/s")); len(got) != maxLabel || got[maxLabel-1] != '…' {
		t.Errorf("long label: %d runes", len(got))
	}
}

func TestPagesMergesFixedFirst(t *testing.T) {
	fixed := []model.ScreenshotPage{pg("/settings", "Settings"), pg("bad", "Bad"), pg("/", "")}
	listed := []model.ScreenshotPage{pg("/settings", "Listed again"), pg("/a", "A"), pg("/b", "B"), pg("/c", "C"), pg("/d", "D"), pg("/e", "E")}
	got := Pages(fixed, listed)
	want := []model.ScreenshotPage{pg("/settings", "Settings"), pg("/", "/"), pg("/a", "A"), pg("/b", "B"), pg("/c", "C"), pg("/d", "D")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestTouchesUI(t *testing.T) {
	changed := []string{"internal/api/x.go", "web/static/app/board.js"}
	for _, c := range []struct {
		ui   []string
		want bool
	}{
		{nil, true}, // no UI paths: any change
		{[]string{"web/"}, true},
		{[]string{"./web"}, true},
		{[]string{"/web/static/"}, true},
		{[]string{"web/static/app/board.js"}, true},
		{[]string{"web/static/app/board"}, false}, // a prefix of a name isn't a folder
		{[]string{"docs/", "cmd"}, false},
		{[]string{"web/*/app"}, true},
		{[]string{"src/*/views"}, false},
		{[]string{" ", "."}, true}, // nothing usable: any change
	} {
		if got := TouchesUI(changed, c.ui); got != c.want {
			t.Errorf("TouchesUI(%v) = %v, want %v", c.ui, got, c.want)
		}
	}
	if TouchesUI(nil, nil) {
		t.Error("no change touches the UI")
	}
}

// Decide: when screenshots are taken, and why not.
func TestDecide(t *testing.T) {
	web := model.Project{RunCommand: "npm run dev", Screenshots: &model.ScreenshotSettings{On: true, UIPaths: []string{"web/"}}}
	ui := []string{"web/app.js"}
	listed := []model.ScreenshotPage{pg("/settings", "Settings")}
	with := func(f func(*model.Project)) model.Project {
		p := web
		s := *web.Screenshots
		p.Screenshots = &s
		f(&p)
		return p
	}
	for _, c := range []struct {
		name    string
		p       model.Project
		changed []string
		listed  []model.ScreenshotPage
		chrome  string
		run     bool
		note    string // part of the note; "" for none
	}{
		{"runs", web, ui, listed, "/chrome", true, ""},
		{"off by default", with(func(p *model.Project) { p.Screenshots = nil }), ui, listed, "/chrome", false, ""},
		{"turned off", with(func(p *model.Project) { p.Screenshots.On = false }), ui, listed, "/chrome", false, ""},
		{"no run command", with(func(p *model.Project) { p.RunCommand = "" }), ui, listed, "/chrome", false, ""},
		{"run command held on this runner", with(func(p *model.Project) {
			p.RunCommand = ""
			p.HeldCommands = map[string]string{model.CmdRun: "the run command waits for approval"}
		}), ui, listed, "/chrome", false, "waits for approval"},
		{"a terminal preview", with(func(p *model.Project) { p.PreviewKind = model.PreviewTerminal }), ui, listed, "/chrome", false, ""},
		{"no UI file changed", web, []string{"README.md"}, listed, "/chrome", false, ""},
		{"nothing changed", web, nil, listed, "/chrome", false, ""},
		{"no pages", web, ui, nil, "/chrome", false, "listed no pages"},
		{"the project's own pages", with(func(p *model.Project) { p.Screenshots.Pages = listed }), ui, nil, "/chrome", true, ""},
		{"no Chrome", web, ui, listed, "", false, "no Chrome"},
		{"setup command held", with(func(p *model.Project) {
			p.HeldCommands = map[string]string{model.CmdScreenshot: "the screenshot command waits for approval"}
		}), ui, listed, "/chrome", false, "screenshot command waits"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := Decide(c.p, c.changed, c.listed, c.chrome)
			if d.Run != c.run || (c.note == "") != (d.Note == "") || !strings.Contains(d.Note, c.note) {
				t.Fatalf("got run=%v note=%q, want run=%v note containing %q", d.Run, d.Note, c.run, c.note)
			}
			if d.Run && len(d.Pages) == 0 {
				t.Fatal("runs with no pages")
			}
		})
	}
}

func TestNamesAndCookies(t *testing.T) {
	n := shotName(3, 1, "wide", "after")
	if n != "r3-p1-wide-after.webp" || !ValidName(n) {
		t.Fatalf("name %q", n)
	}
	for _, bad := range []string{"", "../r3-p1-wide-after.webp", "r3-p1-wide-after.png", "r3-p1-big-after.webp", "x/r3-p1-wide-after.webp"} {
		if ValidName(bad) {
			t.Errorf("ValidName(%q)", bad)
		}
	}
	got := parseCookies([]byte("seeding…\nSet-Cookie: session=abc123; Path=/\nset-cookie: theme=dark\nnot a cookie\n"))
	want := [][2]string{{"session", "abc123"}, {"theme", "dark"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cookies %v, want %v", got, want)
	}
}
