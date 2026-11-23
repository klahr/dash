package store

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// ---- Starred projects ----
//
// Each user stars the projects they work on, in each org they are in: the
// sidebar lists those first and folds the rest away. A star names a
// project as the browser sees it (StarKey), so it follows the user to
// every browser.

var (
	ErrBadStarKey   = errors.New(`a project key is a project's id, or "<runner>/<id>" for a runner's own project`)
	ErrTooManyStars = errors.New("you can star at most 500 projects")
)

// maxStars caps one user's stars in one org.
const maxStars = 500

// starKeyRe matches a star's project key: a project id (NewID), after the
// runner's name (tunnel's runner names) for a project only that runner has.
// The computer serving the page goes by no name: its name is its host
// name, which changes when it moves.
var starKeyRe = regexp.MustCompile(`^(?:[a-z0-9-]{1,32}/)?[A-Za-z0-9_.-]{1,64}$`)

// ValidStarKey reports whether key can name a starred project.
func ValidStarKey(key string) bool { return starKeyRe.MatchString(key) }

// StarredProjects lists the project keys user starred in org, in the
// order they starred them.
func (s *Store) StarredProjects(user, org int64) ([]string, error) {
	return queryAll(s.db, func(sc scanner) (k string, err error) {
		err = sc.Scan(&k)
		return k, err
	}, "SELECT project_key FROM starred_projects WHERE user_id = ? AND org_id = ? ORDER BY created_at, rowid", user, org)
}

// StarProject stars key for user in org (starring it again changes
// nothing).
func (s *Store) StarProject(user, org int64, key string, now time.Time) error {
	if !ValidStarKey(key) {
		return ErrBadStarKey
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM starred_projects WHERE user_id = ? AND org_id = ?", user, org).Scan(&n); err != nil {
		return err
	}
	if n >= maxStars {
		var one int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM starred_projects WHERE user_id = ? AND org_id = ? AND project_key = ?", user, org, key).Scan(&one); err != nil {
			return err
		}
		if one == 0 {
			return ErrTooManyStars
		}
	}
	_, err := s.db.Exec(`INSERT INTO starred_projects (user_id, org_id, project_key, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, org_id, project_key) DO NOTHING`, user, org, key, now.Unix())
	return err
}

// UnstarProject takes user's star off key in org (if it had one).
func (s *Store) UnstarProject(user, org int64, key string) error {
	_, err := s.db.Exec("DELETE FROM starred_projects WHERE user_id = ? AND org_id = ? AND project_key = ?", user, org, key)
	return err
}

// ---- the org's view ----

// StarredProjects lists the user's starred projects in the org. Those of
// this Dash that are gone (or not the org's) are left out; a runner's own
// projects are the runner's to know, so the browser skips those it
// doesn't find.
func (o *OrgStore) StarredProjects() ([]string, error) {
	if o.user == 0 || o.org == 0 {
		return nil, ErrNotFound
	}
	keys, err := o.s.StarredProjects(o.user, o.org)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, k := range keys {
		if strings.Contains(k, "/") || o.ownsProject(k) {
			out = append(out, k)
		}
	}
	return out, nil
}

// StarProject stars key for the user in the org.
func (o *OrgStore) StarProject(key string, now time.Time) error {
	if o.user == 0 || o.org == 0 {
		return ErrNotFound
	}
	return o.s.StarProject(o.user, o.org, key, now)
}

// UnstarProject takes the user's star off key in the org.
func (o *OrgStore) UnstarProject(key string) error {
	if o.user == 0 || o.org == 0 {
		return ErrNotFound
	}
	return o.s.UnstarProject(o.user, o.org, key)
}
