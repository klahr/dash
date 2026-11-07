package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klahr/dash/internal/store"
	"github.com/klahr/dash/internal/totp"
)

// clock pins a's time, so tests choose which 30-second period a code is for.
type clock struct{ t time.Time }

func (c *clock) tick() { c.t = c.t.Add(totp.Period * time.Second) }

func pinClock(a *Auth) *clock {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	a.now = func() time.Time { return c.t }
	return c
}

func codeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	c, err := totp.Code(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// enrol turns on two-factor sign-in for c's account and returns the secret
// and the recovery codes.
func enrol(t *testing.T, c *client, clk *clock) (string, []string) {
	t.Helper()
	w := c.do("POST", "/api/account/2fa/setup", "")
	if w.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
	var s struct{ Secret, URI, QR string }
	decode(t, w, &s)
	w = c.do("POST", "/api/account/2fa/enable", fmt.Sprintf(`{"code":%q}`, codeAt(t, s.Secret, clk.t)))
	if w.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
	var e struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	decode(t, w, &e)
	return s.Secret, e.RecoveryCodes
}

// passwordStep posts the password from ip and returns the login's ticket.
func passwordStep(t *testing.T, h http.Handler, ip, username, password string) string {
	t.Helper()
	w := doFrom(h, ip, "POST", "/api/login", fmt.Sprintf(`{"username":%q,"password":%q}`, username, password))
	var r struct {
		TOTPRequired bool   `json:"totp_required"`
		Ticket       string `json:"ticket"`
	}
	decode(t, w, &r)
	if w.Code != http.StatusOK || !r.TOTPRequired || r.Ticket == "" {
		t.Fatalf("password step: %d %s", w.Code, w.Body)
	}
	for _, k := range w.Result().Cookies() {
		if k.Name == CookieName {
			t.Fatal("a session before the second factor")
		}
	}
	return r.Ticket
}

func codeStep(h http.Handler, ip, ticket, code string) *httptest.ResponseRecorder {
	return doFrom(h, ip, "POST", "/api/login/totp", fmt.Sprintf(`{"ticket":%q,"code":%q}`, ticket, code))
}

func doFrom(h http.Handler, ip, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = ip + ":1234"
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestEnrolTOTP(t *testing.T) {
	a, h := newServer(t)
	clk := pinClock(a)
	c := signIn(t, h, "admin", "s3cret")

	w := c.do("POST", "/api/account/2fa/setup", "")
	var s struct{ Secret, URI, QR string }
	decode(t, w, &s)
	if !strings.HasPrefix(s.QR, "data:image/png;base64,") || !strings.Contains(s.URI, "secret="+s.Secret) || !strings.HasPrefix(s.URI, "otpauth://totp/Dash:admin?") {
		t.Fatalf("setup: %+v", s)
	}
	// Until confirmed, it isn't on.
	if c.me().User.TOTP {
		t.Fatal("on before confirming")
	}
	if w := c.do("POST", "/api/account/2fa/enable", `{"code":"000000"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("wrong confirmation code: %d %s", w.Code, w.Body)
	}
	w = c.do("POST", "/api/account/2fa/enable", fmt.Sprintf(`{"code":%q}`, codeAt(t, s.Secret, clk.t)))
	if w.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
	var e struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	decode(t, w, &e)
	if len(e.RecoveryCodes) != recoveryCount || len(e.RecoveryCodes[0]) != 19 {
		t.Fatalf("recovery codes: %q", e.RecoveryCodes)
	}
	if !c.me().User.TOTP {
		t.Fatal("not on after confirming")
	}
	// Recovery codes and the secret's QR are shown only once.
	if w := c.do("POST", "/api/account/2fa/setup", ""); w.Code != http.StatusConflict {
		t.Fatalf("setup while on: %d", w.Code)
	}
	var st struct {
		Enabled bool `json:"enabled"`
		Left    int  `json:"recovery_codes_left"`
	}
	decode(t, c.do("GET", "/api/account/2fa", ""), &st)
	if !st.Enabled || st.Left != recoveryCount {
		t.Fatalf("status: %+v", st)
	}
}

func TestLoginWithTOTP(t *testing.T) {
	a, h := newServer(t)
	clk := pinClock(a)
	secret, _ := enrol(t, signIn(t, h, "admin", "s3cret"), clk)

	ticket := passwordStep(t, h, "10.0.0.1", "admin", "s3cret")
	if w := codeStep(h, "10.0.0.1", ticket, "123456"); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong code: %d %s", w.Code, w.Body)
	}
	// The code that confirmed enrolment can't be used again.
	if w := codeStep(h, "10.0.0.1", ticket, codeAt(t, secret, clk.t)); w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed code: %d %s", w.Code, w.Body)
	}
	clk.tick()
	w := codeStep(h, "10.0.0.1", ticket, codeAt(t, secret, clk.t))
	if w.Code != http.StatusOK {
		t.Fatalf("right code: %d %s", w.Code, w.Body)
	}
	c := sessionCookie(t, w)
	if w := do(h, "GET", "/api/projects", "", c); w.Code != http.StatusOK {
		t.Fatalf("signed in: %d", w.Code)
	}
	// A ticket works once.
	clk.tick()
	if w := codeStep(h, "10.0.0.1", ticket, codeAt(t, secret, clk.t)); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "ticket_expired") {
		t.Fatalf("used ticket: %d %s", w.Code, w.Body)
	}
	// And expires.
	ticket = passwordStep(t, h, "10.0.0.1", "admin", "s3cret")
	clk.t = clk.t.Add(ticketTTL + time.Second)
	if w := codeStep(h, "10.0.0.1", ticket, codeAt(t, secret, clk.t)); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "ticket_expired") {
		t.Fatalf("expired ticket: %d %s", w.Code, w.Body)
	}
	// Without the password step there is nothing to answer.
	if w := codeStep(h, "10.0.0.1", "made-up", codeAt(t, secret, clk.t)); w.Code != http.StatusUnauthorized {
		t.Fatalf("made-up ticket: %d", w.Code)
	}
}

func TestLoginWithRecoveryCode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "dash.db")
	a := openAuth(t, dbPath, "admin", "s3cret")
	h := serve(a)
	clk := pinClock(a)
	_, codes := enrol(t, signIn(t, h, "admin", "s3cret"), clk)

	// Typed in upper case without dashes still works.
	typed := strings.ToUpper(strings.ReplaceAll(codes[3], "-", " "))
	w := codeStep(h, "10.0.0.1", passwordStep(t, h, "10.0.0.1", "admin", "s3cret"), typed)
	if w.Code != http.StatusOK {
		t.Fatalf("recovery code: %d %s", w.Code, w.Body)
	}
	var r struct {
		Used bool `json:"recovery_used"`
		Left int  `json:"recovery_codes_left"`
	}
	decode(t, w, &r)
	if !r.Used || r.Left != recoveryCount-1 {
		t.Fatalf("after a recovery code: %+v", r)
	}
	c := sessionCookie(t, w)
	// Each works once.
	if w := codeStep(h, "10.0.0.1", passwordStep(t, h, "10.0.0.1", "admin", "s3cret"), codes[3]); w.Code != http.StatusUnauthorized {
		t.Fatalf("used recovery code: %d %s", w.Code, w.Body)
	}
	// They are stored hashed.
	for _, f := range []string{dbPath, dbPath + "-wal"} {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), strings.ReplaceAll(codes[4], "-", "")) {
			t.Fatal("recovery codes are stored in plain text")
		}
	}

	// New codes need the password and replace the old ones.
	if w := do(h, "POST", "/api/account/2fa/recovery-codes", `{"password":"nope"}`, c); w.Code != http.StatusForbidden {
		t.Fatalf("new codes, wrong password: %d", w.Code)
	}
	if w := do(h, "POST", "/api/account/2fa/recovery-codes", `{"password":"s3cret"}`, c); w.Code != http.StatusOK {
		t.Fatalf("new codes: %d %s", w.Code, w.Body)
	}
	if w := codeStep(h, "10.0.0.1", passwordStep(t, h, "10.0.0.1", "admin", "s3cret"), codes[5]); w.Code != http.StatusUnauthorized {
		t.Fatalf("replaced recovery code: %d %s", w.Code, w.Body)
	}
}

func TestTwoFactorLockout(t *testing.T) {
	t.Run("both steps share the address's lockout", func(t *testing.T) {
		a, h := newServer(t)
		clk := pinClock(a)
		secret, _ := enrol(t, signIn(t, h, "admin", "s3cret"), clk)
		clk.tick()
		const ip = "10.0.0.7"
		doFrom(h, ip, "POST", "/api/login", `{"username":"admin","password":"nope"}`)
		doFrom(h, ip, "POST", "/api/login", `{"username":"admin","password":"nope"}`)
		// The right password doesn't wipe the slate before the second factor.
		ticket := passwordStep(t, h, ip, "admin", "s3cret")
		for i := 0; i < maxFailures-2; i++ {
			if w := codeStep(h, ip, ticket, "000000"); w.Code != http.StatusUnauthorized {
				t.Fatalf("wrong code %d: %d", i, w.Code)
			}
		}
		if w := codeStep(h, ip, ticket, codeAt(t, secret, clk.t)); w.Code != http.StatusTooManyRequests {
			t.Fatalf("right code while locked out: %d %s", w.Code, w.Body)
		}
		if w := doFrom(h, ip, "POST", "/api/login", `{"username":"admin","password":"s3cret"}`); w.Code != http.StatusTooManyRequests {
			t.Fatalf("right password while locked out: %d", w.Code)
		}
		// Once the window passes, signing in fully clears it.
		clk.t = clk.t.Add(failureWindow + time.Minute)
		w := codeStep(h, ip, passwordStep(t, h, ip, "admin", "s3cret"), codeAt(t, secret, clk.t))
		if w.Code != http.StatusOK {
			t.Fatalf("after the window: %d %s", w.Code, w.Body)
		}
	})

	t.Run("guessing codes from many addresses locks the account's second step", func(t *testing.T) {
		a, h := newServer(t)
		clk := pinClock(a)
		secret, _ := enrol(t, signIn(t, h, "admin", "s3cret"), clk)
		clk.tick()
		for i := range maxFailures {
			ip := fmt.Sprintf("10.1.0.%d", i)
			if w := codeStep(h, ip, passwordStep(t, h, ip, "admin", "s3cret"), "000000"); w.Code != http.StatusUnauthorized {
				t.Fatalf("guess %d: %d", i, w.Code)
			}
		}
		ip := "10.1.1.1"
		if w := codeStep(h, ip, passwordStep(t, h, ip, "admin", "s3cret"), codeAt(t, secret, clk.t)); w.Code != http.StatusTooManyRequests {
			t.Fatalf("right code from a fresh address: %d %s", w.Code, w.Body)
		}
	})

	t.Run("a ticket takes only so many guesses", func(t *testing.T) {
		a, h := newServer(t)
		clk := pinClock(a)
		secret, _ := enrol(t, signIn(t, h, "admin", "s3cret"), clk)
		clk.tick()
		ticket := passwordStep(t, h, "10.2.0.1", "admin", "s3cret")
		a.mu.Lock()
		a.failed = map[string]*failures{} // only the ticket's own count is left
		a.tickets[sessionKey(ticket)].tries = maxFailures - 1
		a.mu.Unlock()
		codeStep(h, "10.2.0.1", ticket, "000000")
		if w := codeStep(h, "10.2.0.1", ticket, codeAt(t, secret, clk.t)); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "ticket_expired") {
			t.Fatalf("ticket after too many guesses: %d %s", w.Code, w.Body)
		}
	})
}

func TestDisableTOTP(t *testing.T) {
	a, h := newServer(t)
	clk := pinClock(a)
	c := signIn(t, h, "admin", "s3cret")
	enrol(t, c, clk)
	if w := c.do("POST", "/api/account/2fa/disable", `{"password":"nope"}`); w.Code != http.StatusForbidden {
		t.Fatalf("wrong password: %d", w.Code)
	}
	if w := c.do("POST", "/api/account/2fa/disable", `{"password":"s3cret"}`); w.Code != http.StatusNoContent {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
	signIn(t, h, "admin", "s3cret") // one step again
	if n, _ := a.acc.RecoveryCodesLeft(mustEnv(t, a)); n != 0 {
		t.Fatalf("%d recovery codes left after turning it off", n)
	}
}

func TestOrgRequires2FA(t *testing.T) {
	a, h := newServer(t)
	clk := pinClock(a)
	org, _ := a.acc.DefaultOrg()
	addUser(t, a, org.ID, "bea", store.RoleMember)
	addUser(t, a, org.ID, "ada", store.RoleAdmin)
	owner := signIn(t, h, "admin", "s3cret")
	path := fmt.Sprintf("/api/orgs/%d", org.ID)

	// An owner must have it before requiring it; admins can't.
	if w := owner.do("PATCH", path, `{"require_2fa":true,"password":"s3cret"}`); w.Code != http.StatusConflict {
		t.Fatalf("owner without 2FA: %d %s", w.Code, w.Body)
	}
	if w := signIn(t, h, "ada", "pw-ada-long").do("PATCH", path, `{"require_2fa":true,"password":"pw-ada-long"}`); w.Code != http.StatusForbidden {
		t.Fatalf("admin: %d", w.Code)
	}
	bea := signIn(t, h, "bea", "pw-bea-long") // a session from before the requirement
	enrol(t, owner, clk)
	if w := owner.do("PATCH", path, `{"require_2fa":true,"password":"nope"}`); w.Code != http.StatusForbidden {
		t.Fatalf("wrong password: %d", w.Code)
	}
	if w := owner.do("PATCH", path, `{"require_2fa":true,"password":"s3cret"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"require_2fa":true`) {
		t.Fatalf("require: %d %s", w.Code, w.Body)
	}
	if w := owner.do("GET", "/api/projects", ""); w.Code != http.StatusOK {
		t.Fatalf("owner with 2FA: %d", w.Code)
	}

	// Members without it are held at setting it up, sessions old and new.
	if w := bea.do("GET", "/api/projects", ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "2fa_required") {
		t.Fatalf("existing session: %d %s", w.Code, w.Body)
	}
	bea = &client{t: t, h: h}
	w := bea.do("POST", "/api/login", `{"username":"bea","password":"pw-bea-long"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enrol_2fa":true`) {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	if w := bea.do("GET", "/", ""); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login.html?enrol=1" {
		t.Fatalf("page: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := bea.do("GET", "/api/me", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"needs_2fa":true`) {
		t.Fatalf("me: %d %s", w.Code, w.Body)
	}
	enrol(t, bea, clk)
	if w := bea.do("GET", "/api/projects", ""); w.Code != http.StatusOK {
		t.Fatalf("after setting it up: %d %s", w.Code, w.Body)
	}
	// It can't be turned off while required.
	if w := bea.do("POST", "/api/account/2fa/disable", `{"password":"pw-bea-long"}`); w.Code != http.StatusConflict {
		t.Fatalf("disable while required: %d", w.Code)
	}
	// Members see who has it.
	var ms struct{ Members []store.Member }
	decode(t, owner.do("GET", path+"/members", ""), &ms)
	for _, m := range ms.Members {
		if m.TOTP != (m.Username != "ada") {
			t.Fatalf("members: %+v", ms.Members)
		}
	}

	// The operator's reset: back to being asked to set it up.
	if _, err := a.acc.ResetTOTP("bea"); err != nil {
		t.Fatal(err)
	}
	if w := bea.do("GET", "/api/projects", ""); w.Code != http.StatusForbidden {
		t.Fatalf("after a reset: %d", w.Code)
	}
}

// The Claude connector's sign-in can't skip the second factor.
func TestOAuthAsksForSecondFactor(t *testing.T) {
	a, _, h := newOAuthServer(t)
	clk := pinClock(a)
	c := &client{t: t, h: h}
	c.do("POST", "/api/login", `{"username":"admin","password":"s3cret"}`)
	mux := http.NewServeMux()
	a.Routes(mux)
	full := &client{t: t, h: a.Middleware(mux), c: c.c}
	secret, _ := enrol(t, full, clk)
	clk.tick()

	reg := register(t, h, `{"client_name":"Claude","redirect_uris":["`+callback+`"],"token_endpoint_auth_method":"none"}`)
	q := authParams(reg["client_id"].(string))
	if w, _ := login(t, h, q, "s3cret"); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "authenticator") {
		t.Fatalf("no code: %d", w.Code)
	}
	q.Set("code", "000000")
	if w, _ := login(t, h, q, "s3cret"); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong code: %d", w.Code)
	}
	q.Set("code", codeAt(t, secret, clk.t))
	if _, res := login(t, h, q, "s3cret"); res.Get("code") == "" {
		t.Fatalf("right code: %v", res)
	}
}
