// Package mail sends Dash's few emails (confirming an address after signing
// up, resetting a password) through an SMTP server the operator configures.
package mail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Message is a plain-text email to one recipient.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Sender sends messages. SMTP is the real one; tests use their own.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Config is how to reach the SMTP server, from DASH_SMTP_* (see docs/signup.md).
type Config struct {
	Host     string
	Port     int    // 465: TLS from the start; anything else: STARTTLS when offered
	Username string // empty: no authentication
	Password string
	From     string // "Dash <dash@example.com>" or a bare address
}

// Check reports what's missing or wrong, if anything.
func (c Config) Check() error {
	var missing []string
	if c.Host == "" {
		missing = append(missing, "DASH_SMTP_HOST")
	}
	if c.From == "" {
		missing = append(missing, "DASH_SMTP_FROM")
	}
	if len(missing) > 0 {
		return fmt.Errorf("SMTP is not configured: set %s", strings.Join(missing, " and "))
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("DASH_SMTP_PORT %d is not a port", c.Port)
	}
	if (c.Username == "") != (c.Password == "") {
		return errors.New("DASH_SMTP_USERNAME and DASH_SMTP_PASSWORD go together")
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return fmt.Errorf("DASH_SMTP_FROM %q is not an address: %v", c.From, err)
	}
	return nil
}

// SMTP sends through the server Config describes.
type SMTP struct {
	Config
	Timeout time.Duration // per message; 0 = 30s
}

// NewSMTP checks c and returns a sender for it.
func NewSMTP(c Config) (*SMTP, error) {
	if err := c.Check(); err != nil {
		return nil, err
	}
	return &SMTP{Config: c}, nil
}

var errHeader = errors.New("mail: line breaks in a header")

// Send delivers m. Passwords are only sent over TLS (net/smtp refuses
// otherwise, except to localhost).
func (s *SMTP) Send(ctx context.Context, m Message) error {
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return err
	}
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		return err
	}
	msg, err := Compose(s.From, m, time.Now())
	if err != nil {
		return err
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	tlsConf := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	if s.Port == 465 {
		conn, err = (&tls.Dialer{Config: tlsConf}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return err
	}
	defer c.Close()
	if s.Port != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsConf); err != nil {
				return err
			}
		}
	}
	if s.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	if err := c.Rcpt(to.Address); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// Compose renders m as an RFC 5322 message from from.
func Compose(from string, m Message, now time.Time) ([]byte, error) {
	for _, h := range []string{from, m.To, m.Subject} {
		if strings.ContainsAny(h, "\r\n") {
			return nil, errHeader
		}
	}
	f, err := mail.ParseAddress(from)
	if err != nil {
		return nil, err
	}
	t, err := mail.ParseAddress(m.To)
	if err != nil {
		return nil, err
	}
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := "localhost"
	if _, d, ok := strings.Cut(f.Address, "@"); ok {
		domain = d
	}
	var b strings.Builder
	b.WriteString("From: " + f.String() + "\r\n")
	b.WriteString("To: " + t.String() + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", m.Subject) + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: <" + hex.EncodeToString(id) + "@" + domain + ">\r\n")
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	body := strings.ReplaceAll(strings.ReplaceAll(m.Body, "\r\n", "\n"), "\n", "\r\n")
	b.WriteString(body) // net/smtp's DATA writer dot-stuffs lines that start with "."
	if !strings.HasSuffix(body, "\r\n") {
		b.WriteString("\r\n")
	}
	return []byte(b.String()), nil
}
