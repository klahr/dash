package store

import (
	"bytes"
	"database/sql"
	"errors"
	"image"
	_ "image/gif" // DecodeConfig reads these
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"time"
)

// MaxAvatar is the largest profile picture Dash keeps, in bytes. The web
// client shrinks pictures to a small square before sending them, far below.
const MaxAvatar = 1 << 20

// maxAvatarSide is the widest and tallest a profile picture may be.
const maxAvatarSide = 4096

var ErrBadAvatar = errors.New("a profile picture is a PNG, JPEG, GIF or WebP image of up to 1 MB and 4096 × 4096 pixels")

// AvatarType returns the content type of the profile picture data: PNG,
// JPEG, GIF or WebP, by its bytes (never what the browser said), or
// ErrBadAvatar. SVG is refused: it can carry script.
func AvatarType(data []byte) (string, error) {
	if len(data) == 0 || len(data) > MaxAvatar {
		return "", ErrBadAvatar
	}
	ct := http.DetectContentType(data)
	switch ct {
	case "image/png", "image/jpeg", "image/gif":
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxAvatarSide || cfg.Height > maxAvatarSide {
			return "", ErrBadAvatar
		}
	case "image/webp":
		// The standard library has no WebP decoder; the type is sniffed
		// from its RIFF header, and the size is capped.
	default:
		return "", ErrBadAvatar
	}
	return ct, nil
}

// SetAvatar makes data uid's profile picture and returns its version (see
// User.Avatar). Each new picture gets a later version than the last.
func (a *Accounts) SetAvatar(uid int64, data []byte, now time.Time) (int64, error) {
	ct, err := AvatarType(data)
	if err != nil {
		return 0, err
	}
	var at int64
	err = inTx(a.db, func(tx *sql.Tx) error {
		if err := tx.QueryRow("UPDATE users SET avatar_at = MAX(avatar_at + 1, ?) WHERE id = ? RETURNING avatar_at",
			now.UnixMilli(), uid).Scan(&at); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		_, err := tx.Exec(`INSERT INTO user_avatars (user_id, content_type, data) VALUES (?, ?, ?)
			ON CONFLICT (user_id) DO UPDATE SET content_type = excluded.content_type, data = excluded.data`, uid, ct, data)
		return err
	})
	return at, err
}

// DeleteAvatar removes uid's profile picture, if they have one.
func (a *Accounts) DeleteAvatar(uid int64) error {
	return inTx(a.db, func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM user_avatars WHERE user_id = ?", uid); err != nil {
			return err
		}
		_, err := tx.Exec("UPDATE users SET avatar_at = 0 WHERE id = ?", uid)
		return err
	})
}

// Avatar returns uid's profile picture, its content type and its version.
// Without one it is ErrNotFound.
func (a *Accounts) Avatar(uid int64) (data []byte, contentType string, version int64, err error) {
	err = a.db.QueryRow(`SELECT v.data, v.content_type, u.avatar_at FROM user_avatars v JOIN users u ON u.id = v.user_id
		WHERE v.user_id = ?`, uid).Scan(&data, &contentType, &version)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return data, contentType, version, err
}
