package auth

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/store"
)

func lockdownBodyJSON(steps, extra string) string {
	return `{` + steps + `,"confirm":"LOCKDOWN","password":"s3cret"` + extra + `}`
}

func TestLockdownIsPlatformAdminsOnly(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	bea, ada := e.as(t, "bea"), e.as(t, "ada")
	anon := &client{t: t, h: e.h}
	body := lockdownBodyJSON(`"sessions":true,"passwords":true,"totp":true`, "")
	routes := [][3]string{
		{"GET", "/api/admin/lockdown", ""},
		{"POST", "/api/admin/lockdown", body},
		{"GET", "/api/admin/lockdown/users.csv", ""},
		{"DELETE", "/api/admin/notice", ""},
	}
	for _, rt := range routes {
		for name, c := range map[string]*client{"member": bea, "org admin": ada} {
			if w := c.do(rt[0], rt[1], rt[2]); w.Code != http.StatusForbidden {
				t.Errorf("%s %s as %s: %d %s", rt[0], rt[1], name, w.Code, w.Body)
			}
		}
		if w := anon.do(rt[0], rt[1], rt[2]); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s signed out: %d", rt[0], rt[1], w.Code)
		}
	}
	// Nobody was signed out.
	if w := bea.do("GET", "/api/projects", ""); w.Code != http.StatusOK {
		t.Errorf("bea after the refusals: %d", w.Code)
	}
}

