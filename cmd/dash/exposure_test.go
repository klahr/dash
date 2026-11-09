package main

import (
	"reflect"
	"strings"
	"testing"
)

const goodPassword = "kX9v2LqT8zR4wN7pA3mE"

func checks(ps []problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Check)
	}
	return out
}

func TestExposureProblems(t *testing.T) {
	safe := exposure{PublicURL: "https://dash.example.com", TrustProxy: true, Addr: "127.0.0.1:7788",
		Username: "admin", Password: goodPassword}
	cases := []struct {
		name string
		edit func(*exposure)
		want []string
	}{
		{"safe", func(*exposure) {}, nil},
		{"private: nothing checked", func(e *exposure) { *e = exposure{Addr: "0.0.0.0:7788", Password: "x"} }, nil},
		{"trust-proxy alone is public", func(e *exposure) { e.PublicURL, e.Password = "", "short" }, []string{checkPassword}},
		{"missing password", func(e *exposure) { e.Password = "" }, []string{checkPassword}},
		{"short password", func(e *exposure) { e.Password = "fifteen-chars!!" }, []string{checkPassword}},
		{"known default", func(e *exposure) { e.Password = "a-long-random-password" }, []string{checkPassword}},
		{"known default, any case", func(e *exposure) { e.Password = "Correct Horse Battery Staple" }, []string{checkPassword}},
		{"repeated characters", func(e *exposure) { e.Password = "aaaaaaaaaaaaaaaaaaaa" }, []string{checkPassword}},
		{"password is the username", func(e *exposure) { e.Username, e.Password = goodPassword, goodPassword }, []string{checkPassword}},
		{"http public URL", func(e *exposure) { e.PublicURL = "http://dash.example.com" }, []string{checkPublicURL}},
		{"no scheme", func(e *exposure) { e.PublicURL = "dash.example.com" }, []string{checkPublicURL}},
		{"http on loopback is fine", func(e *exposure) { e.PublicURL = "http://127.0.0.1:7788" }, nil},
		{"proxy on all interfaces", func(e *exposure) { e.Addr = "0.0.0.0:7788" }, []string{checkProxyAddr}},
		{"proxy on empty host", func(e *exposure) { e.Addr = ":7788" }, []string{checkProxyAddr}},
		{"no proxy on all interfaces", func(e *exposure) { e.TrustProxy, e.Addr = false, "0.0.0.0:7788" }, nil},
		{"signup without mail", func(e *exposure) { e.SignupOpen = true }, []string{checkSignupMail}},
		{"signup with mail", func(e *exposure) { e.SignupOpen, e.MailOn = true, true }, nil},
		{"everything", func(e *exposure) {
			*e = exposure{PublicURL: "http://dash.example.com", TrustProxy: true, Addr: "0.0.0.0:7788", Password: "change-me", SignupOpen: true}
		}, []string{checkPassword, checkPublicURL, checkProxyAddr, checkSignupMail}},
	}
	for _, c := range cases {
		e := safe
		c.edit(&e)
		if got := checks(e.problems()); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: problems = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCheckExposure(t *testing.T) {
	bad := exposure{PublicURL: "https://dash.example.com", TrustProxy: true, Addr: "0.0.0.0:7788", Password: "change-me"}
	err := checkExposure(bad)
	if err == nil || !strings.Contains(err.Error(), "-allow-unsafe=password,proxy-addr") {
		t.Fatalf("checkExposure = %v, want a refusal naming password,proxy-addr", err)
	}
	bad.AllowUnsafe = "proxy-addr"
	if err := checkExposure(bad); err == nil || strings.Contains(err.Error(), "proxy-addr") {
		t.Fatalf("with proxy-addr allowed: %v, want a refusal for the password only", err)
	}
	bad.AllowUnsafe = " Proxy-Addr , password"
	if err := checkExposure(bad); err != nil {
		t.Fatalf("both allowed: %v", err)
	}
	bad.AllowUnsafe = "all"
	if err := checkExposure(bad); err != nil {
		t.Fatalf("all allowed: %v", err)
	}
	bad.AllowUnsafe = "passwrd"
	if err := checkExposure(bad); err == nil || !strings.Contains(err.Error(), "unknown check") {
		t.Fatalf("typo: %v, want unknown check", err)
	}
	if err := checkExposure(exposure{Addr: "0.0.0.0:7788"}); err != nil {
		t.Fatalf("private Dash: %v", err)
	}
}
