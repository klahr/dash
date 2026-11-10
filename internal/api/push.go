package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/klahr/dash/internal/model"
	"github.com/klahr/dash/internal/notify"
)

// Web Push: a browser that turns notifications on subscribes here, so the
// installed app hears about reviews, failures, approvals and failing builds
// while it's closed (internal/notify/webpush.go).
//
//	GET    /api/push                 is it on, and the key to subscribe with
//	PUT    /api/push/subscription    subscribe this browser {endpoint, keys: {p256dh, auth}}
//	DELETE /api/push/subscription    unsubscribe it {endpoint}
//	POST   /api/push/test            push a test notification to it {endpoint}
//
// A subscription is the caller's in their active org: it gets that org's
// projects' notifications.

var errPushOff = errors.New("this Dash doesn't push notifications")

// pushInput is a browser's PushSubscription as its toJSON() gives it.
type pushInput struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// pushCaller is the signed-in person a push request is for (not another
// Dash), or answers r and returns false.
func (s *Server) pushCaller(w http.ResponseWriter, r *http.Request) (Caller, bool) {
	sv, ok := s.service(w, r)
	if !ok {
		return Caller{}, false
	}
	if sv.c.User == 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "push subscriptions are a person's"})
		return Caller{}, false
	}
	return sv.c, true
}

func (s *Server) pushInfo(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pushCaller(w, r); !ok {
		return
	}
	res := map[string]any{"enabled": s.Notify.PushEnabled(), "events": notify.PushEvents}
	if s.Notify.PushEnabled() {
		res["key"] = s.Notify.VAPID.Public
	}
	writeJSON(w, http.StatusOK, res)
}

// checkEndpoint validates a push service URL: https, not too long, and (as
// webhooks) not into this Dash's network unless that's allowed.
func (s *Server) checkEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(raw) > 2048 {
		return errors.Join(errBadRequest, errors.New("the subscription's endpoint must be an https URL"))
	}
	if err := notify.CheckURL(raw, s.Notify.AllowPrivate); err != nil {
		return errors.Join(errBadRequest, err)
	}
	return nil
}

func (s *Server) subscribePush(w http.ResponseWriter, r *http.Request) {
	c, ok := s.pushCaller(w, r)
	if !ok {
		return
	}
	if !s.Notify.PushEnabled() {
		writeErr(w, errPushOff)
		return
	}
	var in pushInput
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.checkEndpoint(in.Endpoint); err != nil {
		writeErr(w, err)
		return
	}
	if err := notify.CheckPushKeys(in.Keys.P256dh, in.Keys.Auth); err != nil {
		writeErr(w, errors.Join(errBadRequest, err))
		return
	}
	sub, err := s.Store.Accounts().SavePushSubscription(model.PushSubscription{
		UserID: c.User, OrgID: c.Org, Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth, UserAgent: r.UserAgent(),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

func (s *Server) unsubscribePush(w http.ResponseWriter, r *http.Request) {
	c, ok := s.pushCaller(w, r)
	if !ok {
		return
	}
	var in pushInput
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.Store.Accounts().DeleteUserPushSubscription(c.User, in.Endpoint); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testPush pushes one test notification to the caller's browser and
// answers how it went.
func (s *Server) testPush(w http.ResponseWriter, r *http.Request) {
	c, ok := s.pushCaller(w, r)
	if !ok {
		return
	}
	if !s.Notify.PushEnabled() {
		writeErr(w, errPushOff)
		return
	}
	var in pushInput
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	sub, err := s.Store.Accounts().PushSubscription(c.User, in.Endpoint)
	if err != nil {
		writeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res := map[string]any{"ok": true}
	if err := s.Notify.TestPush(ctx, sub); err != nil {
		res["ok"], res["error"] = false, err.Error()
	}
	writeJSON(w, http.StatusOK, res)
}