func TestLockdownConfirms(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	admin := e.as(t, "admin")
	for name, body := range map[string]string{
		"no LOCKDOWN":    `{"sessions":true,"confirm":"lockdown","password":"s3cret"}`,
		"wrong password": `{"sessions":true,"confirm":"LOCKDOWN","password":"nope"}`,
		"no steps":       lockdownBodyJSON(`"sessions":false`, ""),
		"no notice text": lockdownBodyJSON(`"sessions":true`, `,"banner":true`),
	} {
		if w := admin.do("POST", "/api/admin/lockdown", body); w.Code == http.StatusOK {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if w := admin.do("POST", "/api/admin/lockdown", lockdownBodyJSON(`"secrets":true`, "")); w.Code != http.StatusConflict {
		t.Errorf("secrets without the secret key: %d %s", w.Code, w.Body)
	}
	if n, _ := e.a.acc.LockdownPreview(""); n.Sessions == 0 {
		t.Error("a refused lockdown signed people out")
	}
}

func TestLockdownSignsOutEveryoneButTheAdmin(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	bea := e.as(t, "bea")
	tok, _ := bea.newToken(e.org, `["read"]`, "pw-bea-long")
	admin := e.as(t, "admin")
	var resp struct {
		Result    store.LockdownResult
		Checklist []store.ChecklistItem
	}
	w := admin.do("POST", "/api/admin/lockdown", lockdownBodyJSON(`"sessions":true`, ""))
	decode(t, w, &resp)
	if w.Code != http.StatusOK || resp.Result.Sessions != 1 || resp.Result.APITokens != 1 || resp.Result.Kept != "admin" {
		t.Fatalf("lockdown: %d %s", w.Code, w.Body)
	}
	if w := bea.do("GET", "/api/projects", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("bea's session: %d", w.Code)
	}
	if w := withBearer(e.h, tok, "GET", "/api/projects", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("bea's token: %d", w.Code)
	}
	if w := admin.do("GET", "/api/projects", ""); w.Code != http.StatusOK {
		t.Errorf("the admin was signed out: %d", w.Code)
	}
	var kinds []string
	for _, k := range []string{"admin.lockdown.run", "admin.lockdown.sessions"} {
		if !auditHas(t, e.a, k) {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) > 0 {
		t.Errorf("not in the audit log: %v", kinds)
	}
}

// auditHas reports whether the instance's audit log has an entry of kind.
func auditHas(t *testing.T, a *Auth, kind string) bool {
	t.Helper()
	es, err := a.acc.LockdownEvents(100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func TestLockdownForcesANewPassword(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	bea := e.as(t, "bea")
	tok, _ := bea.newToken(e.org, `["read"]`, "pw-bea-long")
	admin := e.as(t, "admin")
	if w := admin.do("POST", "/api/admin/lockdown", lockdownBodyJSON(`"passwords":true`, "")); w.Code != http.StatusOK {
		t.Fatalf("lockdown: %d %s", w.Code, w.Body)
	}
	// bea's session (still signed in) and token are refused until then.
	w := bea.do("GET", "/api/projects", "")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), codeNeedsPassword) {
		t.Errorf("bea's session on the API: %d %s", w.Code, w.Body)
	}
	if w := bea.do("GET", "/", ""); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login.html?change=1" {
		t.Errorf("bea's session on a page: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := withBearer(e.h, tok, "GET", "/api/projects", ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), codeNeedsPassword) {
		t.Errorf("bea's token: %d %s", w.Code, w.Body)
	}
	var me struct {
		NeedsPassword bool `json:"needs_password"`
	}
	decode(t, bea.do("GET", "/api/me", ""), &me)
	if !me.NeedsPassword {
		t.Error("/api/me doesn't say so")
	}
	// Signing in again says so too.
	fresh := &client{t: t, h: e.h}
	w = fresh.do("POST", "/api/login", `{"username":"bea","password":"pw-bea-long"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"change_password":true`) {
		t.Errorf("sign-in: %d %s", w.Code, w.Body)
	}
	// The admin who ran it carries on.
	if w := admin.do("GET", "/api/projects", ""); w.Code != http.StatusOK {
		t.Errorf("the admin: %d", w.Code)
	}

	// The password rules, and not the old one.
	for _, pw := range []string{"short", "pw-bea-long"} {
		if w := fresh.do("POST", "/api/password/change", fmt.Sprintf(`{"password":%q}`, pw)); w.Code != http.StatusBadRequest {
			t.Errorf("new password %q: %d %s", pw, w.Code, w.Body)
		}
	}
	if w := withBearer(e.h, tok, "POST", "/api/password/change", `{"password":"bea-new-password"}`); w.Code == http.StatusOK {
		t.Error("a token changed the password")
	}
	if w := fresh.do("POST", "/api/password/change", `{"password":"bea-new-password"}`); w.Code != http.StatusOK {
		t.Fatalf("change: %d %s", w.Code, w.Body)
	}
	if w := fresh.do("GET", "/api/projects", ""); w.Code != http.StatusOK {
		t.Errorf("after the change: %d %s", w.Code, w.Body)
	}
	// Her other session and her token ended with the change.
	if w := bea.do("GET", "/api/projects", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("bea's other session after the change: %d", w.Code)
	}
	if w := withBearer(e.h, tok, "GET", "/api/projects", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("bea's token after the change: %d", w.Code)
	}
	if w := fresh.do("POST", "/api/password/change", `{"password":"bea-newer-password"}`); w.Code != http.StatusConflict {
		t.Errorf("a second change: %d", w.Code)
	}
	signIn(t, e.h, "bea", "bea-new-password")
}

func TestLockdownResetsTwoFactor(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	clk := pinClock(e.a)
	bea := e.as(t, "bea")
	enrol(t, bea, clk)
	admin := e.as(t, "admin")
	if w := admin.do("POST", "/api/admin/lockdown", lockdownBodyJSON(`"totp":true`, "")); w.Code != http.StatusOK {
		t.Fatalf("lockdown: %d %s", w.Code, w.Body)
	}
	// Signed in already: sent to set it up again.
	if w := bea.do("GET", "/api/projects", ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "2fa_required") {
		t.Errorf("bea: %d %s", w.Code, w.Body)
	}
	// Signing in asks for no code, but to set it up.
	w := (&client{t: t, h: e.h}).do("POST", "/api/login", `{"username":"bea","password":"pw-bea-long"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enrol_2fa":true`) || strings.Contains(w.Body.String(), "totp_required") {
		t.Errorf("sign-in: %d %s", w.Code, w.Body)
	}
}

func TestLockdownNotice(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	e.a.NoticeEvery = time.Millisecond
	bea := e.as(t, "bea")
	admin := e.as(t, "admin")
	w := admin.do("POST", "/api/admin/lockdown", lockdownBodyJSON(`"sessions":false`, `,"notice":"We had a breach.\n\nChoose a new password.","email":true,"banner":true`))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"total":5`) {
		t.Fatalf("lockdown: %d %s", w.Code, w.Body)
	}
	seen := map[string]bool{}
	for range 5 {
		m := e.box.next(t)
		seen[m.To] = true
		if !strings.Contains(m.Body, "We had a breach.") || !strings.Contains(m.Body, "Choose a new password.") {
			t.Errorf("email: %s", m.Body)
		}
	}
	if !seen["bea@example.com"] || len(seen) != 5 {
		t.Errorf("emailed %v", seen)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var st struct {
			Emails *NoticeEmails
		}
		decode(t, admin.do("GET", "/api/admin/lockdown", ""), &st)
		if st.Emails != nil && st.Emails.Done {
			if st.Emails.Sent != 5 || st.Emails.Failed != 0 {
				t.Errorf("emails: %+v", st.Emails)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the emails never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !auditHas(t, e.a, "admin.lockdown.emailed") {
		t.Error("the emails aren't in the audit log")
	}

	var got struct {
		Notice *store.Notice
	}
	decode(t, bea.do("GET", "/api/notice", ""), &got)
	if got.Notice == nil || !strings.Contains(got.Notice.Body, "breach") {
		t.Fatalf("bea's notice: %+v", got)
	}
	if w := bea.do("POST", fmt.Sprintf("/api/notice/%d/dismiss", got.Notice.ID), ""); w.Code != http.StatusNoContent {
		t.Fatalf("dismiss: %d %s", w.Code, w.Body)
	}
	decode(t, bea.do("GET", "/api/notice", ""), &got)
	if got.Notice != nil {
		t.Error("shown after dismissing")
	}
	// The admin hasn't dismissed it; clearing it hides it from everyone.
	decode(t, admin.do("GET", "/api/notice", ""), &got)
	if got.Notice == nil {
		t.Error("the admin doesn't see it")
	}
	if w := admin.do("DELETE", "/api/admin/notice", ""); w.Code != http.StatusNoContent {
		t.Fatalf("clear: %d", w.Code)
	}
	decode(t, admin.do("GET", "/api/notice", ""), &got)
	if got.Notice != nil {
		t.Error("shown after clearing")
	}
}

func TestLockdownNoticeWithoutEmail(t *testing.T) {
	t.Parallel()
	a, h := newServer(t)
	org, _ := a.acc.DefaultOrg()
	addUser(t, a, org.ID, "bea", store.RoleMember)
	addUser(t, a, org.ID, "-x", store.RoleMember)
	admin := signIn(t, h, "admin", "s3cret")
	w := admin.do("POST", "/api/admin/lockdown", lockdownBodyJSON(`"sessions":false`, `,"notice":"Hello","email":true`))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"unavailable":true`) {
		t.Fatalf("lockdown: %d %s", w.Code, w.Body)
	}
	w = admin.do("GET", "/api/admin/lockdown/users.csv", "")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("csv: %d %s", w.Code, w.Header())
	}
	if body := w.Body.String(); !strings.Contains(body, "bea,bea@example.com") || !strings.Contains(body, "'-x,'-x@example.com") {
		t.Errorf("csv:\n%s", body)
	}
}
