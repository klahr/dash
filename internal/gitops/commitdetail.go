package gitops

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// ErrNoCommit is a commit hash this repository doesn't have.
var ErrNoCommit = errors.New("no such commit")

// ErrBadPath is a file path Dash won't pass to git: empty, absolute, with a
// ".." part, or one git could read as an option.
var ErrBadPath = errors.New("not a file path in the repository")

// MaxCommitFiles is how many changed files CommitDetail lists; the rest
// are only counted (a vendored dependency can touch tens of thousands).
const MaxCommitFiles = 2000

// CommitPerson is a commit's author or committer.
type CommitPerson struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date"` // ISO 8601, with the offset it was made at
}

// CommitFile is a file a commit changed, compared with its first parent.
type CommitFile struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"` // renames and copies
	Status  string `json:"status"`             // A, M, D, R, C or T
	Add     int    `json:"add"`
	Del     int    `json:"del"`
	Binary  bool   `json:"binary,omitempty"`
}

// CommitDetail is one commit: its whole message, who made it, and the
// files it changed.
type CommitDetail struct {
	Hash      string       `json:"hash"`
	Short     string       `json:"short"`
	Parents   []string     `json:"parents"`
	Refs      []string     `json:"refs"`
	Subject   string       `json:"subject"`
	Body      string       `json:"body"`
	Author    CommitPerson `json:"author"`
	Committer CommitPerson `json:"committer"`
	Files     []CommitFile `json:"files"`
	// FileCount is how many files changed; more than len(Files) when the
	// list was cut at MaxCommitFiles.
	FileCount int `json:"file_count"`
}

// CheckCommitHash accepts a commit hash or a prefix of one: 4 to 40 hex
// digits (64 for SHA-256 repositories). Nothing else (no ref names, no
// ranges, no options) gets near git.
func CheckCommitHash(s string) error {
	if len(s) < 4 || len(s) > 64 || (len(s) > 40 && len(s) != 64) {
		return ErrBadRev
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return ErrBadRev
		}
	}
	return nil
}

// CheckRepoPath accepts a path inside the repository as git prints it:
// relative, forward slashes, no "." or ".." parts, not starting with "-".
func CheckRepoPath(p string) error {
	if p == "" || len(p) > 4096 || strings.HasPrefix(p, "-") || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\\") {
		return ErrBadPath
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return ErrBadPath
		}
	}
	return nil
}

// ResolveCommit turns a commit hash, or an unambiguous prefix of one, into
// the full hash. ErrBadRev if it isn't hex, ErrNoCommit if no commit has it.
func (g *Git) ResolveCommit(ctx context.Context, projectID, repo, sha string) (string, error) {
	if err := CheckCommitHash(sha); err != nil {
		return "", err
	}
	sha = strings.ToLower(sha)
	full, err := g.run(ctx, false, projectID, repo, "git", "rev-parse", "--verify", "--quiet", sha+"^{commit}")
	// A branch or tag named like a hex prefix ("beef") resolves too: only a
	// commit whose hash starts with what was asked for counts.
	if err != nil || !strings.HasPrefix(full, sha) {
		return "", ErrNoCommit
	}
	return full, nil
}

// CommitDetail returns the commit sha (a hash or a prefix of one) with its
// message, author, committer and changed files.
func (g *Git) CommitDetail(ctx context.Context, projectID, repo, sha string) (CommitDetail, error) {
	full, err := g.ResolveCommit(ctx, projectID, repo, sha)
	if err != nil {
		return CommitDetail{}, err
	}
	// NUL can't be in any of these, so it splits them; %B (the raw message) goes last.
	out, err := g.run(ctx, false, projectID, repo, "git", "log", "-1", "--no-color", "--no-show-signature",
		"--format=%H%x00%h%x00%P%x00%D%x00%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI%x00%B", full, "--")
	if err != nil {
		return CommitDetail{}, err
	}
	f := strings.SplitN(out, "\x00", 11)
	if len(f) != 11 {
		return CommitDetail{}, ErrNoCommit
	}
	d := CommitDetail{
		Hash: f[0], Short: f[1], Parents: strings.Fields(f[2]), Refs: []string{},
		Author:    CommitPerson{Name: f[4], Email: f[5], Date: f[6]},
		Committer: CommitPerson{Name: f[7], Email: f[8], Date: f[9]},
	}
	if d.Parents == nil {
		d.Parents = []string{}
	}
	if f[3] != "" {
		d.Refs = strings.Split(f[3], ", ")
	}
	msg := strings.TrimRight(f[10], "\n")
	d.Subject, d.Body, _ = strings.Cut(msg, "\n")
	d.Body = strings.Trim(d.Body, "\n")
	files, err := g.commitFiles(ctx, projectID, repo, d.Hash, d.Parents)
	if err != nil {
		return CommitDetail{}, err
	}
	d.FileCount = len(files)
	if len(files) > MaxCommitFiles {
		files = files[:MaxCommitFiles]
	}
	d.Files = files
	return d, nil
}

