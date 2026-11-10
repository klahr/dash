package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/gitops"
)

func (rr *releaseRepo) commitDetail(t *testing.T, sha string) gitops.CommitDetail {
	t.Helper()
	w := rr.call(t, "GET", "/commits/"+sha, "")
	var d gitops.CommitDetail
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &d) != nil {
		t.Fatalf("commit %s: %d %s", sha, w.Code, w.Body)
	}
	return d
}

func (rr *releaseRepo) fileDiff(t *testing.T, sha, path string) (int, string) {
	t.Helper()
	w := rr.call(t, "GET", "/commits/"+sha+"/diff?path="+url.QueryEscape(path), "")
	return w.Code, w.Body.String()
}

func writeRepoFile(t *testing.T, repo, name, content string) {
	t.Helper()
	p := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileByPath(d gitops.CommitDetail, path string) (gitops.CommitFile, bool) {
	for _, f := range d.Files {
		if f.Path == path {
			return f, true
		}
	}
	return gitops.CommitFile{}, false
}

func TestCommitDetail(t *testing.T) {
	rr := newReleaseRepo(t, "")
	root := rgit(t, rr.repo, "rev-parse", "HEAD")

	// A normal commit: added and modified files, a body, an author who isn't the committer.
	writeRepoFile(t, rr.repo, "a.txt", "one\ntwo\nthree\n")
	writeRepoFile(t, rr.repo, "dir/b.txt", "bee\n")
	rgit(t, rr.repo, "add", "-A")
	rgit(t, rr.repo, "commit", "-q", "--author=Ada <ada@example.com>", "-m", "Add files\n\nWhy they are here.\nSecond line.")
	normal := rgit(t, rr.repo, "rev-parse", "HEAD")
	d := rr.commitDetail(t, normal[:10])
	if d.Hash != normal || d.Subject != "Add files" || d.Body != "Why they are here.\nSecond line." {
		t.Errorf("message: %+v", d)
	}
	if d.Author.Name != "Ada" || d.Author.Email != "ada@example.com" || d.Committer.Name != "T" || d.Committer.Email != "t@t" {
		t.Errorf("people: %+v %+v", d.Author, d.Committer)
	}
	if !strings.Contains(d.Author.Date, "T") || len(d.Parents) != 1 || d.Parents[0] != root {
		t.Errorf("date or parents: %q %v", d.Author.Date, d.Parents)
	}
	if !strings.Contains(strings.Join(d.Refs, ","), "main") {
		t.Errorf("refs: %v", d.Refs)
	}
	if f, ok := fileByPath(d, "a.txt"); !ok || f.Status != "A" || f.Add != 3 || f.Del != 0 {
		t.Errorf("a.txt: %+v (%+v)", f, d.Files)
	}
	if d.FileCount != 2 || len(d.Files) != 2 {
		t.Errorf("files: %d %+v", d.FileCount, d.Files)
	}
	code, diff := rr.fileDiff(t, normal, "dir/b.txt")
	if code != http.StatusOK || !strings.Contains(diff, "diff --git a/dir/b.txt b/dir/b.txt") || !strings.Contains(diff, "+bee") {
		t.Errorf("diff: %d %s", code, diff)
	}

	// The root commit is compared with the empty tree.
	rd := rr.commitDetail(t, root)
	if len(rd.Parents) != 0 || rd.Subject != "first" || len(rd.Files) != 0 {
		t.Errorf("root: %+v", rd)
	}
	rootRepo := newReleaseRepo(t, "")
	writeRepoFile(t, rootRepo.repo, "x.txt", "hi\n")
	rgit(t, rootRepo.repo, "checkout", "-q", "--orphan", "fresh")
	rgit(t, rootRepo.repo, "add", "x.txt")
	rgit(t, rootRepo.repo, "commit", "-q", "-m", "fresh start")
	fresh := rgit(t, rootRepo.repo, "rev-parse", "HEAD")
	fd := rootRepo.commitDetail(t, fresh)
	if f, ok := fileByPath(fd, "x.txt"); len(fd.Parents) != 0 || !ok || f.Status != "A" || f.Add != 1 {
		t.Errorf("root with a file: %+v", fd)
	}
	if code, diff := rootRepo.fileDiff(t, fresh, "x.txt"); code != http.StatusOK || !strings.Contains(diff, "+hi") {
		t.Errorf("root diff: %d %s", code, diff)
	}

	// A rename keeps its old path, and its diff covers both.
	rgit(t, rr.repo, "mv", "a.txt", "renamed.txt")
	writeRepoFile(t, rr.repo, "renamed.txt", "one\ntwo\nthree\nfour\n")
	rgit(t, rr.repo, "add", "-A")
	rgit(t, rr.repo, "commit", "-q", "-m", "Rename")
	ren := rgit(t, rr.repo, "rev-parse", "HEAD")
	d = rr.commitDetail(t, ren)
	if f, ok := fileByPath(d, "renamed.txt"); !ok || f.Status != "R" || f.OldPath != "a.txt" || f.Add != 1 || f.Del != 0 {
		t.Errorf("rename: %+v", d.Files)
	}
	if code, diff := rr.fileDiff(t, ren, "renamed.txt"); code != http.StatusOK || !strings.Contains(diff, "rename from a.txt") || !strings.Contains(diff, "+four") {
		t.Errorf("rename diff: %d %s", code, diff)
	}

	// A binary file is flagged, without counts.
	writeRepoFile(t, rr.repo, "img.bin", "\x00\x01\x02binary\x00")
	rgit(t, rr.repo, "add", "-A")
	rgit(t, rr.repo, "commit", "-q", "-m", "Binary")
	bin := rgit(t, rr.repo, "rev-parse", "HEAD")
	if f, ok := fileByPath(rr.commitDetail(t, bin), "img.bin"); !ok || !f.Binary || f.Add != 0 || f.Status != "A" {
		t.Errorf("binary: %+v", f)
	}
	if code, diff := rr.fileDiff(t, bin, "img.bin"); code != http.StatusOK || !strings.Contains(diff, "Binary files") {
		t.Errorf("binary diff: %d %s", code, diff)
	}

	// A merge commit is compared with its first parent: what it brought in.
	rgit(t, rr.repo, "checkout", "-q", "-b", "side", root)
	writeRepoFile(t, rr.repo, "side.txt", "side\n")
	rgit(t, rr.repo, "add", "-A")
	rgit(t, rr.repo, "commit", "-q", "-m", "Side work")
	side := rgit(t, rr.repo, "rev-parse", "HEAD")
	rgit(t, rr.repo, "checkout", "-q", "main")
	rgit(t, rr.repo, "merge", "-q", "--no-ff", "-m", "Merge side", "side")
	merge := rgit(t, rr.repo, "rev-parse", "HEAD")
	d = rr.commitDetail(t, merge)
	if len(d.Parents) != 2 || d.Parents[0] != bin || d.Parents[1] != side {
		t.Errorf("merge parents: %v", d.Parents)
	}
	if len(d.Files) != 1 || d.Files[0].Path != "side.txt" || d.Files[0].Status != "A" {
		t.Errorf("merge files: %+v", d.Files)
	}
	if code, diff := rr.fileDiff(t, merge, "side.txt"); code != http.StatusOK || !strings.Contains(diff, "+side") {
		t.Errorf("merge diff: %d %s", code, diff)
	}
}

func TestCommitDetailRefusals(t *testing.T) {
	rr := newReleaseRepo(t, "")
	writeRepoFile(t, rr.repo, "a.txt", "a\n")
	rgit(t, rr.repo, "add", "-A")
	rgit(t, rr.repo, "commit", "-q", "-m", "A")
	sha := rgit(t, rr.repo, "rev-parse", "HEAD")
	// A branch named like a hash prefix doesn't stand in for a commit.
	rgit(t, rr.repo, "branch", "beef")

	for _, bad := range []string{"abc", "HEAD", "main", "zzzz1234", "-n1", strings.Repeat("a", 41), sha + "0", "abcd..ef01", "abcd%5E"} {
		if w := rr.call(t, "GET", "/commits/"+bad, ""); w.Code != http.StatusBadRequest {
			t.Errorf("%q: %d %s", bad, w.Code, w.Body)
		}
	}
	for _, unknown := range []string{"beef", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"} {
		if w := rr.call(t, "GET", "/commits/"+unknown, ""); w.Code != http.StatusNotFound {
			t.Errorf("unknown %q: %d %s", unknown, w.Code, w.Body)
		}
	}
	if w := rr.call(t, "GET", "/commits/deadbeef/diff?path=a.txt", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown commit's diff: %d %s", w.Code, w.Body)
	}

	for _, bad := range []string{"", "../etc/passwd", "a/../../x", "/etc/passwd", "-p", "--output=/tmp/x", "./a.txt", "a\\b", "a//b"} {
		if code, body := rr.fileDiff(t, sha, bad); code != http.StatusBadRequest {
			t.Errorf("path %q: %d %s", bad, code, body)
		}
	}
	// A path the commit didn't change, even a glob that would match one.
	for _, other := range []string{"b.txt", "*.txt", ":(glob)*"} {
		if code, body := rr.fileDiff(t, sha, other); code != http.StatusNotFound {
			t.Errorf("path %q: %d %s", other, code, body)
		}
	}
}

func TestCommitDiffTruncated(t *testing.T) {
	rr := newReleaseRepo(t, "")
	writeRepoFile(t, rr.repo, "big.txt", strings.Repeat("a line of text that is long enough\n", maxCommitDiff/30))
	rgit(t, rr.repo, "add", "-A")
	rgit(t, rr.repo, "commit", "-q", "-m", "Big")
	code, diff := rr.fileDiff(t, rgit(t, rr.repo, "rev-parse", "HEAD"), "big.txt")
	if code != http.StatusOK || len(diff) > maxCommitDiff+100 || !strings.HasSuffix(diff, "[diff truncated]") {
		t.Errorf("big diff: %d, %d bytes", code, len(diff))
	}
}
