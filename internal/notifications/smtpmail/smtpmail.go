// Package smtpmail is the SMTP adapter for notifications. It works with any
// provider offering SMTP submission (and with MailHog/Mailpit locally),
// using TLS (implicit or STARTTLS) and never sending credentials in clear.
package smtpmail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
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

	"webee_rentor/internal/notifications"
)

// TLSMode selects how the connection is secured.
type TLSMode string

const (
	TLSImplicit TLSMode = "tls"      // port 465
	TLSStartTLS TLSMode = "starttls" // port 587; refuses servers without STARTTLS
	TLSNone     TLSMode = "none"     // local development only, no credentials
)

// Config is the SMTP account. Password comes from the environment and is
// never logged.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string // address, e.g. no-reply@shop.example.vn
	FromName string
	TLS      TLSMode
}

// Sender delivers notifications.Message over SMTP.
type Sender struct {
	cfg  Config
	from mail.Address
}

// New validates the configuration.
func New(cfg Config) (*Sender, error) {
	if cfg.Host == "" || cfg.Port < 1 || cfg.Port > 65535 {
		return nil, errors.New("smtpmail: SMTP_HOST and a valid SMTP_PORT are required")
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil || from.Name != "" {
		return nil, errors.New("smtpmail: SMTP_FROM must be a bare email address")
	}
	switch cfg.TLS {
	case TLSImplicit, TLSStartTLS:
	case TLSNone:
		if cfg.Username != "" || cfg.Password != "" {
			return nil, errors.New("smtpmail: credentials are never sent without TLS")
		}
	default:
		return nil, fmt.Errorf("smtpmail: SMTP_TLS must be tls, starttls or none, not %q", cfg.TLS)
	}
	if strings.ContainsAny(cfg.FromName, "\r\n") {
		return nil, errors.New("smtpmail: SMTP_FROM_NAME must be a single line")
	}
	return &Sender{cfg: cfg, from: mail.Address{Name: cfg.FromName, Address: from.Address}}, nil
}

// Send delivers one message within ctx's deadline.
func (s *Sender) Send(ctx context.Context, m notifications.Message) error {
	to, err := mail.ParseAddress(m.To)
	if err != nil || to.Name != "" || strings.ContainsAny(m.Subject, "\r\n") {
		return fmt.Errorf("smtpmail: invalid recipient or subject for %s", m.Kind)
	}
	msg, err := s.compose(to.Address, m)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtpmail: dial: %w", err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsCfg := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
	if s.cfg.TLS == TLSImplicit {
		conn = tls.Client(conn, tlsCfg)
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtpmail: greeting: %w", err)
	}
	defer c.Close()
	if s.cfg.TLS == TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtpmail: server does not offer STARTTLS")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtpmail: starttls: %w", err)
		}
	}
	if s.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("smtpmail: auth: %w", err)
		}
	}
	if err := c.Mail(s.from.Address); err != nil {
		return fmt.Errorf("smtpmail: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to.Address); err != nil {
		return fmt.Errorf("smtpmail: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtpmail: DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtpmail: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtpmail: end of data: %w", err)
	}
	return c.Quit()
}

// compose builds an RFC 5322 message with a UTF-8 subject and a base64
// UTF-8 plain-text body.
func (s *Sender) compose(to string, m notifications.Message) ([]byte, error) {
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	domain := s.from.Address[strings.LastIndexByte(s.from.Address, '@')+1:]
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", s.from.String())
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", hex.EncodeToString(id), domain)
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	body := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(m.Body, "\n", "\r\n")))
	for len(body) > 76 {
		b.WriteString(body[:76] + "\r\n")
		body = body[76:]
	}
	b.WriteString(body + "\r\n")
	return []byte(b.String()), nil
}
