package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/klahr/dash/internal/store"
)

func pngBytes(t *testing.T, w, h int) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// Someone sets a profile picture; members of their org see it, others
// don't; a new one gets a new version; removing it removes it.
func TestAvatar(t *testing.T) {
	t.Parallel()

	a, h := newServer(t)
	def, _ := a.acc.DefaultOrg()
	kimID := addUser(t, a, def.ID, "kim", store.RoleMember)
	addUser(t, a, def.ID, "lee", store.RoleMember)
	env := mustEnv(t, a)
	other, err := a.acc.CreateOrg(env, "Other")
	if err != nil {
		t.Fatal(err)
	}
	addUser(t, a, other.ID, "zed", store.RoleMember)
	kim := signIn(t, h, "kim", "pw-kim-long")
	lee := signIn(t, h, "lee", "pw-lee-long")
	zed := signIn(t, h, "zed", "pw-zed-long")
	path := fmt.Sprintf("/api/users/%d/avatar", kimID)

	if w := kim.do("GET", path, ""); w.Code != http.StatusNotFound {
		t.Fatalf("no picture yet: %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{"", "not an image", "<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>", pngBytes(t, 5000, 1)} {
		if w := kim.do("PUT", "/api/account/avatar", bad); w.Code != http.StatusBadRequest {
			t.Fatalf("bad picture %.20q: %d %s", bad, w.Code, w.Body)
		}
	}
	if w := kim.do("PUT", "/api/account/avatar", strings.Repeat("x", store.MaxAvatar+1)); w.Code != http.StatusBadRequest {
		t.Fatalf("too big: %d %s", w.Code, w.Body)
	}

	pic := pngBytes(t, 8, 8)
	w := kim.do("PUT", "/api/account/avatar", pic)
	var put struct{ Avatar int64 }
	decode(t, w, &put)
	if w.Code != http.StatusOK || put.Avatar == 0 {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	var me meResp
	decode(t, kim.do("GET", "/api/me", ""), &me)
	if me.User.Avatar != put.Avatar {
		t.Fatalf("me: avatar %d, want %d", me.User.Avatar, put.Avatar)
	}

	// Members of the org see it, cached for good under its version.
	w = lee.do("GET", fmt.Sprintf("%s?v=%d", path, put.Avatar), "")
	if w.Code != http.StatusOK || w.Body.String() != pic || w.Header().Get("Content-Type") != "image/png" ||
		!strings.Contains(w.Header().Get("Cache-Control"), "immutable") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("lee gets kim's: %d %v", w.Code, w.Header())
	}
	var members struct{ Members []store.Member }
	decode(t, lee.do("GET", fmt.Sprintf("/api/orgs/%d/members", def.ID), ""), &members)
	var listed bool
	for _, m := range members.Members {
		listed = listed || m.ID == kimID && m.Avatar == put.Avatar
	}
	if !listed {
		t.Fatalf("members: %+v", members.Members)
	}
	if w := zed.do("GET", path, ""); w.Code != http.StatusNotFound {
		t.Fatalf("someone in another org: %d", w.Code)
	}

	// A new picture, a new version.
	w = kim.do("PUT", "/api/account/avatar", pngBytes(t, 4, 4))
	var again struct{ Avatar int64 }
	decode(t, w, &again)
	if again.Avatar <= put.Avatar {
		t.Fatalf("new version %d after %d", again.Avatar, put.Avatar)
	}

	w = kim.do("GET", "/api/account/export", "")
	var x store.AccountExport
	if err := json.Unmarshal(w.Body.Bytes(), &x); err != nil || !strings.HasPrefix(x.Account.ProfilePicture, "data:image/png;base64,") {
		t.Fatalf("export: %.200s %v", w.Body, err)
	}

	if w := kim.do("DELETE", "/api/account/avatar", ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	me = meResp{}
	decode(t, kim.do("GET", "/api/me", ""), &me)
	if me.User.Avatar != 0 {
		t.Fatalf("me after delete: %d", me.User.Avatar)
	}
	if w := lee.do("GET", path, ""); w.Code != http.StatusNotFound {
		t.Fatalf("after delete: %d", w.Code)
	}
}