// diffBase is what diff-tree compares a commit with: its first parent (for
// a merge too: what the merge brought into the branch), or the empty tree
// for a root commit.
func diffBase(sha string, parents []string) []string {
	if len(parents) == 0 {
		return []string{"--root", sha}
	}
	return []string{parents[0], sha}
}

// commitFiles lists the files commit sha changed compared with diffBase,
// with their +/- line counts.
func (g *Git) commitFiles(ctx context.Context, projectID, repo, sha string, parents []string) ([]CommitFile, error) {
	base := diffBase(sha, parents)
	status, err := g.run(ctx, false, projectID, repo, "git", append(append([]string{"diff-tree", "-r", "-z", "-M", "--no-commit-id", "--name-status"}, base...), "--")...)
	if err != nil {
		return nil, err
	}
	numstat, err := g.run(ctx, false, projectID, repo, "git", append(append([]string{"diff-tree", "-r", "-z", "-M", "--no-commit-id", "--numstat"}, base...), "--")...)
	if err != nil {
		return nil, err
	}
	files := parseNameStatus(status)
	counts := parseNumstat(numstat)
	for i := range files {
		if c, ok := counts[files[i].Path]; ok {
			files[i].Add, files[i].Del, files[i].Binary = c.Add, c.Del, c.Binary
		}
	}
	return files, nil
}

// parseNameStatus reads `diff-tree --name-status -z`: a status, then the
// path (the old and the new path for renames and copies), each NUL-ended.
func parseNameStatus(out string) []CommitFile {
	f := strings.Split(out, "\x00")
	files := []CommitFile{}
	for i := 0; i < len(f); i++ {
		st := strings.TrimSpace(f[i])
		if st == "" {
			continue
		}
		c := CommitFile{Status: st[:1]}
		if (c.Status == "R" || c.Status == "C") && i+2 < len(f) {
			c.OldPath, c.Path = f[i+1], f[i+2]
			i += 2
		} else if i+1 < len(f) {
			c.Path = f[i+1]
			i++
		} else {
			break
		}
		files = append(files, c)
	}
	return files
}

// parseNumstat reads `diff-tree --numstat -z` into counts by (new) path:
// "add\tdel\tpath\0", or "add\tdel\t\0old\0new\0" for a rename; "-" counts
// mean a binary file.
func parseNumstat(out string) map[string]CommitFile {
	f := strings.Split(out, "\x00")
	counts := map[string]CommitFile{}
	for i := 0; i < len(f); i++ {
		parts := strings.SplitN(strings.TrimLeft(f[i], "\n"), "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if path == "" && i+2 < len(f) { // rename: old and new path follow
			path = f[i+2]
			i += 2
		}
		c := CommitFile{Binary: parts[0] == "-" && parts[1] == "-"}
		c.Add, _ = strconv.Atoi(parts[0])
		c.Del, _ = strconv.Atoi(parts[1])
		counts[path] = c
	}
	return counts
}

// CommitFileDiff returns the patch commit sha made to path, compared with
// diffBase. path must be one of the files the commit changed
// (ErrNoFileChange otherwise); a rename's diff covers both its paths.
func (g *Git) CommitFileDiff(ctx context.Context, projectID, repo, sha, path string) (string, error) {
	if err := CheckRepoPath(path); err != nil {
		return "", err
	}
	full, err := g.ResolveCommit(ctx, projectID, repo, sha)
	if err != nil {
		return "", err
	}
	ps, err := g.run(ctx, false, projectID, repo, "git", "rev-list", "--parents", "-n", "1", full, "--")
	if err != nil {
		return "", err
	}
	parents := strings.Fields(ps)[1:]
	files, err := g.commitFiles(ctx, projectID, repo, full, parents)
	if err != nil {
		return "", err
	}
	paths := []string(nil)
	for _, f := range files {
		if f.Path == path {
			paths = []string{f.Path}
			if f.OldPath != "" {
				paths = []string{f.OldPath, f.Path}
			}
			break
		}
	}
	if paths == nil {
		return "", ErrNoFileChange
	}
	// Literal pathspecs: a "*" or ":" in a file's name is just a character.
	args := append([]string{"--literal-pathspecs", "diff-tree", "-p", "-M", "--no-commit-id", "--no-color", "--no-ext-diff", "--no-textconv"}, diffBase(full, parents)...)
	return g.run(ctx, false, projectID, repo, "git", append(append(args, "--"), paths...)...)
}

// ErrNoFileChange is a path the commit didn't change.
var ErrNoFileChange = errors.New("the commit didn't change that file")
