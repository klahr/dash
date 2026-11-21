package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/branding"
	"github.com/klahr/dash/internal/store"
)

// The planner role (store.RolePlanner): a product owner who uses Planning
// and their own account, and, with a runner of their own, Claude for the
// general chat and feature briefs, and nothing else. plannerGate enforces it for
// every route, in one place: a planner's request reaches a route only if
// its pattern is in plannerRoutes; everything else (projects, tasks,
// chats, terminals, runners, previews, scans, the MCP tools, other events)
// is refused with 403. A new route is therefore refused to planners until
// it is added here (TestPlannerGateRefusesEveryOtherRoute walks them all).

// plannerRoutes are the route patterns, as registered, a planner may use.
var plannerRoutes = map[string]bool{
	// The page and its files, the logo and legal pages, the version.
	"GET /":                       true,
	"GET " + branding.LogoPath:    true,
	"GET " + branding.TermsPath:   true,
	"GET " + branding.PrivacyPath: true,
	"GET /api/version":            true,

	// Signing in and out, switching orgs and invitations.
	"POST /api/login":                true,
	"POST /api/login/totp":           true,
	"POST /api/logout":               true,
	"GET /api/me":                    true,
	"GET /api/orgs":                  true,
	"POST /api/auth/switch-org":      true,
	"POST /api/invitations/lookup":   true,
	"POST /api/invitations/accept":   true,
	"POST /api/invitations/decline":  true,
	"GET " + auth.GoogleStartPath:    true,
	"POST " + auth.GoogleStartPath:   true,
	"GET " + auth.GoogleCallbackPath: true,

	// Their own account.
	"GET /api/account/2fa":                 true,
	"POST /api/account/2fa/setup":          true,
	"POST /api/account/2fa/enable":         true,
	"POST /api/account/2fa/disable":        true,
	"POST /api/account/2fa/recovery-codes": true,
	"PUT /api/account/avatar":              true,
	"DELETE /api/account/avatar":           true,
	"GET /api/users/{uid}/avatar":          true,
	"GET /api/account/google":              true,
	"DELETE /api/account/google":           true,
	"GET /api/account/export":              true,
	"DELETE /api/account":                  true,

	// Their notifications: card news (mentions, added to a card, its
	// work), pushed to their browsers. No summary or personal webhooks:
	// those are about tasks, which planners don't see.
	"GET /api/me/notifications":     true,
	"PUT /api/me/notifications":     true,
	"GET /api/push":                 true,
	"PUT /api/push/subscription":    true,
	"DELETE /api/push/subscription": true,
	"POST /api/push/test":           true,

	// Planning, and its live updates (the stream sends a planner Planning's
	// events only; see eventsWS).
	"GET /ws/events":                                   true,
	"GET /api/planning/cards":                          true,
	"POST /api/planning/cards":                         true,
	"GET /api/planning/cards/{cid}":                    true,
	"PATCH /api/planning/cards/{cid}":                  true,
	"DELETE /api/planning/cards/{cid}":                 true,
	"POST /api/planning/cards/{cid}/move":              true,
	"GET /api/planning/cards/{cid}/comments":           true,
	"POST /api/planning/cards/{cid}/comments":          true,
	"DELETE /api/planning/cards/{cid}/comments/{mid}":  true,
	"POST /api/planning/cards/{cid}/start":             true,
	"POST /api/planning/cards/{cid}/files":             true,
	"GET /api/planning/cards/{cid}/files/{fid}/{name}": true,
	"DELETE /api/planning/cards/{cid}/files/{fid}":     true,
	"GET /api/planning/projects":                       true,
	"GET /api/planning/projects/{pid}/icon":            true,
	"GET /api/planning/people":                         true,
	"GET /api/planning/boards":                         true,
	"POST /api/planning/boards":                        true,
	"PATCH /api/planning/boards/{bid}":                 true,
	"DELETE /api/planning/boards/{bid}":                true,

	// Their own runners (and no one else's: see sees), which run only their
	// general chat and feature brief interviews with their own Claude login
	// (tunnel.Runner.ClaudeOnly): pairing one, renaming and removing it.
	"GET /api/runners":         true,
	"POST /api/runners/codes":  true,
	"PATCH /api/runners/{id}":  true,
	"DELETE /api/runners/{id}": true,
	"GET /api/claude":          true,

	// The general chat, on their runner, with Planning's tools only (see
	// plannerBoard); project, task and goal chats stay members' (chatWS).
	"GET /ws/chat":     true,
	"GET /api/chat":    true,
	"DELETE /api/chat": true,

	// Write a feature brief, on their runner. Not a project's brief, which
	// launches a project (createPlan refuses it to planners).
	"GET /api/plans":                 true,
	"POST /api/plans":                true,
	"GET /api/plans/{id}":            true,
	"PATCH /api/plans/{id}":          true,
	"DELETE /api/plans/{id}":         true,
	"POST /api/plans/{id}/interview": true,
	"POST /api/plans/{id}/feature":   true,
	"GET /api/org/planner-settings":  true,

	// Deploys, to look at: what is live where, the deploys and the
	// environments' settings history. Deploying, approving and changing
	// settings stay refused (and their roles rank a planner below them all).
	"GET /api/projects/{pid}/environments":         true,
	"GET /api/projects/{pid}/environments/history": true,
	"GET /api/projects/{pid}/deploys":              true,
	"GET /api/deploys/{id}":                        true,
}

