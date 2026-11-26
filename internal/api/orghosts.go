package api

import (
	"net/http"
	"strings"

	"github.com/klahr/dash/internal/preview"
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

// previewOrigin is where a browser signs in to the preview with host
// label: its task's org's subdomain (only that org's members may open it).
// A runner's preview (<task>-<runner>) is the hub's task's it reported, if
// any; a commit preview (<project>--<runner>) is its project's org's. ""
// without org subdomains, or for a task that isn't known here.
func (s *Server) previewOrigin(label string) string {
	d := s.Auth.Domains
	if d == nil || s.Store == nil {
		return ""
	}
	var org int64
	var ok bool
	if pid, _, isTry := preview.ParseTryLabel(label); isTry {
		org, ok = s.Store.ProjectOrg(pid) // a commit preview: its project's org
	} else {
		todoID := label
		if htid, found := s.hubPreviewTask(label); found {
			todoID = htid
		} else if id, _, found := strings.Cut(label, "-"); found {
			todoID = id
		}
		org, ok = s.Store.TodoOrg(todoID)
	}
	if !ok {
		return ""
	}
	o, err := s.Store.Accounts().Org(org)
	if err != nil || !d.Usable(o.Slug) {
		return ""
	}
	return d.Origin(o.Slug)
}
