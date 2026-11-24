package mail

import (
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestComposeHTML(t *testing.T) {
	long := strings.Repeat("x", 2000)
	m := Message{To: "ann@example.com", Subject: "Hi", Body: "plain\nline", HTML: "<p>" + long + "</p>"}
	raw, err := Compose("dash@example.com", m, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line of %d characters", len(line))
		}
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	typ, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || typ != "multipart/alternative" {
		t.Fatalf("content type %q %v", typ, err)
	}
	r := multipart.NewReader(msg.Body, params["boundary"])
	var got []string
	for {
		p, err := r.NextPart() // decodes quoted-printable
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		got = append(got, p.Header.Get("Content-Type")+"|"+string(b))
	}
	want := []string{"text/plain; charset=utf-8|plain\r\nline", "text/html; charset=utf-8|<p>" + long + "</p>"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("parts %q", got)
	}
}

func TestNotice(t *testing.T) {
	n := Notice{Brand: "Acme <Agents>", Contact: "help@example.com", Title: "Reset your password", Greeting: "Hello ann,",
		Before: []string{"Open this link:"}, Action: "Choose a new password", URL: "https://dash.example.com/login.html#reset=abc",
		After: []string{"If it wasn't you, ignore this email."}}
	m := n.Message("ann@example.com", "Reset")
	for _, want := range []string{"Hello ann,\n\nOpen this link:\n\nhttps://dash.example.com/login.html#reset=abc\n\n", "-- \nAcme <Agents>\nQuestions? Write to help@example.com\n"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("text lacks %q:\n%s", want, m.Body)
		}
	}
	for _, want := range []string{"Acme &lt;Agents&gt;", `href="https://dash.example.com/login.html#reset=abc"`, ">Choose a new password</a>",
		`href="mailto:help@example.com"`, "Reset your password</h1>"} {
		if !strings.Contains(m.HTML, want) {
			t.Errorf("HTML lacks %q:\n%s", want, m.HTML)
		}
	}
	// An inline link sits in grey text of about the same lightness, so the
	// underline, not the colour, has to mark it (WCAG 1.4.1).
	if a := string(A("x", "https://example.com")); !strings.Contains(a, "text-decoration:underline") {
		t.Errorf("inline link is not underlined: %s", a)
	}
	if strings.Contains(string(A("x", "javascript:alert(1)")), "javascript:") {
		t.Error("a javascript: link got through")
	}
}
