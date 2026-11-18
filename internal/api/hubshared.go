package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/claudecli"
	"github.com/klahr/dash/internal/localrun"
)

// A runner's own check of whose work its hub sends it (see tunnel/shared.go
// for the hub's): every start of work from the hub says whom it is for. Work
// for anyone but the user who registered the runner (HubUser) is other
// members' work: the runner takes it only while its hub's setting Take other
// members' tasks is on and it runs Claude with an API key and no
// subscription login (Server.Shared.Check), and then runs Claude and the
// project's commands with that key alone (claudecli.SharedEnv). The hub
// decides where work goes; this makes sure it never runs on the owner's
// subscription even if the hub is wrong.

// errNotShared is why a runner refuses other members' work.
var errNotShared = errors.New("this runner only does its owner's work")

// othersWork reports whether work for users, sent by the hub, is for
// another member than this runner's owner. Users not given (an older hub)
// or an owner not known (an older registration) count as someone else.
func (s *Server) othersWork(users []int64) bool {
	return s.HubUser == 0 || len(users) == 0 || slices.ContainsFunc(users, func(u int64) bool { return u != s.HubUser })
}

// sharedOK says why this runner may not do other members' work now (nil:
// it may).
func (s *Server) sharedOK() error {
	if s.Shared == nil || s.Shared.Check == nil || s.Shared.Dir == "" {
		return errNotShared
	}
	if err := s.Shared.Check(); err != nil {
		return fmt.Errorf("%w: %v", errNotShared, err)
	}
	return nil
}

// refuseOthers checks work for users from the hub: nil and shared=false
// for its owner's, shared=true for other members' work it may take, or why
// it refuses it (logged).
func (s *Server) refuseOthers(users []int64, what string) (shared bool, err error) {
	if !s.othersWork(users) {
		return false, nil
	}
	if err := s.sharedOK(); err != nil {
		slog.Warn("runner: refused other members' work", "what", what, "for", users, "owner", s.HubUser, "err", err)
		return true, err
	}
	return true, nil
}

// requestShared is refuseOthers for a request from the hub that says whom
// it is for in tunnel.UserHeader (a project command, say). A request not
// from the hub is this computer's own.
func (s *Server) requestShared(r *http.Request, what string) (shared bool, err error) {
	if !auth.FromHub(r.Context()) {
		return false, nil
	}
	var users []int64
	if uid := hubUser(r); uid != 0 {
		users = []int64{uid}
	}
	return s.refuseOthers(users, what)
}

// sharedEnv is the environment of a project command for other members'
// work (claudecli.SharedEnv), with extra.
func (s *Server) sharedEnv(extra ...string) []string {
	return claudecli.SharedEnv(localrun.Environ(extra...), s.Shared.Dir)
}
