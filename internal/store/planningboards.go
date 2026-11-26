package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/klahr/dash/internal/model"
)

// ---- Planning boards ----
//
// An org's planning cards are on its boards (docs/planning.md), in the
// order they show. Every org has one default board: the org's cards of
// before boards were there are on it (migration 0040), a card given no
// board goes on it, and the cards of a board that is deleted move to it. An
// org that has none yet gets it when Planning first asks for it. Changes
// are announced to the org (model.EvPlanningBoards).

// ErrBadPlanningBoard: a planning board's change that can't be made (a name
// in use, deleting the default board).
var ErrBadPlanningBoard = errors.New("planning board")

// DefaultBoardName is the name of the board an org starts with.
const DefaultBoardName = "Main"

const planningBoardColumns = "id, name, position, is_default, created_at"

func scanPlanningBoard(sc scanner) (model.CardBoard, error) {
	var b model.CardBoard
	var at int64
	err := sc.Scan(&b.ID, &b.Name, &b.Position, &b.Default, &at)
	b.CreatedAt = time.Unix(at, 0).UTC()
	return b, err
}

// onBoard picks the cards (of planning_cards) on a board (its id, the
// query's argument): planning_card_boards keeps which board a card is on.
const onBoard = "id IN (SELECT card_id FROM planning_card_boards WHERE board_id = ?)"

// setBoard puts a card on a board.
func setBoard(tx execQuerier, cardID, boardID string) error {
	_, err := tx.Exec("INSERT INTO planning_card_boards (card_id, board_id) VALUES (?, ?) ON CONFLICT (card_id) DO UPDATE SET board_id = excluded.board_id", cardID, boardID)
	return err
}

// execQuerier is a database or a transaction.
type execQuerier interface {
	rowsQuerier
	QueryRow(string, ...any) *sql.Row
	Exec(string, ...any) (sql.Result, error)
}

// defaultBoard is the id of the org's default board, which it makes if the
// org has none yet. It is safe without the store's lock: two that make it
// at once make one (planning_boards_default is unique).
func defaultPlanningBoard(q execQuerier, org int64) (string, error) {
	var id string
	err := q.QueryRow("SELECT id FROM planning_boards WHERE org_id = ? AND is_default = 1", org).Scan(&id)
	if !errors.Is(err, sql.ErrNoRows) {
		return id, err
	}
	if _, err := q.Exec(`INSERT OR IGNORE INTO planning_boards (id, org_id, name, position, is_default, created_at)
		VALUES (?, ?, ?, (SELECT COALESCE(MAX(position) + 1, 0) FROM planning_boards WHERE org_id = ?), 1, ?)`,
		NewID(), org, DefaultBoardName, org, time.Now().Unix()); err != nil {
		return "", err
	}
	err = q.QueryRow("SELECT id FROM planning_boards WHERE org_id = ? AND is_default = 1", org).Scan(&id)
	return id, err
}

// PlanningBoards are the org's planning boards, in their order.
func (o *OrgStore) PlanningBoards() ([]model.CardBoard, error) {
	if o.org == 0 {
		return nil, errNoOrg
	}
	if _, err := defaultPlanningBoard(o.s.db, o.org); err != nil {
		return nil, err
	}
	return queryAll(o.s.db, scanPlanningBoard, "SELECT "+planningBoardColumns+" FROM planning_boards WHERE org_id = ? ORDER BY position, created_at, id", o.org)
}

// PlanningBoard is one of the org's boards, named by ref: its id, or its name in
// any case. "" is the default board.
func (o *OrgStore) PlanningBoard(ref string) (model.CardBoard, error) {
	return o.planningBoard(o.s.db, ref)
}

func (o *OrgStore) planningBoard(q execQuerier, ref string) (model.CardBoard, error) {
	if o.org == 0 {
		return model.CardBoard{}, errNoOrg
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		id, err := defaultPlanningBoard(q, o.org)
		if err != nil {
			return model.CardBoard{}, err
		}
		ref = id
	}
	b, err := scanPlanningBoard(q.QueryRow("SELECT "+planningBoardColumns+" FROM planning_boards WHERE org_id = ? AND id = ?", o.org, ref))
	if errors.Is(err, sql.ErrNoRows) {
		b, err = scanPlanningBoard(q.QueryRow("SELECT "+planningBoardColumns+" FROM planning_boards WHERE org_id = ? AND name = ? COLLATE NOCASE ORDER BY position LIMIT 1", o.org, ref))
	}
	if errors.Is(err, sql.ErrNoRows) {
		return model.CardBoard{}, fmt.Errorf("%w: no planning board %q", ErrNotFound, ref)
	}
	return b, err
}

