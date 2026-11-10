package api

import (
	"net/http"
	"os"
	"testing"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/gitops"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/passhash"
	"github.com/klahr/dash/internal/store"
)

// TestMain makes password hashing cheap (nearly every test signs someone
// in, and the real Argon2id parameters were most of this package's run
// time) and lets runners link to local paths, the tests' remotes. Tests run
// in parallel (see boardServer), so neither changes per test.
func TestMain(m *testing.M) {
	passhash.UseWeakParamsForTests()
	gitops.AllowLocalRemotes = true
	os.Exit(m.Run())
}

// raw is the whole store behind s, for tests that set up or inspect records
// across orgs.
func raw(s *Server) *store.Store { return s.Store.(*store.Store) }

// ownerSession is the first account (boardServer's "u") in the default org.
func ownerSession(t *testing.T, s *Server) store.Session {
	t.Helper()
	acc := raw(s).Accounts()
	u, err := acc.EnvUser()
	if err != nil {
		t.Fatal(err)
	}
	org, err := acc.DefaultOrg()
	if err != nil {
		t.Fatal(err)
	}
	m, err := acc.Membership(u.ID, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	return store.Session{User: u, Org: &m}
}

// ownerCaller is ownerSession as a Caller.
func ownerCaller(t *testing.T, s *Server) Caller {
	sess := ownerSession(t, s)
	return Caller{User: sess.User.ID, Org: sess.Org.ID, OrgFolder: folderOf(sess.Org.Org), PlatformAdmin: sess.User.PlatformAdmin}
}

// chatCaller is the owner, for chat-run tokens.
func chatCaller(t *testing.T, s *Server) mcptools.Caller {
	c := ownerCaller(t, s)
	return mcptools.Caller{Org: c.Org, User: c.User}
}

// asOwner makes r a request of the owner.
func asOwner(t *testing.T, s *Server, r *http.Request) *http.Request {
	return r.WithContext(auth.WithSession(r.Context(), ownerSession(t, s)))
}
