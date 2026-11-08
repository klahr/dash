package api

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/klahr/dash/internal/model"
)

// A project added from a repository that already has content (an existing
// clone, or one cloned from GitHub) gets its site's favicon as its icon, when
// the repository has one, so it doesn't start out as a bare initial.

const (
	faviconDepth   = 4       // folders below the root to look in (apps/web/public/favicon.svg)
	faviconEntries = 20000   // entries to look at, at most, in a big repository
	maxFavicon     = 1 << 20 // bytes; a favicon is small
)

// faviconSkip are folders that hold other people's code or build output.
var faviconSkip = map[string]bool{
	"node_modules": true, "vendor": true, "third_party": true, "bower_components": true,
	"testdata": true, "target": true, "Pods": true,
}

// faviconRank orders the file names worth taking, best first; -1 is none.
// Vector first, then PNGs, then an .ico (only usable with a PNG inside).
func faviconRank(name string) int {
	name = strings.ToLower(name)
	switch {
	case name == "favicon.svg":
		return 0
	case name == "favicon.png":
		return 1
	case name == "apple-touch-icon.png":
		return 2
	case strings.HasPrefix(name, "favicon") && path.Ext(name) == ".png":
		return 3 // favicon-32x32.png
	case name == "favicon.ico":
		return 4
	}
	return -1
}

type faviconCandidate struct {
	path        string
	rank, depth int
	size        int64
}

// findFavicon is the best favicon in the repository at root, as a file name
// and type we accept as an upload and its contents, or ok false. It never
// follows a symlink nor reads past maxFavicon.
func findFavicon(root string) (name, typ string, data []byte, ok bool) {
	var found []faviconCandidate
	seen := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if seen++; seen > faviconEntries {
			return filepath.SkipAll
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			return nil
		}
		depth := strings.Count(filepath.ToSlash(rel), "/")
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || faviconSkip[d.Name()] || depth >= faviconDepth {
				return filepath.SkipDir
			}
			return nil
		}
		rank := faviconRank(d.Name())
		if rank < 0 || !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() == 0 || info.Size() > maxFavicon {
			return nil
		}
		found = append(found, faviconCandidate{p, rank, depth, info.Size()})
		return nil
	})
	sort.Slice(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.depth != b.depth {
			return a.depth < b.depth
		}
		if a.size != b.size {
			return a.size > b.size // the bigger of favicon-16x16 and favicon-32x32
		}
		return a.path < b.path
	})
	for _, c := range found {
		b, err := readSmall(c.path, maxFavicon)
		if err != nil {
			continue
		}
		switch {
		case strings.EqualFold(filepath.Ext(c.path), ".svg"):
			if bytes.Contains(b, []byte("<svg")) {
				return "favicon.svg", "image/svg+xml", b, true
			}
		case strings.EqualFold(filepath.Ext(c.path), ".ico"):
			if png := icoPNG(b); png != nil {
				return "favicon.png", "image/png", png, true
			}
		default:
			if bytes.HasPrefix(b, pngMagic) {
				return "favicon.png", "image/png", b, true
			}
		}
	}
	return "", "", nil, false
}

// readSmall reads the regular file at p if it's at most limit bytes.
func readSmall(p string, limit int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = errors.New("too big")
	}
	return b, err
}

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// icoPNG is the biggest PNG image stored in an .ico file, or nil (the older
// bitmap-only icons have none).
func icoPNG(b []byte) []byte {
	if len(b) < 6 || binary.LittleEndian.Uint16(b[0:]) != 0 || binary.LittleEndian.Uint16(b[2:]) != 1 {
		return nil
	}
	var best []byte
	for i, n := 0, int(binary.LittleEndian.Uint16(b[4:])); i < n; i++ {
		e := 6 + 16*i
		if e+16 > len(b) {
			break
		}
		size := int(binary.LittleEndian.Uint32(b[e+8:]))
		off := int(binary.LittleEndian.Uint32(b[e+12:]))
		if off < 0 || size <= 0 || off > len(b) || size > len(b)-off {
			continue
		}
		if img := b[off : off+size]; bytes.HasPrefix(img, pngMagic) && len(img) > len(best) {
			best = img
		}
	}
	return best
}

// adoptFavicon gives p, a project just made from a repository with content,
// the repository's favicon as its icon when it has one and no icon yet. It
// is best effort: on any failure p stays as it was.
func (sv *service) adoptFavicon(p model.Project) model.Project {
	if p.Icon != "" || sv.s.UploadRoot == "" {
		return p
	}
	name, typ, data, ok := findFavicon(p.Path)
	if !ok {
		return p
	}
	a, err := sv.saveUpload(name, typ, bytes.NewReader(data))
	if err != nil {
		slog.Warn("project favicon", "project", p.Name, "err", err)
		return p
	}
	icon := a.ID + "/" + a.Name
	up, err := sv.data.UpdateProject(p.ID, func(p *model.Project) error {
		if p.Icon == "" {
			p.Icon = icon
		}
		return nil
	})
	if err != nil {
		slog.Warn("project favicon", "project", p.Name, "err", err)
		return p
	}
	return up
}
