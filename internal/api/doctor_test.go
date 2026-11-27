package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/doctor"
	"github.com/klahr/dash/internal/tunnel"
)

// The hub's Check again runs a runner's doctor through the tunnel: the
// route answers only the hub, and a runner without checks says so.
func TestRunnerDoctorRoute(t *testing.T) {
	parallel(t)
	s, h, _ := runnerSide(t, 7)
	if w := send(h, "POST", tunnel.DoctorPath, ""); w.Code != http.StatusNotImplemented {
		t.Errorf("no doctor: %d %s", w.Code, w.Body)
	}
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	s.Doctor = func(context.Context) doctor.Report {
		return doctor.Report{At: at, Checks: []doctor.Check{{Group: doctor.GroupRequired, Name: "git", Status: doctor.OK}}}
	}
	w := send(h, "POST", tunnel.DoctorPath, "")
	var rep doctor.Report
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &rep) != nil || !rep.At.Equal(at) || len(rep.Checks) != 1 {
		t.Fatalf("doctor: %d %s", w.Code, w.Body)
	}
	r := asOwner(t, s, httptest.NewRequest("POST", tunnel.DoctorPath, strings.NewReader("")))
	rec := httptest.NewRecorder()
	s.mux().ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("doctor route for a user: %d %s", rec.Code, rec.Body)
	}
}

// On the hub: the runners list carries each runner's doctor report (none
// from one that hasn't sent any), Check again on an offline runner says
// so, and another org's runner isn't found.
func TestHubRunnerDoctor(t *testing.T) {
	parallel(t)
	f := newFleetServer(t)
	if v := runnerNames(t, send(f.u, "GET", "/api/runners", ""))["u-box"]; v.Doctor != nil {
		t.Errorf("doctor before any report: %+v", v.Doctor)
	}
	at := time.Now().UTC().Truncate(time.Second)
	if err := f.rh.SetDoctor(f.ids["u-box"], doctor.Report{At: at, System: "macOS",
		Checks: []doctor.Check{{Group: doctor.GroupFeatures, Name: doctor.CheckChrome, Status: doctor.Warning, Fix: "brew install --cask google-chrome"}}}); err != nil {
		t.Fatal(err)
	}
	// Seen by the org's other members too.
	v := runnerNames(t, send(f.c, "GET", "/api/runners", ""))["u-box"]
	if v.Doctor == nil || !v.Doctor.At.Equal(at) || len(v.Doctor.Checks) != 1 || v.Doctor.Checks[0].Fix != "brew install --cask google-chrome" {
		t.Errorf("doctor in the list: %+v", v.Doctor)
	}
	if w := send(f.c, "POST", "/api/runners/"+f.ids["u-box"]+"/doctor", ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "offline") {
		t.Errorf("check again offline: %d %s", w.Code, w.Body)
	}
	if w := send(f.u, "POST", "/api/runners/"+f.ids["b-box"]+"/doctor", ""); w.Code != http.StatusNotFound {
		t.Errorf("check again on another org's runner: %d %s", w.Code, w.Body)
	}
}
