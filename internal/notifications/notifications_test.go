package notifications_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"webee_rentor/internal/notifications"
)

type fakeSender struct {
	mu       sync.Mutex
	failures int // fail this many sends first
	sent     []notifications.Message
	block    chan struct{}
}

func (f *fakeSender) Send(_ context.Context, m notifications.Message) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures > 0 {
		f.failures--
		return errors.New("temporary failure")
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeSender) messages() []notifications.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]notifications.Message(nil), f.sent...)
}

func queue(t *testing.T, s notifications.Sender, size int) *notifications.Queue {
	t.Helper()
	q, err := notifications.NewQueue(s, notifications.QueueConfig{Size: size, Workers: 1, Timeout: time.Second, Attempts: 3, Backoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestQueueRetriesAndDrainsOnClose(t *testing.T) {
	s := &fakeSender{failures: 2}
	q := queue(t, s, 4)
	if err := q.Enqueue(notifications.Message{To: "a@example.test", Kind: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.messages()) != 1 {
		t.Fatalf("message must be delivered on the third attempt: %v", s.messages())
	}
	if err := q.Enqueue(notifications.Message{}); !errors.Is(err, notifications.ErrQueueClosed) {
		t.Fatalf("enqueue after close err = %v", err)
	}
}

func TestQueueNeverBlocksTheCaller(t *testing.T) {
	s := &fakeSender{block: make(chan struct{})}
	q := queue(t, s, 1)
	// One message is held by the worker, one fills the buffer.
	_ = q.Enqueue(notifications.Message{Kind: "1"})
	deadline := time.Now().Add(time.Second)
	var err error
	for time.Now().Before(deadline) {
		if err = q.Enqueue(notifications.Message{Kind: "n"}); errors.Is(err, notifications.ErrQueueFull) {
			break
		}
	}
	if !errors.Is(err, notifications.ErrQueueFull) {
		t.Fatalf("a full queue must fail fast, err = %v", err)
	}
	close(s.block)
	_ = q.Close(context.Background())
}

func TestAccountMailLinksCarryTheTokenInTheFragment(t *testing.T) {
	s := &fakeSender{}
	q := queue(t, s, 4)
	m, err := notifications.NewAccountMail(q, "https://shop.example.vn/", "Webee", false)
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Date(2026, 11, 1, 2, 30, 0, 0, time.UTC)
	_ = m.VerifyEmail(context.Background(), "a@example.test", "TOKEN123", exp)
	_ = m.PasswordReset(context.Background(), "a@example.test", "TOKEN456", exp)
	_ = m.AccountExists(context.Background(), "a@example.test")
	_ = q.Close(context.Background())
	got := s.messages()
	if len(got) != 3 {
		t.Fatalf("messages = %v", got)
	}
	if !strings.Contains(got[0].Body, "https://shop.example.vn/verify-email#token=TOKEN123") ||
		!strings.Contains(got[0].Body, "09:30 01/11/2026") {
		t.Fatalf("verification body:\n%s", got[0].Body)
	}
	if !strings.Contains(got[1].Body, "https://shop.example.vn/reset-password#token=TOKEN456") {
		t.Fatalf("reset body:\n%s", got[1].Body)
	}
	if strings.Contains(got[2].Body, "token") || !strings.Contains(got[2].Body, "https://shop.example.vn/forgot-password") {
		t.Fatalf("account-exists body:\n%s", got[2].Body)
	}

	for _, bad := range []string{"http://shop.example.vn", "shop.example.vn", "https://shop.example.vn/?a=1"} {
		if _, err := notifications.NewAccountMail(q, bad, "Webee", false); err == nil {
			t.Errorf("base URL %q accepted", bad)
		}
	}
	if _, err := notifications.NewAccountMail(q, "http://localhost:3000", "Webee", true); err != nil {
		t.Errorf("local http must be allowed in development: %v", err)
	}
}
