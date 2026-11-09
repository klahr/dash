package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/mail"
	"github.com/klahr/dash/internal/store"
)

// outbox is a mail.Sender that keeps what it is given.
type outbox struct{ ch chan mail.Message }

func newOutbox() *outbox { return &outbox{ch: make(chan mail.Message, 32)} }

func (o *outbox) Send(_ context.Context, m mail.Message) error { o.ch <- m; return nil }

// next is the next email sent (they're sent in the background).
func (o *outbox) next(t *testing.T) mail.Message {
	t.Helper()
	select {
	case m := <-o.ch:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no email sent")
	}
	return mail.Message{}
}

// none checks that no (further) email goes out.
func (o *outbox) none(t *testing.T) {
	t.Helper()
	select {
	case m := <-o.ch:
		t.Fatalf("unexpected email to %s: %s", m.To, m.Subject)
	case <-time.After(100 * time.Millisecond):
	}
}

var linkToken = regexp.MustCompile(`/login\.html#(?:verify|reset)=([A-Za-z0-9_-]+)`)

func tokenIn(t *testing.T, m mail.Message) string {
	t.Helper()
	if !strings.Contains(m.Body, "https://dash.example.com/login.html#") {
		t.Fatalf("link isn't on the public URL:\n%s", m.Body)
	}
	got := linkToken.FindStringSubmatch(m.Body)
	if got == nil {
		t.Fatalf("no link in:\n%s", m.Body)
	}
	return got[1]
}

// newSignupServer is a server with email and open signup on.
func newSignupServer(t *testing.T) (*Auth, http.Handler, *outbox) {
	t.Helper()
	a, h := newServer(t)
	box := newOutbox()
	a.SetMail(box, "https://dash.example.com/")
	if err := a.EnableSignup(); err != nil {
		t.Fatal(err)
	}
	return a, h, box
}

func signUp(c *client, email, password string) *httptest.ResponseRecorder {
	return c.do("POST", "/api/signup", fmt.Sprintf(`{"email":%q,"password":%q}`, email, password))
}

