package api

import (
	"context"
	"net/http"
	"slices"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/notes"
	"github.com/klahr/dash/internal/tunnel"
)

// Shared notes (package notes) belong to the runners of a hub project. The
// hub only relays them: it keeps no copy, reads none and shows none.

// notesRelay handles POST /runner/notes on a hub: a runner's message for
// its project's other runners, passed on unread to those online now.
func (s *Server) notesRelay(rh *tunnel.Hub) http.Handler {
	return &notes.Relay{
		Route: func(r *http.Request, project string) ([]string, error) {
			x, ok := rh.Authenticate(r)
			if !ok {
				return nil, &notes.RelayError{Status: http.StatusUnauthorized, Msg: "unknown or revoked runner token"}
			}
			org := x.OrgID()
			if x.Owner == nil || org == 0 {
				return nil, &notes.RelayError{Status: http.StatusForbidden, Msg: "this runner works for nobody"}
			}
			p, err := s.Store.ForOrg(org).GetProject(project)
			if o, ok := s.Store.ProjectOrg(project); !ok || o != org || err != nil || !slices.Contains(p.Runners, x.ID) {
				return nil, &notes.RelayError{Status: http.StatusForbidden, Msg: "no such project linked to this runner"}
			}
			var peers []string
			for _, id := range p.Runners {
				if id == x.ID || !rh.Online(id) {
					continue
				}
				if y, ok := rh.Get(id); ok && y.OrgID() == org {
					peers = append(peers, id)
				}
			}
			return peers, nil
		},
		Forward: func(ctx context.Context, peer, project string, msg []byte) ([]byte, error) {
			return rh.CallBytes(ctx, peer, notes.PeerPathFor(project), http.Header{notes.RelayHeader: {"1"}}, msg, notes.MaxReply)
		},
	}
}

// notesFromPeers handles POST /api/hub/projects/{hpid}/notes on a runner: a
// message from another runner of the project, which its hub relayed.
func (s *Server) notesFromPeers(w http.ResponseWriter, r *http.Request) {
	if !auth.FromHub(r.Context()) || s.NotesPeer == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": notes.ErrNoProject.Error()})
		return
	}
	s.NotesPeer.ServeHTTP(w, r)
}
