package api

import (
	"net/http"
	"strconv"
)

// Past chats: New chat (DELETE on a chat) puts the caller's general chat,
// or their chat about a project, aside and starts an empty one; these
// list the ones put aside, open one again (which puts the current one
// aside in its place), delete one and undo that (see store.PastChats).
//
//	GET    /api/chat/past                          [{id, title, at, count}], newest first
//	POST   /api/chat/past/{id}/open                the opened conversation's messages
//	DELETE /api/chat/past/{id}
//	POST   /api/chat/past/{id}/restore             undoes the delete
//
// and the same under /api/projects/{pid}/chat/past for a project's chat.
// A chat's GET says how many it has in X-Chat-Past.
func (s *Server) pastChatRoutes(mux routeMux) {
	for _, base := range []string{"/api/chat/past", "/api/projects/{pid}/chat/past"} {
		mux.HandleFunc("GET "+base, s.pastChats)
		mux.HandleFunc("POST "+base+"/{id}/open", s.openPastChat)
		mux.HandleFunc("DELETE "+base+"/{id}", s.markPastChat(true))
		mux.HandleFunc("POST "+base+"/{id}/restore", s.markPastChat(false))
	}
}

// chatOwner is whose chat r is about, and its project ("" for the general chat).
func chatOwner(r *http.Request, sv *service) (int64, string) {
	if pid := r.PathValue("pid"); pid != "" {
		return chatUser(r, sv.c), pid
	}
	return sv.c.User, ""
}

func (s *Server) pastChats(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	user, pid := chatOwner(r, sv)
	list, err := sv.data.PastChats(user, pid)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) openPastChat(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	user, pid := chatOwner(r, sv)
	if s.chatBusy(sv, user, pid) {
		writeError(w, http.StatusConflict, "Claude is answering in this chat")
		return
	}
	msgs, err := sv.data.OpenPastChat(user, pid, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) markPastChat(del bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sv, ok := s.service(w, r)
		if !ok {
			return
		}
		user, pid := chatOwner(r, sv)
		mark := sv.data.RestorePastChat
		if del {
			mark = sv.data.DeletePastChat
		}
		if err := mark(user, pid, r.PathValue("id")); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// putChatAside handles New chat (DELETE on a chat): see pastChatRoutes.
func (s *Server) putChatAside(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.service(w, r)
	if !ok {
		return
	}
	user, pid := chatOwner(r, sv)
	if s.chatBusy(sv, user, pid) {
		writeError(w, http.StatusConflict, "Claude is answering in this chat")
		return
	}
	if err := sv.data.PutChatAside(user, pid); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// chatBusy reports whether a turn of user's chat about project pid is
// running, which would save its reply into whichever conversation is then
// current. (A general chat's turn is counted until its suggestion is sent,
// after it saved: its client waits for its own turns instead.)
func (s *Server) chatBusy(sv *service, user int64, pid string) bool {
	return pid != "" && s.projectChatBusy(sv.c.Org, user, pid)
}

// pastChatCount says in X-Chat-Past how many past chats user's chat
// (about pid, or general) has.
func pastChatCount(w http.ResponseWriter, sv *service, user int64, pid string) {
	if n, err := sv.data.PastChatCount(user, pid); err == nil && n > 0 {
		w.Header().Set("X-Chat-Past", strconv.Itoa(n))
	}
}
