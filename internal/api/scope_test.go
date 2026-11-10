package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The API reaches records only through an org's view of the store (Data,
// Store.ForOrg). Server.Store's type enforces that for the store itself;
// this keeps handlers from going around it through the engines' stores or
// by naming the whole store's type.
func TestAPIReachesRecordsOnlyPerOrg(t *testing.T) {
	parallel(t)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	unscoped := regexp.MustCompile(`\*store\.Store\b|\.(Todos|Merges|Scans|Scheduler|CI|Previews)\.(Store|Todos)\b`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if unscoped.MatchString(line) {
				t.Errorf("%s:%d reaches the whole store: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
