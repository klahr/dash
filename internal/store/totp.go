package store

import (
	"database/sql"
	"errors"
)

var (
	ErrTOTPEnabled   = errors.New("two-factor sign-in is already on")
	ErrTOTPNotSetUp  = errors.New("two-factor sign-in isn't on")
	ErrNoPendingTOTP = errors.New("start setting up two-factor sign-in again")
	ErrTOTPRequired  = errors.New("an organization you belong to requires two-factor sign-in")
	ErrTOTPSelf      = errors.New("turn on two-factor sign-in for your own account first")
)

// requires2FA is an SQL expression: whether any org the user u belongs to
// requires two-factor sign-in.
const requires2FA = `EXISTS (SELECT 1 FROM org_memberships rm JOIN organizations ro ON ro.id = rm.org_id
	WHERE rm.user_id = u.id AND ro.require_2fa = 1)`

// Requires2FA reports whether an org uid belongs to requires two-factor
// sign-in.
func (a *Accounts) Requires2FA(uid int64) (bool, error) {
	var req bool
	err := a.db.QueryRow("SELECT "+requires2FA+" FROM users u WHERE u.id = ?", uid).Scan(&req)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return req, err
}

// BeginTOTP keeps secret as uid's enrolment in progress, replacing any
// earlier one, until ConfirmTOTP.
func (a *Accounts) BeginTOTP(uid int64, secret string) error {
	res, err := a.db.Exec("UPDATE users SET totp_pending = ? WHERE id = ? AND totp_secret IS NULL", secret, uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTOTPEnabled
	}
	return nil
}

// PendingTOTP is the secret of uid's enrolment in progress.
func (a *Accounts) PendingTOTP(uid int64) (string, error) {
	var s sql.NullString
	err := a.db.QueryRow("SELECT totp_pending FROM users WHERE id = ? AND totp_secret IS NULL", uid).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !s.Valid {
		return "", ErrNoPendingTOTP
	}
	return s.String, err
}

// ConfirmTOTP turns two-factor sign-in on with the pending secret (which
// must still be secret), marks step used, and gives uid the recovery codes.
func (a *Accounts) ConfirmTOTP(uid int64, secret string, step int64, codes []string) error {
	return inTx(a.db, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE users SET totp_secret = totp_pending, totp_pending = NULL, totp_last_step = ?
			WHERE id = ? AND totp_secret IS NULL AND totp_pending = ?`, step, uid, secret)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoPendingTOTP
		}
		return replaceCodes(tx, uid, codes)
	})
}

func replaceCodes(tx *sql.Tx, uid int64, codes []string) error {
	if _, err := tx.Exec("DELETE FROM recovery_codes WHERE user_id = ?", uid); err != nil {
		return err
	}
	for _, c := range codes {
		if _, err := tx.Exec("INSERT INTO recovery_codes (user_id, code_hash) VALUES (?, ?)", uid, hashToken(c)); err != nil {
			return err
		}
	}
	return nil
}

// TOTPSecret is uid's secret, if two-factor sign-in is on.
func (a *Accounts) TOTPSecret(uid int64) (string, error) {
	var s sql.NullString
	err := a.db.QueryRow("SELECT totp_secret FROM users WHERE id = ?", uid).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !s.Valid {
		return "", ErrTOTPNotSetUp
	}
	return s.String, err
}

// UseTOTPStep records that uid signed in with the code of step, and reports
// false if that step (or a later one) was used already: a replayed code.
func (a *Accounts) UseTOTPStep(uid, step int64) (bool, error) {
	res, err := a.db.Exec("UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_secret IS NOT NULL AND totp_last_step < ?", step, uid, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// UseRecoveryCode spends one of uid's recovery codes, reporting whether it
// was one.
func (a *Accounts) UseRecoveryCode(uid int64, code string) (bool, error) {
	res, err := a.db.Exec(`DELETE FROM recovery_codes WHERE user_id = ? AND code_hash = ?
		AND EXISTS (SELECT 1 FROM users WHERE id = ? AND totp_secret IS NOT NULL)`, uid, hashToken(code), uid)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RecoveryCodesLeft counts uid's unused recovery codes.
func (a *Accounts) RecoveryCodesLeft(uid int64) (int, error) {
	var n int
	err := a.db.QueryRow("SELECT COUNT(*) FROM recovery_codes WHERE user_id = ?", uid).Scan(&n)
	return n, err
}

// ReplaceRecoveryCodes gives uid new recovery codes; the old ones stop working.
func (a *Accounts) ReplaceRecoveryCodes(uid int64, codes []string) error {
	return inTx(a.db, func(tx *sql.Tx) error {
		var on bool
		if err := tx.QueryRow("SELECT totp_secret IS NOT NULL FROM users WHERE id = ?", uid).Scan(&on); err != nil {
			return err
		}
		if !on {
			return ErrTOTPNotSetUp
		}
		return replaceCodes(tx, uid, codes)
	})
}

// DisableTOTP turns uid's two-factor sign-in off, unless an org of theirs
// requires it.
func (a *Accounts) DisableTOTP(uid int64) error {
	return inTx(a.db, func(tx *sql.Tx) error {
		var on, required bool
		if err := tx.QueryRow("SELECT u.totp_secret IS NOT NULL, "+requires2FA+" FROM users u WHERE u.id = ?", uid).Scan(&on, &required); err != nil {
			return err
		}
		if !on {
			return ErrTOTPNotSetUp
		}
		if required {
			return ErrTOTPRequired
		}
		return resetTOTP(tx, uid)
	})
}

func resetTOTP(tx *sql.Tx, uid int64) error {
	if _, err := tx.Exec("UPDATE users SET totp_secret = NULL, totp_pending = NULL, totp_last_step = 0 WHERE id = ?", uid); err != nil {
		return err
	}
	_, err := tx.Exec("DELETE FROM recovery_codes WHERE user_id = ?", uid)
	return err
}

// ResetTOTP turns off two-factor sign-in for the account named login (a
// username or email) regardless of its orgs, for someone who lost both their
// authenticator and their recovery codes. It is for the server's operator
// (dash reset-2fa), not the API. If an org requires it, they are asked to set
// it up again when they next sign in.
func (a *Accounts) ResetTOTP(login string) (User, error) {
	u, _, err := a.Credentials(login)
	if err != nil {
		return u, err
	}
	err = inTx(a.db, func(tx *sql.Tx) error { return resetTOTP(tx, u.ID) })
	u.TOTP = false
	return u, err
}

// SetRequire2FA turns the org's two-factor requirement on or off, as actor,
// who must be an owner and, to turn it on, have it on themselves.
func (a *Accounts) SetRequire2FA(actor, orgID int64, on bool) (Org, error) {
	var o Org
	err := inTx(a.db, func(tx *sql.Tx) error {
		mine, err := membership(tx, actor, orgID)
		if err != nil {
			return err
		}
		if mine.Role != RoleOwner {
			return ErrForbidden
		}
		if on {
			var totp bool
			if err := tx.QueryRow("SELECT totp_secret IS NOT NULL FROM users WHERE id = ?", actor).Scan(&totp); err != nil {
				return err
			}
			if !totp {
				return ErrTOTPSelf
			}
		}
		if _, err := tx.Exec("UPDATE organizations SET require_2fa = ? WHERE id = ?", on, orgID); err != nil {
			return err
		}
		o = mine.Org
		o.Require2FA = on
		return nil
	})
	return o, err
}
