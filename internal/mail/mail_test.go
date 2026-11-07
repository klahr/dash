package mail

import (
	"context"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	if err := (Config{}).Check(); err == nil || !strings.Contains(err.Error(), "DASH_SMTP_HOST") {
		t.Fatalf("empty: %v", err)
	}
	ok := Config{Host: "smtp.example.com", Port: 587, From: "Dash <dash@example.com>"}
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.Username = "u"
	if bad.Check() == nil {
		t.Fatal("username without password")
	}
	bad = ok
	bad.From = "not an address"
	if bad.Check() == nil {
		t.Fatal("bad from")
	}
}

func TestCompose(t *testing.T) {
	b, err := Compose("Dash <dash@example.com>", Message{To: "ann@example.com", Subject: "Hej då", Body: "line\nnext"}, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"From: \"Dash\" <dash@example.com>\r\n", "To: <ann@example.com>\r\n", "Subject: =?utf-8?q?", "@example.com>\r\n", "\r\n\r\nline\r\nnext\r\n"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	if _, err := Compose("dash@example.com", Message{To: "ann@example.com", Subject: "x\r\nBcc: eve@example.com"}, time.Now()); err == nil {
		t.Fatal("header injection")
	}
}

// fakeSMTP accepts one message on a local port and returns what it got.
func fakeSMTP(t *testing.T) (port int, got <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ch := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		tp := textproto.NewConn(c)
		var log strings.Builder
		_ = tp.PrintfLine("220 localhost ready")
		for {
			line, err := tp.ReadLine()
			if err != nil {
				return
			}
			log.WriteString(line + "\n")
			switch {
			case strings.HasPrefix(line, "EHLO"):
				_ = tp.PrintfLine("250 localhost")
			case line == "DATA":
				_ = tp.PrintfLine("354 go on")
				b, _ := tp.ReadDotBytes()
				log.Write(b)
				_ = tp.PrintfLine("250 queued")
			case line == "QUIT":
				_ = tp.PrintfLine("221 bye")
				ch <- log.String()
				return
			default:
				_ = tp.PrintfLine("250 ok")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, ch
}

func TestSMTPSend(t *testing.T) {
	port, got := fakeSMTP(t)
	s, err := NewSMTP(Config{Host: "127.0.0.1", Port: port, From: "Dash <dash@example.com>"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), Message{To: "ann@example.com", Subject: "Hi", Body: "hello\n.dot line"}); err != nil {
		t.Fatal(err)
	}
	log := <-got
	for _, want := range []string{"MAIL FROM:<dash@example.com>", "RCPT TO:<ann@example.com>", "Subject: Hi", "hello\n.dot line"} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q in:\n%s", want, log)
		}
	}
}
