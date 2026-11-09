package store

import (
	"database/sql"
	"errors"
	"time"
)

// WallToken lets a TV open one org's wall view (/wall?token=…) without
// signing in. It grants read access to the wall's data and nothing else:
// the auth middleware never accepts it as an API token or a session.
type WallToken struct {
	ID        int64     `json:"id"`
	OrgID     int64     `json:"org_id"`
	CreatedBy int64     `json:"created_by,omitempty"`
	Name      string    `json:"name"`
	Prefix    string    `json:"prefix"`
	Created   time.Time `json:"created"`
	LastUsed  time.Time `json:"last_used,omitzero"`
}

const prefixWall = "dashw_"

const wallCols = `SELECT id, org_id, COALESCE(created_by, 0), name, token_prefix, created_at, last_used_at FROM wall_tokens`

func scanWallToken(sc interface{ Scan(...any) error }) (WallToken, error) {
	var t WallToken
	var created int64
	var used sql.NullInt64
	err := sc.Scan(&t.ID, &t.OrgID, &t.CreatedBy, &t.Name, &t.Prefix, &created, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.Created = time.Unix(created, 0)
	if used.Valid {
		t.LastUsed = time.Unix(used.Int64, 0)
	}
	return t, nil
}

// CreateWallToken makes a wall token for org, made by uid, and returns its
// secret: shown only now.
func (a *Accounts) CreateWallToken(org, uid int64, name string, now time.Time) (string, WallToken, error) {
	if org == 0 {
		return "", WallToken{}, ErrNoOrg
	}
	name, err := cleanTokenName(name)
	if err != nil {
		return "", WallToken{}, err
	}
	secret := prefixWall + newToken()
	var by any
	if uid != 0 {
		by = uid
	}
	res, err := a.db.Exec(`INSERT INTO wall_tokens (org_id, created_by, name, token_hash, token_prefix, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		org, by, name, hashToken(secret), secret[:prefixShown], now.Unix())
	if err != nil {
		return "", WallToken{}, err
	}
	id, _ := res.LastInsertId()
	t, err := scanWallToken(a.db.QueryRow(wallCols+" WHERE id = ?", id))
	return secret, t, err
}

// WallTokens lists org's wall tokens, newest first.
func (a *Accounts) WallTokens(org int64) ([]WallToken, error) {
	rows, err := a.db.Query(wallCols+" WHERE org_id = ? ORDER BY created_at DESC, id DESC", org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WallToken{}
	for rows.Next() {
		t, err := scanWallToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeWallToken deletes org's wall token id (ErrNotFound if it isn't org's).
func (a *Accounts) RevokeWallToken(org, id int64) error {
	res, err := a.db.Exec("DELETE FROM wall_tokens WHERE id = ? AND org_id = ?", id, org)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AuthenticateWall finds the wall token whose secret is secret
// (ErrTokenInvalid if none) and records when it was last used.
func (a *Accounts) AuthenticateWall(secret string, now time.Time) (WallToken, error) {
	if secret == "" {
		return WallToken{}, ErrTokenInvalid
	}
	t, err := scanWallToken(a.db.QueryRow(wallCols+" WHERE token_hash = ?", hashToken(secret)))
	if errors.Is(err, ErrNotFound) {
		return t, ErrTokenInvalid
	}
	if err != nil {
		return t, err
	}
	if now.Sub(t.LastUsed) >= time.Minute { // at most once a minute: the TV polls every few seconds
		if _, err := a.db.Exec("UPDATE wall_tokens SET last_used_at = ? WHERE id = ?", now.Unix(), t.ID); err == nil {
			t.LastUsed = now
		}
	}
	return t, nil
}