func TestSignupOffByDefault(t *testing.T) {
	a, h := newServer(t)
	anon := &client{t: t, h: h}
	w := anon.do("GET", "/api/signup", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"open":false`) || !strings.Contains(w.Body.String(), `"password_reset":false`) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	for _, req := range [][2]string{
		{"/api/signup", `{"email":"new@example.com","password":"long-enough"}`},
		{"/api/signup/resend", `{"email":"new@example.com"}`},
		{"/api/password/forgot", `{"email":"new@example.com"}`},
	} {
		if w := anon.do("POST", req[0], req[1]); w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d %s", req[0], w.Code, w.Body)
		}
	}
	if _, _, err := a.acc.Credentials("new@example.com"); err == nil {
		t.Fatal("an account was made with signup off")
	}
	// Without a mailer, signup can't be turned on.
	if err := a.EnableSignup(); err == nil || a.SignupOpen() {
		t.Fatal("signup enabled without email")
	}
	// Only platform admins create orgs, and nobody gets a personal workspace.
	org, _ := a.acc.DefaultOrg()
	addUser(t, a, org.ID, "bea", store.RoleMember)
	bea := signIn(t, h, "bea", "pw-bea-long")
	if m := bea.me(); len(m.Orgs) != 1 {
		t.Fatalf("orgs with signup off: %+v", m.Orgs)
	}
	if w := bea.do("POST", "/api/orgs", `{"name":"Mine"}`); w.Code != http.StatusForbidden {
		t.Fatalf("create org: %d %s", w.Code, w.Body)
	}
}

func TestSignupNeedsVerification(t *testing.T) {
	a, h, box := newSignupServer(t)
	anon := &client{t: t, h: h}
	if w := anon.do("GET", "/api/signup", ""); !strings.Contains(w.Body.String(), `"open":true`) {
		t.Fatalf("status: %s", w.Body)
	}
	if w := anon.do("POST", "/api/signup", `{"email":"Carla.Doe+dash@example.com"}`); w.Code != http.StatusAccepted {
		t.Fatalf("signup: %d %s", w.Code, w.Body)
	}
	m := box.next(t)
	if m.To != "Carla.Doe+dash@example.com" {
		t.Fatalf("to %q", m.To)
	}
	tok := tokenIn(t, m)

	// No password signs in before the link.
	if w := anon.do("POST", "/api/login", `{"username":"carla.doe+dash@example.com","password":"carla-password"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("login before verifying: %d %s", w.Code, w.Body)
	}

	if w := anon.do("POST", "/api/signup/verify", `{"token":"nope","password":"carla-password"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad link: %d", w.Code)
	}
	if w := anon.do("POST", "/api/signup/verify", `{"token":"`+tok+`","password":"short"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("weak password: %d", w.Code)
	}
	if w := anon.do("POST", "/api/signup/verify", `{"token":"`+tok+`","password":"carla-password"}`); w.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", w.Code, w.Body)
	}
	// Signed in, in their personal workspace, the only member.
	me := anon.me()
	if me.Username != "Carla.Doe" || me.User.PlatformAdmin || me.Org == nil || !me.Org.Personal || me.Org.Role != store.RoleOwner || len(me.Orgs) != 1 {
		t.Fatalf("me after verifying: %+v", me)
	}
	if n, err := a.acc.ForOrg(me.Org.ID).MemberCount(); err != nil || n != 1 {
		t.Fatalf("member count %d, %v", n, err)
	}
	// The link works once.
	if w := anon.do("POST", "/api/signup/verify", `{"token":"`+tok+`","password":"other-password"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("link again: %d", w.Code)
	}
	signIn(t, h, "carla.doe+dash@example.com", "carla-password")
}

func TestSignupDoesntRevealAccounts(t *testing.T) {
	a, h, box := newSignupServer(t)
	org, _ := a.acc.DefaultOrg()
	addUser(t, a, org.ID, "bea", store.RoleMember) // bea@example.com
	anon := &client{t: t, h: h}

	fresh := signUp(anon, "dan@example.com", "dan-password")
	taken := signUp(anon, "BEA@example.com", "bea-other-password")
	if fresh.Code != http.StatusAccepted || fresh.Code != taken.Code || fresh.Body.String() != taken.Body.String() {
		t.Fatalf("new: %d %s; taken: %d %s", fresh.Code, fresh.Body, taken.Code, taken.Body)
	}
	firstTok := tokenIn(t, box.next(t)) // dan's link
	m := box.next(t)
	if m.To != "bea@example.com" || !strings.Contains(m.Body, "already") || strings.Contains(m.Body, "#verify=") {
		t.Fatalf("to the existing account: %+v", m)
	}
	// bea's password is unchanged.
	signIn(t, h, "bea", "pw-bea-long")

	// Signing up again for a pending address sends a new link, replacing the
	// old. The password sent with a signup counts for nothing: whoever opens
	// the link (the address's owner) chooses it.
	again := signUp(anon, "dan@example.com", "other-password")
	if again.Body.String() != fresh.Body.String() {
		t.Fatalf("again: %s", again.Body)
	}
	tok := tokenIn(t, box.next(t))
	if w := anon.do("POST", "/api/signup/verify", `{"token":"`+firstTok+`","password":"dan-password"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("replaced link: %d", w.Code)
	}
	if w := anon.do("POST", "/api/signup/verify", `{"token":"`+tok+`","password":"dan-chosen-password"}`); w.Code != http.StatusOK {
		t.Fatalf("verify: %d", w.Code)
	}
	for _, pw := range []string{"dan-password", "other-password"} {
		if w := do(h, "POST", "/api/login", `{"username":"dan@example.com","password":"`+pw+`"}`); w.Code != http.StatusUnauthorized {
			t.Fatalf("a signup's password %q works: %d", pw, w.Code)
		}
	}
	signIn(t, h, "dan@example.com", "dan-chosen-password")

	// Resending and password reset answer alike for every address.
	r1 := anon.do("POST", "/api/signup/resend", `{"email":"nobody@example.com"}`)
	r2 := anon.do("POST", "/api/signup/resend", `{"email":"dan@example.com"}`) // verified: no link
	if r1.Code != http.StatusAccepted || r1.Body.String() != r2.Body.String() {
		t.Fatalf("resend: %s / %s", r1.Body, r2.Body)
	}
	f1 := do(h, "POST", "/api/password/forgot", `{"email":"nobody@example.com"}`)
	f2 := do(h, "POST", "/api/password/forgot", `{"email":"dan@example.com"}`)
	if f1.Code != http.StatusAccepted || f1.Body.String() != f2.Body.String() {
		t.Fatalf("forgot: %s / %s", f1.Body, f2.Body)
	}
	if m := box.next(t); m.To != "dan@example.com" || !strings.Contains(m.Body, "#reset=") {
		t.Fatalf("reset mail: %+v", m)
	}
	box.none(t)
}

func TestSignupRateLimits(t *testing.T) {
	_, h, box := newSignupServer(t)
	anon := &client{t: t, h: h}
	for i := 0; i < limitPerEmail; i++ {
		if w := anon.do("POST", "/api/signup/resend", `{"email":"x@example.com"}`); w.Code != http.StatusAccepted {
			t.Fatalf("resend %d: %d", i, w.Code)
		}
	}
	if w := anon.do("POST", "/api/signup/resend", `{"email":"X@example.com"}`); w.Code != http.StatusTooManyRequests {
		t.Fatalf("per address: %d", w.Code)
	}
	// Per IP: other addresses, until the address's limit is spent too.
	codes := []int{}
	for i := 0; i < limitPerIP; i++ {
		codes = append(codes, anon.do("POST", "/api/signup/resend", fmt.Sprintf(`{"email":"y%d@example.com"}`, i)).Code)
	}
	if codes[len(codes)-1] != http.StatusTooManyRequests {
		t.Fatalf("per IP: %v", codes)
	}
	// Password reset has limits of its own.
	if w := anon.do("POST", "/api/password/forgot", `{"email":"x@example.com"}`); w.Code != http.StatusAccepted {
		t.Fatalf("forgot: %d", w.Code)
	}
	box.none(t)
}

func TestPersonalWorkspaceForExistingUsers(t *testing.T) {
	a, h, _ := newSignupServer(t)
	org, _ := a.acc.DefaultOrg()
	addUser(t, a, org.ID, "bea", store.RoleMember)

	// Signing in gives bea a personal workspace; she keeps working in Default.
	bea := signIn(t, h, "bea", "pw-bea-long")
	me := bea.me()
	if me.Org.ID != org.ID || len(me.Orgs) != 2 || !me.Orgs[1].Personal || me.Orgs[1].Role != store.RoleOwner {
		t.Fatalf("bea: %+v", me)
	}
	personal := me.Orgs[1]
	// Signing in again doesn't make another.
	if me := signIn(t, h, "bea", "pw-bea-long").me(); len(me.Orgs) != 2 {
		t.Fatalf("orgs: %+v", me.Orgs)
	}

	// The admin's Default org stays as it was: not personal, same members.
	admin := signIn(t, h, "admin", "s3cret")
	am := admin.me()
	if am.Org.ID != org.ID || am.Org.Personal || len(am.Orgs) != 2 {
		t.Fatalf("admin: %+v", am)
	}
	if n, _ := a.acc.ForOrg(org.ID).MemberCount(); n != 2 {
		t.Fatalf("Default has %d members", n)
	}

	// Nobody can be invited to a personal workspace...
	w := bea.do("POST", fmt.Sprintf("/api/orgs/%d/invitations", personal.ID), `{"email":"eve@example.com","password":"pw-bea-long"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("invite to personal: %d %s", w.Code, w.Body)
	}
	// ...but anyone may create an org, and invite people to that.
	w = bea.do("POST", "/api/orgs", `{"name":"Bea's team"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create org: %d %s", w.Code, w.Body)
	}
	var team store.Membership
	decode(t, w, &team)
	if team.Personal {
		t.Fatal("new org is personal")
	}
	w = bea.do("POST", fmt.Sprintf("/api/orgs/%d/invitations", team.ID), `{"email":"eve@example.com","password":"pw-bea-long"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", w.Code, w.Body)
	}
	w = bea.do("GET", fmt.Sprintf("/api/orgs/%d/members", team.ID), "")
	if !strings.Contains(w.Body.String(), `"member_count":1`) {
		t.Fatalf("members: %s", w.Body)
	}
}

func TestPasswordReset(t *testing.T) {
	a, h, box := newSignupServer(t)
	org, _ := a.acc.DefaultOrg()
	addUser(t, a, org.ID, "bea", store.RoleMember)
	old := signIn(t, h, "bea", "pw-bea-long")

	if w := do(h, "POST", "/api/password/forgot", `{"email":"Bea@Example.com"}`); w.Code != http.StatusAccepted {
		t.Fatalf("forgot: %d %s", w.Code, w.Body)
	}
	tok := tokenIn(t, box.next(t))

	if w := do(h, "POST", "/api/password/reset", `{"token":"`+tok+`","password":"short"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("weak: %d", w.Code)
	}
	if w := do(h, "POST", "/api/password/reset", `{"token":"wrong","password":"brand-new-password"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad link: %d", w.Code)
	}
	if w := do(h, "POST", "/api/password/reset", `{"token":"`+tok+`","password":"brand-new-password"}`); w.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", w.Code, w.Body)
	}
	// The old password and the old sessions are gone; the link is used up.
	if w := old.do("GET", "/api/me", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("old session: %d", w.Code)
	}
	if w := do(h, "POST", "/api/login", `{"username":"bea","password":"pw-bea-long"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("old password: %d", w.Code)
	}
	signIn(t, h, "bea", "brand-new-password")
	if w := do(h, "POST", "/api/password/reset", `{"token":"`+tok+`","password":"another-password"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("link again: %d", w.Code)
	}
}

func TestPasswordResetWithoutSignup(t *testing.T) {
	// Email alone (no open signup) gives password reset, not signup.
	a, h := newServer(t)
	box := newOutbox()
	a.SetMail(box, "https://dash.example.com")
	org, _ := a.acc.DefaultOrg()
	addUser(t, a, org.ID, "bea", store.RoleMember)
	if w := do(h, "POST", "/api/signup", `{"email":"new@example.com","password":"long-enough"}`); w.Code != http.StatusNotFound {
		t.Fatalf("signup: %d", w.Code)
	}
	if w := do(h, "POST", "/api/password/forgot", `{"email":"bea@example.com"}`); w.Code != http.StatusAccepted {
		t.Fatalf("forgot: %d", w.Code)
	}
	tokenIn(t, box.next(t))
	if me := signIn(t, h, "bea", "pw-bea-long").me(); len(me.Orgs) != 1 {
		t.Fatalf("personal workspace without signup: %+v", me.Orgs)
	}
}
