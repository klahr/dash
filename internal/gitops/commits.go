package gitops

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/klahr/dash/internal/model"
)

// commitFormat is one commit per record: fields split by \x1f, records by \x1e.
const commitFormat = "--format=%H%x1f%h%x1f%s%x1f%an%x1f%aI%x1e"

func parseCommits(out string) []model.CommitInfo {
	var cs []model.CommitInfo
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.Split(strings.TrimLeft(rec, "\n"), "\x1f")
		if len(f) != 5 || f[0] == "" {
			continue
		}
		at, _ := time.Parse(time.RFC3339, strings.TrimSpace(f[4]))
		cs = append(cs, model.CommitInfo{SHA: f[0], Short: f[1], Subject: f[2], Author: f[3], Date: at})
	}
	return cs
}

// Commits lists the commits reachable from ref, newest first: at most
// limit of them, of the latest scan that match query (case-insensitive,
// in the hash, subject or author; "" matches all).
func (g *Git) Commits(ctx context.Context, projectID, repo, ref, query string, limit, scan int) ([]model.CommitInfo, error) {
	if err := CheckRev(ref); err != nil {
		return nil, err
	}
	out, err := g.run(ctx, false, projectID, repo, "git", "log", "--no-color", "--max-count="+strconv.Itoa(max(scan, 1)), commitFormat, ref, "--")
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	cs := []model.CommitInfo{}
	for _, c := range parseCommits(out) {
		if q != "" && !strings.HasPrefix(c.SHA, q) && !strings.Contains(strings.ToLower(c.Subject), q) && !strings.Contains(strings.ToLower(c.Author), q) {
			continue
		}
		cs = append(cs, c)
		if len(cs) >= limit {
			break
		}
	}
	return cs, nil
}

// CommitOf resolves rev (a hash, a branch, a tag, …) to its commit.
func (g *Git) CommitOf(ctx context.Context, projectID, repo, rev string) (model.CommitInfo, error) {
	if err := CheckRev(rev); err != nil {
		return model.CommitInfo{}, err
	}
	out, err := g.run(ctx, false, projectID, repo, "git", "log", "--no-color", "-1", commitFormat, rev+"^{commit}", "--")
	if err != nil {
		return model.CommitInfo{}, err
	}
	cs := parseCommits(out)
	if len(cs) == 0 {
		return model.CommitInfo{}, errors.New("no such commit: " + rev)
	}
	return cs[0], nil
}

// ErrBadRev is a revision Dash won't pass to git.
var ErrBadRev = errors.New("not a commit hash or ref name")

// CheckRev accepts a revision someone typed: a commit hash, a branch or a
// tag name, optionally with ~N or ^N. It refuses anything git could read as
// an option or a range, and spaces or control characters.
func CheckRev(rev string) error {
	if rev == "" || len(rev) > 200 || strings.HasPrefix(rev, "-") || strings.Contains(rev, "..") || strings.Contains(rev, "@{") {
		return ErrBadRev
	}
	for _, r := range rev {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(":?*[\\", r) {
			return ErrBadRev
		}
	}
	return nil
}