// errPlanner is what a planner gets for any other route.
const errPlanner = "planners can use Planning, their own account and runners, the general chat and feature briefs, and see deploys, only"

// planner reports whether r is a planner's (a session or token of someone
// whose role in the org they work in is planner).
func planner(r *http.Request) bool {
	id, ok := auth.From(r.Context())
	return ok && id.Org != nil && id.Org.Role.Planner()
}

// plannerGate refuses a planner's request for any route that isn't in
// plannerRoutes. Everyone else passes. It runs after sign-in (it needs the
// identity) and looks the route up in the API's mux.
func (s *Server) plannerGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if planner(r) {
			if _, pattern := s.mux().Handler(r); !plannerRoutes[pattern] {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": errPlanner, "code": "planner"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// routeMux is where routes are registered: the API's mux, recording each
// pattern (see recordingMux).
type routeMux interface {
	Handle(pattern string, handler http.Handler)
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// recordingMux registers routes on a ServeMux and records their patterns,
// so the planner gate's test can walk every route.
type recordingMux struct {
	*http.ServeMux
	patterns *[]string
}

func (m recordingMux) Handle(pattern string, h http.Handler) {
	*m.patterns = append(*m.patterns, pattern)
	m.ServeMux.Handle(pattern, h)
}

func (m recordingMux) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	*m.patterns = append(*m.patterns, pattern)
	m.ServeMux.HandleFunc(pattern, h)
}

// plannerSettingsRoutes are the org's settings for its planners: GET for
// everyone in the org (a planner's feature brief sheet says what its rounds
// are sent), PUT for owners.
//
//	GET /api/org/planner-settings → {"project_context": bool}
//	PUT /api/org/planner-settings {"project_context": bool}
//
// project_context: planners' feature brief rounds are sent the project's
// CLAUDE.md, README and file tree, as members' are (on by default); off,
// only the card and the project's name.
func (s *Server) plannerSettingsRoutes(mux routeMux) {
	type settings struct {
		ProjectContext *bool `json:"project_context"`
	}
	mux.HandleFunc("GET /api/org/planner-settings", func(w http.ResponseWriter, r *http.Request) {
		sv, ok := s.service(w, r)
		if !ok {
			return
		}
		on, err := s.Store.Accounts().PlannerContext(sv.c.Org)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, settings{&on})
	})
	mux.HandleFunc("PUT /api/org/planner-settings", func(w http.ResponseWriter, r *http.Request) {
		sv, ok := s.service(w, r)
		if !ok {
			return
		}
		var req settings
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		if req.ProjectContext == nil {
			writeErr(w, fmt.Errorf("%w: project_context is required", errBadRequest))
			return
		}
		err := s.Store.Accounts().SetPlannerContext(sv.c.User, sv.c.Org, *req.ProjectContext)
		if errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrNotMember) {
			writeError(w, http.StatusForbidden, "only an owner of the organization can change that")
			return
		}
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, settings{req.ProjectContext})
	})
}
