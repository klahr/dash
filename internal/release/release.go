// Package release names release tags in a project's tag format and works out
// the next version from the tags a repository already has.
//
// A format is literal text around three placeholders: X (major), Y (minor)
// and Z (patch), each exactly once and in that order, e.g. "X.Y.Z",
// "vX.Y.Z", "release-X.Y.Z" or "app/X.Y.Z".
package release

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// DefaultFormat is the tag format of a project that hasn't set one.
const DefaultFormat = "X.Y.Z"

// Version is a semantic version without pre-release or build parts.
type Version struct {
	Major, Minor, Patch int
}

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// Less orders versions by semver precedence.
func (v Version) Less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// Bump kinds.
const (
	Patch = "patch"
	Minor = "minor"
	Major = "major"
)

// Bump returns the next version of the given kind (patch, minor or major).
func (v Version) Bump(kind string) (Version, error) {
	switch kind {
	case Patch:
		return Version{v.Major, v.Minor, v.Patch + 1}, nil
	case Minor:
		return Version{v.Major, v.Minor + 1, 0}, nil
	case Major:
		return Version{v.Major + 1, 0, 0}, nil
	}
	return v, fmt.Errorf("bump must be %q, %q or %q", Patch, Minor, Major)
}

// First is the version suggested when a repository has no release tags yet.
var First = Version{0, 1, 0}

