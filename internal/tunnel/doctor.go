package tunnel

import (
	"slices"

	"github.com/klahr/dash/internal/doctor"
)

// A runner's doctor: its setup checks (package doctor), which it runs when
// it starts, about hourly and when asked (DoctorPath), and sends with its
// reports. The hub keeps the latest per runner in runners.json, so a runner
// that is offline still shows what it last found.

// DoctorPath is POST on a runner: run the doctor checks now and answer
// with the report (doctor.Report). An older runner refuses it.
const DoctorPath = "/api/hub/doctor"

// SetDoctor keeps rep as runner id's latest checks (cleaned: doctor.Clean),
// unless it isn't newer than the one kept.
func (h *Hub) SetDoctor(id string, rep doctor.Report) error {
	h.mu.Lock()
	if !h.registeredLocked(id) {
		h.mu.Unlock()
		return ErrUnknownName
	}
	changed := h.setDoctorLocked(id, rep)
	h.mu.Unlock()
	if changed {
		h.changed()
	}
	return nil
}

// setDoctorLocked keeps rep for runner id, saving runners.json, when it is
// newer than the one kept; it reports whether it was. h.mu must be held.
func (h *Hub) setDoctorLocked(id string, rep doctor.Report) bool {
	i := slices.IndexFunc(h.runners, func(r *record) bool { return r.ID == id })
	if i < 0 || rep.At.IsZero() {
		return false
	}
	r := h.runners[i]
	if r.Doctor != nil && !rep.At.After(r.Doctor.At) {
		return false
	}
	clean := doctor.Clean(rep)
	r.Doctor = &clean
	h.saveLocked()
	return true
}

// Has reports whether the runner's doctor found feature check name (e.g.
// doctor.CheckChrome, doctor.CheckGH) working. A runner that hasn't
// reported its checks doesn't have it.
func (r Runner) Has(name string) bool {
	ok, _ := r.Doctor.Has(name)
	return ok
}

// MissingFor are the tools hub project id's commands use that the runner's
// doctor found missing (none: none, or it doesn't say).
func (r Runner) MissingFor(project string) []string {
	return r.Doctor.ProjectMissing(project)
}
