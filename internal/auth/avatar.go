package auth

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/klahr/dash/internal/store"
)

// Profile pictures (Account → Profile):
//
//	PUT    /api/account/avatar      the image as the body: PNG, JPEG, GIF or WebP, up to 1 MB
//	DELETE /api/account/avatar      remove it
//	GET    /api/users/{uid}/avatar  the user's picture: their own, or a member's of the session's org
//
// A user's avatar field (in /api/me and the member lists) is the picture's
// version, 0 without one; the web client asks for ?v={avatar}, which may be
// cached for good.

func (a *Auth) avatarRoutes(mux Mux) {
	mux.HandleFunc("PUT /api/account/avatar", a.sessionOnly(a.putAvatar))
	mux.HandleFunc("DELETE /api/account/avatar", a.sessionOnly(a.deleteAvatar))
	mux.HandleFunc("GET /api/users/{uid}/avatar", a.signedIn(a.getAvatar))
}

func (a *Auth) putAvatar(w http.ResponseWriter, r *http.Request, id *Identity) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, store.MaxAvatar))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		err = store.ErrBadAvatar
	}
	if err != nil {
		accountError(w, err)
		return
	}
	v, err := a.acc.SetAvatar(id.User.ID, data, a.now())
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"avatar": v})
}

func (a *Auth) deleteAvatar(w http.ResponseWriter, r *http.Request, id *Identity) {
	if err := a.acc.DeleteAvatar(id.User.ID); err != nil {
		accountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Auth) getAvatar(w http.ResponseWriter, r *http.Request, id *Identity) {
	uid, err := pathID(r, "uid")
	if err != nil {
		accountError(w, err)
		return
	}
	// Someone else's picture is for the members of the org the request is in.
	if uid != id.User.ID {
		if id.Org == nil {
			accountError(w, store.ErrNotFound)
			return
		}
		if _, err := a.acc.Membership(uid, id.Org.ID); err != nil {
			accountError(w, store.ErrNotFound)
			return
		}
	}
	data, ct, v, err := a.acc.Avatar(uid)
	if err != nil {
		accountError(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// A URL with the picture's version always gets the same picture.
	if r.URL.Query().Get("v") == strconv.FormatInt(v, 10) {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "private, no-cache")
	}
	h.Set("ETag", `"`+strconv.FormatInt(v, 10)+`"`)
	if r.Header.Get("If-None-Match") == h.Get("ETag") {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(data)
}
