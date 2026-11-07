package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// An org's slug names it in URLs: with org subdomains on, the org is served
// at <slug>.<base domain>. A slug is a lower-case DNS label, unique among the
// orgs, that isn't reserved; owners may rename it, and the old one then
// redirects to the org for SlugRedirectTTL, during which no other org can
// take it.

const (
	MinSlug = 3
	MaxSlug = 40
	// SlugRedirectTTL is how long a renamed org's old slug redirects to it.
	SlugRedirectTTL = 30 * 24 * time.Hour
)

// DefaultReservedSlugs are names no org may use as its slug: subdomains a
// deployment is likely to use for something else.
var DefaultReservedSlugs = []string{
	"www", "api", "app", "admin", "preview", "runner", "mcp", "oauth", "auth", "login",
	"mail", "status", "docs", "static", "assets", "help", "support", "billing",
}

var (
	ErrBadSlug      = fmt.Errorf("a subdomain name is %d to %d lower-case letters (a-z), digits or hyphens, not starting or ending with a hyphen", MinSlug, MaxSlug)
	ErrSlugReserved = errors.New("that name is reserved; pick another")
	ErrSlugTaken    = errors.New("that name is taken; pick another")
)

// ValidSlug reports whether s follows the rules for a slug, reserved names
// aside: a DNS label of MinSlug to MaxSlug characters a-z, 0-9 and '-', not
// starting or ending with '-'.
func ValidSlug(s string) bool {
	if len(s) < MinSlug || len(s) > MaxSlug || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	// Labels like xn--… are punycode (IDNA): they would show up as some
	// other name in the address bar.
	return !(len(s) >= 4 && s[2:4] == "--")
}

// reserved reports whether slug is reserved: by SlugReserved when set,
// else DefaultReservedSlugs.
func (a *Accounts) reserved(slug string) bool {
	if a.s != nil && a.s.SlugReserved != nil {
		return a.s.SlugReserved(slug)
	}
	return slices.Contains(DefaultReservedSlugs, slug)
}

// UsableSlug reports whether slug can be an org's subdomain: valid and not
// reserved.
func (a *Accounts) UsableSlug(slug string) bool { return ValidSlug(slug) && !a.reserved(slug) }

// checkSlug normalises slug and checks it may be used.
func (a *Accounts) checkSlug(slug string) (string, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !ValidSlug(slug) {
		return slug, ErrBadSlug
	}
	if a.reserved(slug) {
		return slug, ErrSlugReserved
	}
	return slug, nil
}

// CheckSlug reports why slug can't be an org's slug, if it can't (whether
// it is taken aside).
func (a *Accounts) CheckSlug(slug string) error {
	_, err := a.checkSlug(slug)
	return err
}

