package smtpmail_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"mime"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"testing"
	"time"

	"webee_rentor/internal/notifications"
	"webee_rentor/internal/notifications/smtpmail"
)

// fakeServer speaks just enough SMTP to accept one message and returns the
// DATA section.
func fakeServer(t *testing.T, starttls bool) (string, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	out := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		say("220 fake ESMTP")
		var data strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				if starttls {
					say("250-fake\r\n250 STARTTLS")
				} else {
					say("250 fake")
				}
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				say("250 ok")
			case cmd == "DATA":
				say("354 go ahead")
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
					data.WriteString(l)
				}
				say("250 queued")
				out <- data.String()
			case cmd == "QUIT":
				say("221 bye")
				return
			default:
				say("502 unsupported")
			}
		}
	}()
	return ln.Addr().String(), out
}

func sender(t *testing.T, addr string, mode smtpmail.TLSMode) *smtpmail.Sender {
	t.Helper()
	host, port, _ := net.SplitHostPort(addr)
	p, _ := strconv.Atoi(port)
	s, err := smtpmail.New(smtpmail.Config{Host: host, Port: p, From: "no-reply@shop.example.vn", FromName: "Cửa hàng Webee", TLS: mode})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSendsAUTF8PlainTextMessage(t *testing.T) {
	addr, got := fakeServer(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := sender(t, addr, smtpmail.TLSNone).Send(ctx, notifications.Message{
		To: "khach@example.test", Subject: "Xác minh email", Body: "Xin chào,\nliên kết: https://x/#token=abc\n", Kind: "verify-email"})
	if err != nil {
		t.Fatal(err)
	}
	raw := <-got
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if subject != "Xác minh email" || msg.Header.Get("To") != "khach@example.test" ||
		!strings.Contains(msg.Header.Get("From"), "no-reply@shop.example.vn") {
		t.Fatalf("headers = %v", msg.Header)
	}
	var body strings.Builder
	sc := bufio.NewScanner(msg.Body)
	for sc.Scan() {
		body.WriteString(sc.Text())
	}
	plain, err := base64.StdEncoding.DecodeString(body.String())
	if err != nil || !strings.Contains(string(plain), "liên kết: https://x/#token=abc") {
		t.Fatalf("body = %q, %v", plain, err)
	}
}

func TestRefusesToContinueWithoutSTARTTLS(t *testing.T) {
	addr, _ := fakeServer(t, false)
	err := sender(t, addr, smtpmail.TLSStartTLS).Send(context.Background(), notifications.Message{To: "a@example.test", Subject: "s", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v", err)
	}
}

func TestRejectsUnsafeConfigurationAndHeaders(t *testing.T) {
	if _, err := smtpmail.New(smtpmail.Config{Host: "h", Port: 25, From: "a@b.c", TLS: smtpmail.TLSNone, Username: "u", Password: "p"}); err == nil {
		t.Error("credentials without TLS accepted")
	}
	if _, err := smtpmail.New(smtpmail.Config{Host: "h", Port: 25, From: "A <a@b.c>", TLS: smtpmail.TLSStartTLS}); err == nil {
		t.Error("display name in SMTP_FROM accepted")
	}
	s, _ := smtpmail.New(smtpmail.Config{Host: "127.0.0.1", Port: 1, From: "a@b.c", TLS: smtpmail.TLSNone})
	if err := s.Send(context.Background(), notifications.Message{To: "a@b.c", Subject: "x\r\nBcc: evil@x.y"}); err == nil {
		t.Error("header injection accepted")
	}
}
