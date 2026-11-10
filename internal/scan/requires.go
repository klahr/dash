package scan

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
)

// globRE compiles a requires_files glob, matched against a file's path
// relative to the repository root, with '/' separators:
//
//   - '*' matches within one path segment, '?' one character of one, and
//     "**" any number of segments ("**/" also none: "**/*.vue" matches
//     "App.vue" and "src/App.vue");
//   - "{a,b}" matches either;
//   - a glob with no '/' matches a file's name in any directory, like a
//     .gitignore line: "*.tsx" is "**/*.tsx".
func globRE(glob string) (*regexp.Regexp, error) {
	g := strings.TrimPrefix(strings.TrimSpace(glob), "/")
	if g == "" {
		return nil, errors.New("empty glob")
	}
	if !strings.Contains(g, "/") {
		g = "**/" + g
	}
	var b strings.Builder
	b.WriteString("^")
	inBrace := false
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			if strings.HasPrefix(g[i:], "**/") {
				b.WriteString("(?:.*/)?")
				i += 2
			} else if strings.HasPrefix(g[i:], "**") {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '{':
			if inBrace {
				return nil, fmt.Errorf("glob %q: nested {", glob)
			}
			inBrace = true
			b.WriteString("(?:")
		case '}':
			if !inBrace {
				return nil, fmt.Errorf("glob %q: } without {", glob)
			}
			inBrace = false
			b.WriteString(")")
		case ',':
			if inBrace {
				b.WriteString("|")
			} else {
				b.WriteString(",")
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	if inBrace {
		return nil, fmt.Errorf("glob %q: { not closed", glob)
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// skipDirs are directories requiresMet doesn't look in: not the project's
// own files, or not worth the walk.
var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true}

// requiresMet says whether dir (a checkout) has a file matching one of t's
// RequiresFiles, under t.Path when it has one; true when it requires none.
func requiresMet(t Template, dir string) (bool, error) {
	if len(t.RequiresFiles) == 0 {
		return true, nil
	}
	res := make([]*regexp.Regexp, 0, len(t.RequiresFiles))
	for _, g := range t.RequiresFiles {
		re, err := globRE(g)
		if err != nil {
			return false, fmt.Errorf("requires_files: %w", err)
		}
		res = append(res, re)
	}
	root := dir
	if t.Path != "" {
		root = filepath.Join(dir, filepath.FromSlash(t.Path))
	}
	errFound := errors.New("found")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll // the path isn't in this commit: nothing matches
			}
			return err
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, re := range res {
			if re.MatchString(rel) {
				return errFound
			}
		}
		return nil
	})
	if errors.Is(err, errFound) {
		return true, nil
	}
	return false, err
}
