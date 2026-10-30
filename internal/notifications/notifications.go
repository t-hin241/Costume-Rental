// Package notifications delivers messages to customers and staff. Sending
// happens in the background after the business transaction has committed;
// a failed delivery is logged and retried a bounded number of times, and
// never changes a booking, payment or account.
package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Message is one plain-text email.
type Message struct {
	To      string
	Subject string
	Body    string
	// Kind names the template, for logs (recipients are not logged).
	Kind string
}

// Sender delivers a message (smtpmail.Sender).
type Sender interface {
	Send(ctx context.Context, m Message) error
}

var (
	ErrQueueFull   = errors.New("notifications: queue full, message dropped")
	ErrQueueClosed = errors.New("notifications: queue closed")
)

// QueueConfig bounds the background sender.
type QueueConfig struct {
	Size     int           // buffered messages; beyond it Enqueue fails
	Workers  int           // concurrent deliveries
	Timeout  time.Duration // per delivery attempt
	Attempts int           // delivery attempts per message
	Backoff  time.Duration // wait before the second attempt, doubled after
}

// DefaultQueueConfig suits a single shop.
func DefaultQueueConfig() QueueConfig {
	return QueueConfig{Size: 256, Workers: 2, Timeout: 15 * time.Second, Attempts: 3, Backoff: 2 * time.Second}
}

// Queue sends messages in the background so callers never wait for the
// mail server, which also keeps response times independent of whether a
// message was sent.
type Queue struct {
	sender Sender
	cfg    QueueConfig
	ch     chan Message
	wg     sync.WaitGroup
	stop   chan struct{}

	mu     sync.Mutex
	closed bool
}

// NewQueue starts the workers.
func NewQueue(s Sender, cfg QueueConfig) (*Queue, error) {
	if s == nil || cfg.Size < 1 || cfg.Workers < 1 || cfg.Timeout <= 0 || cfg.Attempts < 1 || cfg.Backoff < 0 {
		return nil, errors.New("notifications: invalid queue configuration")
	}
	q := &Queue{sender: s, cfg: cfg, ch: make(chan Message, cfg.Size), stop: make(chan struct{})}
	q.wg.Add(cfg.Workers)
	for range cfg.Workers {
		go q.work()
	}
	return q, nil
}

// Enqueue schedules m without blocking.
func (q *Queue) Enqueue(m Message) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrQueueClosed
	}
	select {
	case q.ch <- m:
		return nil
	default:
		return ErrQueueFull
	}
}

// Close stops accepting messages and waits for queued ones to be sent
// until ctx ends; then pending retries are abandoned.
func (q *Queue) Close(ctx context.Context) error {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.ch)
	}
	q.mu.Unlock()
	done := make(chan struct{})
	go func() { q.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		close(q.stop)
		<-done
		return fmt.Errorf("notifications: closed before the queue drained: %w", ctx.Err())
	}
}

func (q *Queue) work() {
	defer q.wg.Done()
	for m := range q.ch {
		q.deliver(m)
	}
}

func (q *Queue) deliver(m Message) {
	backoff := q.cfg.Backoff
	var err error
	for attempt := 1; attempt <= q.cfg.Attempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), q.cfg.Timeout)
		err = q.sender.Send(ctx, m)
		cancel()
		if err == nil {
			return
		}
		if attempt == q.cfg.Attempts {
			break
		}
		select {
		case <-time.After(backoff):
		case <-q.stop:
			slog.Error("email abandoned at shutdown", "kind", m.Kind, "error", err)
			return
		}
		backoff *= 2
	}
	slog.Error("email not delivered", "kind", m.Kind, "attempts", q.cfg.Attempts, "error", err)
}