// planningNameFree checks that no other board of the org (than id) has name.
func (o *OrgStore) planningNameFree(q querier, name, id string) error {
	var one int
	err := q.QueryRow("SELECT 1 FROM planning_boards WHERE org_id = ? AND name = ? COLLATE NOCASE AND id != ?", o.org, name, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: there is a board named %q already", ErrBadPlanningBoard, name)
}

// emitPlanningBoards announces a change to the org's boards.
func (o *OrgStore) emitPlanningBoards() {
	if o.s.pub != nil {
		o.s.pub.Publish(model.Event{Type: model.EvPlanningBoards, Data: map[string]any{}, Time: time.Now(), OrgID: o.org})
	}
}

// CreatePlanningBoard adds a board after the org's others.
func (o *OrgStore) CreatePlanningBoard(name string) (model.CardBoard, error) {
	if o.org == 0 {
		return model.CardBoard{}, errNoOrg
	}
	name, err := model.CleanCardBoardName(name)
	if err != nil {
		return model.CardBoard{}, fmt.Errorf("%w: %v", ErrBadPlanningBoard, err)
	}
	b := model.CardBoard{ID: NewID(), Name: name, CreatedAt: time.Now().UTC().Truncate(time.Second)}
	o.s.mu.Lock()
	err = inTx(o.s.db, func(tx *sql.Tx) error {
		if _, err := defaultPlanningBoard(tx, o.org); err != nil {
			return err
		}
		if err := o.planningNameFree(tx, name, ""); err != nil {
			return err
		}
		if err := tx.QueryRow("SELECT COALESCE(MAX(position) + 1, 0) FROM planning_boards WHERE org_id = ?", o.org).Scan(&b.Position); err != nil {
			return err
		}
		_, err := tx.Exec("INSERT INTO planning_boards (id, org_id, name, position, is_default, created_at) VALUES (?, ?, ?, ?, 0, ?)",
			b.ID, o.org, b.Name, b.Position, b.CreatedAt.Unix())
		return err
	})
	o.s.mu.Unlock()
	if err != nil {
		return model.CardBoard{}, err
	}
	o.emitPlanningBoards()
	return b, nil
}

// RenamePlanningBoard gives one of the org's boards a new name.
func (o *OrgStore) RenamePlanningBoard(id, name string) (model.CardBoard, error) {
	name, err := model.CleanCardBoardName(name)
	if err != nil {
		return model.CardBoard{}, fmt.Errorf("%w: %v", ErrBadPlanningBoard, err)
	}
	var b model.CardBoard
	o.s.mu.Lock()
	err = inTx(o.s.db, func(tx *sql.Tx) error {
		var err error
		if b, err = o.planningBoardByID(tx, id); err != nil {
			return err
		}
		if err := o.planningNameFree(tx, name, b.ID); err != nil {
			return err
		}
		b.Name = name
		_, err = tx.Exec("UPDATE planning_boards SET name = ? WHERE id = ?", name, b.ID)
		return err
	})
	o.s.mu.Unlock()
	if err != nil {
		return model.CardBoard{}, err
	}
	o.emitPlanningBoards()
	return b, nil
}

// SetDefaultPlanningBoard makes one of the org's boards its default one: the
// board a card given no board goes on, and a deleted board's cards move to.
// The board that was the default stays, as an ordinary board.
func (o *OrgStore) SetDefaultPlanningBoard(id string) (model.CardBoard, error) {
	var b model.CardBoard
	o.s.mu.Lock()
	err := inTx(o.s.db, func(tx *sql.Tx) error {
		var err error
		if b, err = o.planningBoardByID(tx, id); err != nil {
			return err
		}
		if b.Default {
			return nil
		}
		// One default an org (planning_boards_default): the old one first.
		if _, err := tx.Exec("UPDATE planning_boards SET is_default = 0 WHERE org_id = ? AND is_default = 1", o.org); err != nil {
			return err
		}
		b.Default = true
		_, err = tx.Exec("UPDATE planning_boards SET is_default = 1 WHERE id = ?", b.ID)
		return err
	})
	o.s.mu.Unlock()
	if err != nil {
		return model.CardBoard{}, err
	}
	o.emitPlanningBoards()
	return b, nil
}

// planningBoardByID is one of the org's boards, by its id only.
func (o *OrgStore) planningBoardByID(q querier, id string) (model.CardBoard, error) {
	b, err := scanPlanningBoard(q.QueryRow("SELECT "+planningBoardColumns+" FROM planning_boards WHERE org_id = ? AND id = ?", o.org, id))
	if errors.Is(err, sql.ErrNoRows) {
		return b, fmt.Errorf("%w: no planning board %q", ErrNotFound, id)
	}
	return b, err
}

// MovePlanningBoard puts one of the org's boards at position among them (0 is the
// first; past the end is the last), and renumbers the others.
func (o *OrgStore) MovePlanningBoard(id string, position int) (model.CardBoard, error) {
	var b model.CardBoard
	o.s.mu.Lock()
	err := inTx(o.s.db, func(tx *sql.Tx) error {
		var err error
		if b, err = o.planningBoardByID(tx, id); err != nil {
			return err
		}
		ids, err := queryAll(tx, scanOne[string], "SELECT id FROM planning_boards WHERE org_id = ? AND id != ? ORDER BY position, created_at, id", o.org, id)
		if err != nil {
			return err
		}
		position = max(0, min(position, len(ids)))
		ids = slices.Insert(ids, position, id)
		for i, x := range ids {
			if _, err := tx.Exec("UPDATE planning_boards SET position = ? WHERE id = ?", i, x); err != nil {
				return err
			}
		}
		b.Position = position
		return nil
	})
	o.s.mu.Unlock()
	if err != nil {
		return model.CardBoard{}, err
	}
	o.emitPlanningBoards()
	return b, nil
}

// DeletePlanningBoard deletes one of the org's boards, not its default one. Its
// cards move to the default board, after the cards of each column there.
func (o *OrgStore) DeletePlanningBoard(id string) error {
	o.s.mu.Lock()
	err := inTx(o.s.db, func(tx *sql.Tx) error {
		b, err := o.planningBoardByID(tx, id)
		if err != nil {
			return err
		}
		if b.Default {
			return fmt.Errorf("%w: the default board can't be deleted", ErrBadPlanningBoard)
		}
		def, err := defaultPlanningBoard(tx, o.org)
		if err != nil {
			return err
		}
		for _, col := range model.CardColumns {
			var end int
			if err := tx.QueryRow("SELECT COALESCE(MAX(position) + 1, 0) FROM planning_cards WHERE org_id = ? AND "+onBoard+" AND col = ?", o.org, def, col).Scan(&end); err != nil {
				return err
			}
			ids, err := queryAll(tx, scanOne[string], "SELECT id FROM planning_cards WHERE org_id = ? AND "+onBoard+" AND col = ? ORDER BY position, created_at, id", o.org, id, col)
			if err != nil {
				return err
			}
			for i, x := range ids {
				if _, err := tx.Exec("UPDATE planning_cards SET position = ? WHERE id = ?", end+i, x); err != nil {
					return err
				}
				if err := setBoard(tx, x, def); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec("DELETE FROM planning_boards WHERE id = ?", id); err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE planning_boards SET position = position - 1 WHERE org_id = ? AND position > ?", o.org, b.Position)
		return err
	})
	o.s.mu.Unlock()
	if err != nil {
		return err
	}
	o.emitPlanningBoards() // its cards' new board too: Planning reads them all again
	return nil
}

// PlanningBoardCounts is how many cards each of the org's boards has.
func (o *OrgStore) PlanningBoardCounts() (map[string]int, error) {
	return queryMap(o.s.db, scanCount, "SELECT cb.board_id, count(*) FROM planning_card_boards cb JOIN planning_cards c ON c.id = cb.card_id WHERE c.org_id = ? GROUP BY cb.board_id", o.org)
}
