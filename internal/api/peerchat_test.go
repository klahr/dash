package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/klahr/dash/internal/auth"
	"github.com/klahr/dash/internal/peer"
	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/tunnel"
)

// A chat the browser opens on a runner through the hub (/peer/<runner>/ws/chat)
// reaches the runner for the logged-in user: it runs for the runner's owner,
// is refused for another hub user, and an X-Dash-User the browser sends
// changes neither.
func TestRunnerChatThroughTheHubIsForTheLoggedInUser(t *testing.T) {
	parallel(t)

	hub, _, _ := boardServer(t)
	rh := &tunnel.Hub{Accounts: hub.Auth.Accounts()}
	hub.Peers = &peer.Hub{Self: "hub", Runners: rh}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(hub.Routes())
	defer srv.Close()

	owner := testOwner(t, hub)
	code, _, err := rh.NewCode(owner)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := tunnel.Register(ctx, srv.URL, code, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	rs, runnerAPI, _ := runnerSide(t, creds.OrgID)
	rs.HubUser = creds.UserID
	rs.ChatDir = filepath.Join(t.TempDir(), "chat")
	chat := &echoChat{}
	rs.Chat = chat
	client := &tunnel.Client{Creds: creds, Handler: runnerAPI, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	go func() { _ = client.Run(ctx) }()
	eventually(t, "the runner to come online", func() bool { r, _ := rh.Get(creds.ID); return r.Online })

	ownerSess := sessionOf(t, hub, owner.User, owner.Org)
	otherSess := ownerSess // another platform admin of the hub
	otherSess.User.ID = owner.User + 1000
	var sess store.Session
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.mux().ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), sess)))
	}))
	defer hs.Close()

	ask := func(forged string) (last, text string) {
		t.Helper()
		hdr := http.Header{}
		if forged != "" {
			hdr.Set(tunnel.UserHeader, forged)
		}
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hs.URL, "http")+"/peer/laptop/ws/chat", hdr)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.Close()
		if err := ws.WriteJSON(map[string]any{"type": "send", "messages": []map[string]string{{"role": "user", "content": "hello"}}}); err != nil {
			t.Fatal(err)
		}
		_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			var d struct{ Kind, Text string }
			if err := ws.ReadJSON(&d); err != nil {
				t.Fatal(err)
			}
			text += d.Text
			if d.Kind == "done" || d.Kind == "error" {
				return d.Kind, text
			}
		}
	}
	ownerID := strconv.FormatInt(owner.User, 10)

	// Another hub user is refused, even claiming to be the owner.
	sess = otherSess
	for _, forged := range []string{"", ownerID} {
		if last, text := ask(forged); last != "error" || !strings.Contains(text, "registered it") {
			t.Fatalf("chat for another user (claiming %q): %s %q", forged, last, text)
		}
	}
	if chat.calls.Load() != 0 {
		t.Fatal("Claude ran for someone other than the runner's owner")
	}

	// The owner's chat runs, whatever the browser claims.
	sess = ownerSess
	for _, forged := range []string{"", strconv.FormatInt(owner.User+1000, 10)} {
		if last, text := ask(forged); last != "done" || text != "hi" {
			t.Fatalf("owner's chat (claiming %q): %s %q", forged, last, text)
		}
	}
}