var versionRe = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`)

// ParseVersion reads "1.2.3" (a leading "v" is allowed).
func ParseVersion(s string) (Version, error) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, fmt.Errorf("%q isn't a version like 1.2.3", s)
	}
	return Version{atoi(m[1]), atoi(m[2]), atoi(m[3])}, nil
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1 // too large; callers reject it
	}
	return n
}

// Format is a parsed tag format.
type Format struct {
	raw                       string
	prefix, sep1, sep2, suffx string
	re                        *regexp.Regexp
}

// ParseFormat checks a tag format and parses it; "" is DefaultFormat.
func ParseFormat(s string) (Format, error) {
	if s == "" {
		s = DefaultFormat
	}
	for _, p := range []string{"X", "Y", "Z"} {
		if n := strings.Count(s, p); n != 1 {
			return Format{}, fmt.Errorf("the tag format must contain %s exactly once (X = major, Y = minor, Z = patch)", p)
		}
	}
	x, y, z := strings.Index(s, "X"), strings.Index(s, "Y"), strings.Index(s, "Z")
	if !(x < y && y < z) {
		return Format{}, errors.New("the tag format must have X, Y and Z in that order, like X.Y.Z")
	}
	f := Format{raw: s, prefix: s[:x], sep1: s[x+1 : y], sep2: s[y+1 : z], suffx: s[z+1:]}
	if f.sep1 == "" || f.sep2 == "" {
		return Format{}, errors.New("the tag format needs something between X, Y and Z, like X.Y.Z")
	}
	digit := func(s string) bool { return strings.ContainsAny(s, "0123456789") }
	if digit(f.sep1) || digit(f.sep2) || endsInDigit(f.prefix) || startsWithDigit(f.suffx) {
		return Format{}, errors.New("the tag format can't have a digit next to X, Y or Z or between them")
	}
	if err := CheckRefName(f.Render(Version{1, 2, 3})); err != nil {
		return Format{}, fmt.Errorf("the tag format doesn't make a valid git tag name: %w", err)
	}
	f.re = regexp.MustCompile("^" + regexp.QuoteMeta(f.prefix) + `(0|[1-9]\d*)` + regexp.QuoteMeta(f.sep1) +
		`(0|[1-9]\d*)` + regexp.QuoteMeta(f.sep2) + `(0|[1-9]\d*)` + regexp.QuoteMeta(f.suffx) + "$")
	return f, nil
}

func endsInDigit(s string) bool     { return s != "" && s[len(s)-1] >= '0' && s[len(s)-1] <= '9' }
func startsWithDigit(s string) bool { return s != "" && s[0] >= '0' && s[0] <= '9' }

// String is the format as written.
func (f Format) String() string { return f.raw }

// Render names the tag of version v.
func (f Format) Render(v Version) string {
	return f.prefix + strconv.Itoa(v.Major) + f.sep1 + strconv.Itoa(v.Minor) + f.sep2 + strconv.Itoa(v.Patch) + f.suffx
}

// Parse reads the version out of a tag in this format; ok is false for tags
// in another format.
func (f Format) Parse(tag string) (v Version, ok bool) {
	m := f.re.FindStringSubmatch(tag)
	if m == nil {
		return v, false
	}
	v = Version{atoi(m[1]), atoi(m[2]), atoi(m[3])}
	return v, v.Major >= 0 && v.Minor >= 0 && v.Patch >= 0
}

// Tag is a release tag and its version.
type Tag struct {
	Name    string  `json:"name"`
	Version Version `json:"-"`
}

// Latest is the tag with the highest version among tags in this format
// (others are ignored); ok is false when there is none.
func (f Format) Latest(tags []string) (latest Tag, ok bool) {
	for _, t := range tags {
		v, match := f.Parse(t)
		if match && (!ok || latest.Version.Less(v)) {
			latest, ok = Tag{t, v}, true
		}
	}
	return latest, ok
}

// Previous is the highest-versioned tag in this format among tags that is
// lower than v (the release before v).
func (f Format) Previous(tags []string, v Version) (prev Tag, ok bool) {
	for _, t := range tags {
		tv, match := f.Parse(t)
		if match && tv.Less(v) && (!ok || prev.Version.Less(tv)) {
			prev, ok = Tag{t, tv}, true
		}
	}
	return prev, ok
}

// Suggestions are the next tags to offer: the next patch, minor and major
// release after the latest tag, or First for a repository without one.
type Suggestions struct {
	Latest string `json:"latest,omitempty"` // "" when there is no release tag yet
	Patch  string `json:"patch,omitempty"`
	Minor  string `json:"minor,omitempty"`
	Major  string `json:"major,omitempty"`
	First  string `json:"first,omitempty"` // set instead of the bumps when there is no tag yet
}

// Suggest works out the Suggestions from a repository's tags.
func (f Format) Suggest(tags []string) Suggestions {
	latest, ok := f.Latest(tags)
	if !ok {
		return Suggestions{First: f.Render(First)}
	}
	bump := func(kind string) string { v, _ := latest.Version.Bump(kind); return f.Render(v) }
	return Suggestions{Latest: latest.Name, Patch: bump(Patch), Minor: bump(Minor), Major: bump(Major)}
}

// Resolve picks the tag to create: an explicit version ("1.4.0" or a tag
// in the format), or a bump of the latest tag (the first release when
// there is none).
func (f Format) Resolve(tags []string, version, bump string) (Tag, error) {
	version = strings.TrimSpace(version)
	switch {
	case version != "" && bump != "":
		return Tag{}, errors.New("give a version or a bump, not both")
	case version != "":
		if v, ok := f.Parse(version); ok {
			return Tag{version, v}, nil
		}
		if v, err := ParseVersion(version); err == nil {
			return Tag{f.Render(v), v}, nil
		}
		return Tag{}, fmt.Errorf("%q doesn't match the tag format %s (or a version like 1.2.3)", version, f.raw)
	case bump == "":
		return Tag{}, errors.New("give a version or a bump (patch, minor or major)")
	}
	latest, ok := f.Latest(tags)
	if !ok {
		if _, err := First.Bump(bump); err != nil {
			return Tag{}, err
		}
		return Tag{f.Render(First), First}, nil
	}
	v, err := latest.Version.Bump(bump)
	if err != nil {
		return Tag{}, err
	}
	return Tag{f.Render(v), v}, nil
}

// SortTags orders the tags in this format by version, highest first.
func (f Format) SortTags(tags []string) []Tag {
	var out []Tag
	for _, t := range tags {
		if v, ok := f.Parse(t); ok {
			out = append(out, Tag{t, v})
		}
	}
	slices.SortFunc(out, func(a, b Tag) int {
		switch {
		case a.Version.Less(b.Version):
			return 1
		case b.Version.Less(a.Version):
			return -1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// CheckRefName applies git's rules for a ref name (git check-ref-format) to
// the tag name, as refs/tags/<name>.
func CheckRefName(name string) error {
	bad := func(why string) error { return fmt.Errorf("%q %s", name, why) }
	switch {
	case name == "":
		return errors.New("the tag name is empty")
	case name == "@":
		return bad("is not allowed")
	case strings.HasPrefix(name, "-"):
		return bad("can't start with -")
	case strings.HasSuffix(name, "/") || strings.HasPrefix(name, "/"):
		return bad("can't start or end with /")
	case strings.HasSuffix(name, "."):
		return bad("can't end with .")
	case strings.Contains(name, ".."):
		return bad("can't contain ..")
	case strings.Contains(name, "//"):
		return bad("can't contain //")
	case strings.Contains(name, "@{"):
		return bad("can't contain @{")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return bad(fmt.Sprintf("can't contain %q", r))
		}
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return bad("can't have a part starting with .")
		}
		if strings.HasSuffix(part, ".lock") {
			return bad("can't have a part ending in .lock")
		}
	}
	return nil
}
