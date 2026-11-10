package web

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// The web client is app.js and its modules in static/app/. Each module is
// imported by app.js, so it runs at startup in a known order, and is in the
// service worker's SHELL, so Dash opens offline. Every relative import
// names a file that exists.
func TestAppModules(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		b, err := fs.ReadFile(Static, name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	mods, err := fs.Glob(Static, "app/*.js")
	if err != nil || len(mods) == 0 {
		t.Fatalf("no modules in static/app: %v", err)
	}
	entry, sw := read("app.js"), read("sw.js")
	for _, m := range mods {
		if !strings.Contains(entry, `"./`+m+`"`) {
			t.Errorf("app.js doesn't import ./%s", m)
		}
		if !strings.Contains(sw, `"/`+m+`"`) {
			t.Errorf("sw.js SHELL is missing /%s", m)
		}
	}
	imp := regexp.MustCompile(`(?m)^import (?:[^"]* from )?"(\.[^"]+)";`)
	for _, f := range append([]string{"app.js"}, mods...) {
		for _, m := range imp.FindAllStringSubmatch(read(f), -1) {
			target := path.Join(path.Dir(f), m[1])
			if _, err := fs.Stat(Static, target); err != nil {
				t.Errorf("%s imports %s, which doesn't exist", f, m[1])
			}
		}
	}
}