// slugHeld reports whether slug belongs to an org other than orgID: as its
// slug, or as an old slug still redirecting to it.
func slugHeld(tx *sql.Tx, slug string, orgID int64, now time.Time) (bool, error) {
	var held bool
	err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM organizations WHERE slug = ?1 AND id != ?2)
		OR EXISTS (SELECT 1 FROM org_slug_redirects WHERE slug = ?1 AND org_id != ?2 AND expires_at > ?3)`,
		slug, orgID, now.Unix()).Scan(&held)
	return held, err
}

// slugBase is the slug an org named name starts from: name made URL-safe,
// padded to MinSlug and steered off reserved names. Numbers are appended to
// it when it's taken (createOrg), so it leaves room for them.
func (a *Accounts) slugBase(name string) string {
	s := slugify(name)
	if len(s) > MaxSlug-5 {
		s = strings.TrimRight(s[:MaxSlug-5], "-")
	}
	if len(s) < MinSlug || !ValidSlug(s) || a.reserved(s) {
		s = strings.Trim(s+"-org", "-")
	}
	if !ValidSlug(s) || a.reserved(s) {
		s = "org"
	}
	return s
}

// freeSlug is the first of base, base-2, base-3, … that no org holds and
// isn't reserved, in tx.
func (a *Accounts) freeSlug(tx *sql.Tx, base string, orgID int64, now time.Time) (string, error) {
	for i := 1; i < 10000; i++ {
		s := base
		if i > 1 {
			s = fmt.Sprintf("%s-%d", base, i)
		}
		if !ValidSlug(s) || a.reserved(s) {
			continue
		}
		held, err := slugHeld(tx, s, orgID, now)
		if err != nil {
			return "", err
		}
		var folder bool // another org's folder: not a clash for the slug, but createOrg uses it as the folder too
		if err := tx.QueryRow("SELECT EXISTS (SELECT 1 FROM organizations WHERE folder = ? AND id != ?)", s, orgID).Scan(&folder); err != nil {
			return "", err
		}
		if !held && !folder {
			return s, nil
		}
	}
	return "", errors.New("no free slug for " + base)
}

// OrgBySlug is the org whose slug is slug, or which slug redirects to (moved
// true: the org's Slug is its current one). Unknown slugs are ErrNotFound.
func (a *Accounts) OrgBySlug(slug string, now time.Time) (o Org, moved bool, err error) {
	slug = strings.ToLower(slug)
	err = a.db.QueryRow(`SELECT id, name, slug, require_2fa, personal_user_id IS NOT NULL, COALESCE(folder, slug)
		FROM organizations WHERE slug = ?`, slug).Scan(&o.ID, &o.Name, &o.Slug, &o.Require2FA, &o.Personal, &o.Folder)
	if err == nil {
		return o, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return o, false, err
	}
	err = a.db.QueryRow(`SELECT o.id, o.name, o.slug, o.require_2fa, o.personal_user_id IS NOT NULL, COALESCE(o.folder, o.slug)
		FROM org_slug_redirects r JOIN organizations o ON o.id = r.org_id WHERE r.slug = ? AND r.expires_at > ?`, slug, now.Unix()).
		Scan(&o.ID, &o.Name, &o.Slug, &o.Require2FA, &o.Personal, &o.Folder)
	if errors.Is(err, sql.ErrNoRows) {
		return o, false, ErrNotFound
	}
	return o, err == nil, err
}

// Org is the org with id.
func (a *Accounts) Org(id int64) (Org, error) {
	var o Org
	err := a.db.QueryRow(`SELECT id, name, slug, require_2fa, personal_user_id IS NOT NULL, COALESCE(folder, slug)
		FROM organizations WHERE id = ?`, id).Scan(&o.ID, &o.Name, &o.Slug, &o.Require2FA, &o.Personal, &o.Folder)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	return o, err
}

// SetOrgSlug renames orgID's slug, if actor is one of its owners. The old
// slug redirects to the org for SlugRedirectTTL and is held for it; an org
// may take back its own old slug.
func (a *Accounts) SetOrgSlug(actor, orgID int64, slug string, now time.Time) (Org, error) {
	slug, err := a.checkSlug(slug)
	if err != nil {
		return Org{}, err
	}
	var o Org
	err = inTx(a.db, func(tx *sql.Tx) error {
		m, err := membership(tx, actor, orgID)
		if err != nil {
			return err
		}
		if m.Role != RoleOwner {
			return ErrForbidden
		}
		o = m.Org
		if o.Slug == slug {
			return nil
		}
		if _, err := tx.Exec("DELETE FROM org_slug_redirects WHERE expires_at <= ?", now.Unix()); err != nil {
			return err
		}
		held, err := slugHeld(tx, slug, orgID, now)
		if err != nil {
			return err
		}
		if held {
			return ErrSlugTaken
		}
		if _, err := tx.Exec("DELETE FROM org_slug_redirects WHERE slug = ? AND org_id = ?", slug, orgID); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT OR REPLACE INTO org_slug_redirects (slug, org_id, expires_at) VALUES (?, ?, ?)",
			o.Slug, orgID, now.Add(SlugRedirectTTL).Unix()); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE organizations SET slug = ? WHERE id = ?", slug, orgID); err != nil {
			if isUnique(err) {
				return ErrSlugTaken
			}
			return err
		}
		o.Slug = slug
		return nil
	})
	return o, err
}

// FixSlugs gives every org whose slug can't be a subdomain (made before the
// rules, or now reserved) one that can, derived from its name, and returns
// those orgs. Such a slug was never served as a subdomain, so nothing
// redirects from it.
func (a *Accounts) FixSlugs(now time.Time) ([]Org, error) {
	rows, err := a.db.Query("SELECT id, name, slug FROM organizations ORDER BY id")
	if err != nil {
		return nil, err
	}
	var bad []Org
	for rows.Next() {
		var o Org
		if err := rows.Scan(&o.ID, &o.Name, &o.Slug); err != nil {
			rows.Close()
			return nil, err
		}
		if !a.UsableSlug(o.Slug) {
			bad = append(bad, o)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range bad {
		err := inTx(a.db, func(tx *sql.Tx) error {
			s, err := a.freeSlug(tx, a.slugBase(bad[i].Name), bad[i].ID, now)
			if err != nil {
				return err
			}
			if _, err := tx.Exec("UPDATE organizations SET slug = ? WHERE id = ?", s, bad[i].ID); err != nil {
				return err
			}
			bad[i].Slug = s
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return bad, nil
}

// SlugRedirects lists orgID's old slugs that still redirect to it, with
// when they stop.
func (a *Accounts) SlugRedirects(orgID int64, now time.Time) (map[string]time.Time, error) {
	rows, err := a.db.Query("SELECT slug, expires_at FROM org_slug_redirects WHERE org_id = ? AND expires_at > ? ORDER BY slug", orgID, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var s string
		var exp int64
		if err := rows.Scan(&s, &exp); err != nil {
			return nil, err
		}
		out[s] = time.Unix(exp, 0)
	}
	return out, rows.Err()
}
