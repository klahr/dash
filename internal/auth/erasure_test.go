package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/store"
)

// Someone downloads their data and deletes their own account; a platform
// admin deletes someone else's. Both ask for the password.
func TestExportAndDeleteAccount(t *testing.T) {
	t.Parallel()

	a, h := newServer(t)
	var changed []int64
	a.OnMembersChanged = func(org int64) { changed = append(changed, org) }
	var revoked []string
	a.OnRevoke = func(tk store.Token) { revoked = append(revoked, tk.Name) }
	def, _ := a.acc.DefaultOrg()
	uid := addUser(t, a, def.ID, "kim", store.RoleMember)
	if _, _, err := a.acc.CreateToken(store.NewToken{Kind: store.TokenAPI, Name: "ci", UserID: uid, OrgID: def.ID, Scopes: []string{"read"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	kim := signIn(t, h, "kim", "pw-kim-long")

	w := kim.do("GET", "/api/account/export", "")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Disposition"), `attachment; filename="dash-account-kim-`) {
		t.Fatalf("export: %d %v %s", w.Code, w.Header(), w.Body)
	}
	var x store.AccountExport
	if err := json.Unmarshal(w.Body.Bytes(), &x); err != nil || x.Account.Username != "kim" || len(x.Tokens) != 1 || len(x.Sessions) != 1 {
		t.Fatalf("export: %+v %v", x, err)
	}
	if strings.Contains(w.Body.String(), "password_hash") || strings.Contains(w.Body.String(), "token_hash") {
		t.Fatalf("export has secrets: %s", w.Body)
	}

	if w := kim.do("DELETE", "/api/account", `{"password":"wrong"}`); w.Code != http.StatusForbidden {
		t.Fatalf("wrong password: %d %s", w.Code, w.Body)
	}
	if w := kim.do("DELETE", "/api/account", `{"password":"pw-kim-long"}`); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if fmt.Sprint(revoked) != "[ci]" || fmt.Sprint(changed) != fmt.Sprint([]int64{def.ID}) {
		t.Fatalf("revoked %v, members changed %v", revoked, changed)
	}
	if w := kim.do("GET", "/api/me", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("session after deleting: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/login", `{"username":"kim","password":"pw-kim-long"}`); w.Code == http.StatusOK {
		t.Fatal("signed in to a deleted account")
	}

	// A platform admin deletes lee; lee can't delete anyone.
	lee := addUser(t, a, def.ID, "lee", store.RoleAdmin)
	moe := addUser(t, a, def.ID, "moe", store.RoleMember)
	leeC := signIn(t, h, "lee", "pw-lee-long")
	if w := leeC.do("DELETE", fmt.Sprintf("/api/admin/users/%d", moe), `{"password":"pw-lee-long"}`); w.Code != http.StatusForbidden {
		t.Fatalf("not an admin: %d %s", w.Code, w.Body)
	}
	admin := signIn(t, h, "admin", "s3cret")
	if w := admin.do("DELETE", fmt.Sprintf("/api/admin/users/%d", mustEnv(t, a)), `{"password":"s3cret"}`); w.Code != http.StatusConflict {
		t.Fatalf("own account by the admin route: %d %s", w.Code, w.Body)
	}
	if w := admin.do("DELETE", "/api/account", `{"password":"s3cret"}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "DASH_USERNAME") {
		t.Fatalf("env account: %d %s", w.Code, w.Body)
	}
	if w := admin.do("DELETE", fmt.Sprintf("/api/admin/users/%d", lee), `{"password":"s3cret"}`); w.Code != http.StatusNoContent {
		t.Fatalf("admin deletes lee: %d %s", w.Code, w.Body)
	}
	if w := leeC.do("GET", "/api/me", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("lee's session: %d", w.Code)
	}
	events, _ := a.acc.AuthEvents(def.ID, 10)
	var deleted int
	for _, e := range events {
		if e.Kind == "account.deleted" {
			deleted++
		}
	}
	if deleted != 2 {
		t.Fatalf("log: %+v", events)
	}
}
