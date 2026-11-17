package api

import (
	"net/http"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/branding"
)

// The planner role (store.RolePlanner): a product owner who uses Planning
// and their own account, and nothing else. plannerGate enforces it for
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

	// Planning, and its live updates (the stream sends a planner Planning's
	// events only; see eventsWS).
	"GET /ws/events":                                  true,
	"GET /api/planning/cards":                         true,
	"POST /api/planning/cards":                        true,
	"GET /api/planning/cards/{cid}":                   true,
	"PATCH /api/planning/cards/{cid}":                 true,
	"DELETE /api/planning/cards/{cid}":                true,
	"POST /api/planning/cards/{cid}/move":             true,
	"GET /api/planning/cards/{cid}/comments":          true,
	"POST /api/planning/cards/{cid}/comments":         true,
	"DELETE /api/planning/cards/{cid}/comments/{mid}": true,
	"POST /api/planning/cards/{cid}/start":            true,
	"GET /api/planning/projects":                      true,
}

// errPlanner is what a planner gets for any other route.
const errPlanner = "planners can use Planning and their own account only"

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
