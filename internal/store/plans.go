package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ---- Plans ----
//
// A plan is one user's, in one org, and nobody else reads it, admins
// included: it is a draft of theirs until they launch it. Plans live in
// the database only (no copy in memory): they are read when the planner
// opens one, a few times a minute at most.

// ErrNoUser: plans are a person's, and the caller is none (another Dash).
var ErrNoUser = errors.New("plans are a person's: sign in to plan a project")

// ListPlans returns the user's plans in the org that aren't launched yet,
// most recently changed first.
func (o *OrgStore) ListPlans() ([]model.Plan, error) {
	if o.org == 0 || o.user == 0 {
		return nil, ErrNoUser
	}
	rows, err := o.s.db.Query("SELECT data FROM plans WHERE org_id = ? AND user_id = ? AND launched = 0 ORDER BY updated_at DESC, id",
		o.org, o.user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Plan{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var p model.Plan
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPlan returns one of the user's plans in the org.
func (o *OrgStore) GetPlan(id string) (model.Plan, error) {
	if o.org == 0 || o.user == 0 {
		return model.Plan{}, ErrNoUser
	}
	var data string
	err := o.s.db.QueryRow("SELECT data FROM plans WHERE id = ? AND org_id = ? AND user_id = ?", id, o.org, o.user).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Plan{}, ErrNotFound
	}
	if err != nil {
		return model.Plan{}, err
	}
	var p model.Plan
	err = json.Unmarshal([]byte(data), &p)
	return p, err
}

// CreatePlan adds a plan for the user in the org. p must be cleaned
// (model.CleanPlanDraft) by the caller.
func (o *OrgStore) CreatePlan(p model.Plan) (model.Plan, error) {
	if o.org == 0 || o.user == 0 {
		return p, ErrNoUser
	}
	p.ID = NewID()
	p.CreatedAt = time.Now().UTC()
	p.UpdatedAt = p.CreatedAt
	data, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	o.s.mu.Lock()
	defer o.s.mu.Unlock()
	_, err = o.s.db.Exec("INSERT INTO plans (id, org_id, user_id, data, launched, updated_at) VALUES (?, ?, ?, ?, 0, ?)",
		p.ID, o.org, o.user, string(data), p.UpdatedAt.Unix())
	return p, err
}

// UpdatePlan applies fn to the user's plan and stores it. fn's error
// leaves the plan as it was. The id, owner and creation time stay.
func (o *OrgStore) UpdatePlan(id string, fn func(*model.Plan) error) (model.Plan, error) {
	if o.org == 0 || o.user == 0 {
		return model.Plan{}, ErrNoUser
	}
	// The store's lock keeps two changes to one plan from interleaving
	// between the read and the write.
	o.s.mu.Lock()
	defer o.s.mu.Unlock()
	var p model.Plan
	err := inTx(o.s.db, func(tx *sql.Tx) error {
		var data string
		err := tx.QueryRow("SELECT data FROM plans WHERE id = ? AND org_id = ? AND user_id = ?", id, o.org, o.user).Scan(&data)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			return err
		}
		created := p.CreatedAt
		if err := fn(&p); err != nil {
			return err
		}
		p.ID, p.CreatedAt, p.UpdatedAt = id, created, time.Now().UTC()
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		launched := 0
		if p.LaunchedAt != nil {
			launched = 1
		}
		_, err = tx.Exec("UPDATE plans SET data = ?, launched = ?, updated_at = ? WHERE id = ?", string(b), launched, p.UpdatedAt.Unix(), id)
		return err
	})
	return p, err
}

// DeletePlan deletes one of the user's plans.
func (o *OrgStore) DeletePlan(id string) error {
	if o.org == 0 || o.user == 0 {
		return ErrNoUser
	}
	o.s.mu.Lock()
	defer o.s.mu.Unlock()
	res, err := o.s.db.Exec("DELETE FROM plans WHERE id = ? AND org_id = ? AND user_id = ?", id, o.org, o.user)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
