package api

import (
	"net/http"

	"github.com/klahr/dash/internal/runnerctl"
)

// hubURL is the hub's address as runners should be told it: with org
// subdomains, the org's subdomain the request came to (so the runner's
// install command and registration name that org's address), else the
// public URL or the request's origin.
func (s *Server) hubURL(r *http.Request) (string, error) {
	if origin, ok := s.Auth.OrgOrigin(r); ok {
		return origin, nil
	}
	return runnerctl.HubURL(r, s.PublicURL, s.Auth.Secure(r), s.Auth.TrustProxy)
}

// previewOrigin is where a browser signs in to todoID's preview: its org's
// subdomain (only that org's members may open it). "" without org
// subdomains, or for a todo that isn't known here.
func (s *Server) previewOrigin(todoID string) string {
	d := s.Auth.Domains
	if d == nil || s.Store == nil {
		return ""
	}
	org, ok := s.Store.TodoOrg(todoID)
	if !ok {
		return ""
	}
	o, err := s.Store.Accounts().Org(org)
	if err != nil || !d.Usable(o.Slug) {
		return ""
	}
	return d.Origin(o.Slug)
}
