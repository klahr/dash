package api

import (
	"net/http"
	"testing"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/mcptools"
	"github.com/klahr/dash/internal/store"
)

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
	return Caller{User: sess.User.ID, Org: sess.Org.ID, OrgSlug: sess.Org.Slug, PlatformAdmin: sess.User.PlatformAdmin}
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
