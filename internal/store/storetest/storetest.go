// Package storetest opens throwaway stores for other packages' tests.
package storetest

import (
	"path/filepath"
	"testing"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/store"
)

// Accounts opens a new database whose first user is "admin", owner of the
// default org, and returns its accounts and that user in that org.
func Accounts(t testing.TB) (*store.Accounts, model.Owner) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dash.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return Bootstrap(t, st)
}

// Bootstrap makes "admin" the first user of st, owner of the default org.
func Bootstrap(t testing.TB, st *store.Store) (*store.Accounts, model.Owner) {
	t.Helper()
	acc := st.Accounts()
	if err := acc.Bootstrap("admin", "s3cret-password"); err != nil {
		t.Fatal(err)
	}
	u, err := acc.EnvUser()
	if err != nil {
		t.Fatal(err)
	}
	org, err := acc.DefaultOrg()
	if err != nil {
		t.Fatal(err)
	}
	return acc, model.Owner{User: u.ID, Org: org.ID}
}
