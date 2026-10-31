// Package supportpg stores support requests in PostgreSQL.
package supportpg

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/support"
)

// Insert stores a new request with its messages.
func Insert(ctx context.Context, q postgres.Querier, r support.Request) error {
	var code *string
	if r.BookingCode != "" {
		code = &r.BookingCode
	}
	if _, err := q.Exec(ctx, `INSERT INTO support_requests (id, code, customer_id, topic, subject, status, booking_code, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`, r.ID, r.Code, r.CustomerID, r.Topic, r.Subject, r.Status, code, r.CreatedAt); err != nil {
		return fmt.Errorf("insert support request %s: %w", r.ID, err)
	}
	for _, m := range r.Messages {
		if err := AddMessage(ctx, q, r.ID, m); err != nil {
			return err
		}
	}
	return nil
}

// AddMessage appends one message.
func AddMessage(ctx context.Context, q postgres.Querier, id string, m support.Message) error {
	if _, err := q.Exec(ctx, `INSERT INTO support_messages (request_id, sender, author_id, body, at) VALUES ($1, $2, $3, $4, $5)`,
		id, m.Sender, m.Author, m.Body, m.At); err != nil {
		return fmt.Errorf("add message to support request %s: %w", id, err)
	}
	return nil
}

// SetStatus records where a request stands.
func SetStatus(ctx context.Context, q postgres.Querier, id string, s support.Status, now time.Time) error {
	if _, err := q.Exec(ctx, `UPDATE support_requests SET status = $2, updated_at = $3 WHERE id = $1`, id, s, now.UTC()); err != nil {
		return fmt.Errorf("update support request %s: %w", id, err)
	}
	return nil
}

const requestColumns = `id, code, customer_id, topic, subject, status, coalesce(booking_code, ''), created_at`

func scanRequest(r pgx.CollectableRow) (support.Request, error) {
	var s support.Request
	err := r.Scan(&s.ID, &s.Code, &s.CustomerID, &s.Topic, &s.Subject, &s.Status, &s.BookingCode, &s.CreatedAt)
	s.CreatedAt = s.CreatedAt.UTC()
	return s, err
}

// Load reads a request with its messages; lock holds it for a reply.
func Load(ctx context.Context, q postgres.Querier, id string, lock bool) (support.Request, error) {
	query := `SELECT ` + requestColumns + ` FROM support_requests WHERE id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	rows, err := q.Query(ctx, query, id)
	if err != nil {
		return support.Request{}, fmt.Errorf("load support request %s: %w", id, err)
	}
	s, err := pgx.CollectExactlyOneRow(rows, scanRequest)
	if err != nil {
		return support.Request{}, postgres.NotFound(err, fmt.Sprintf("support request %s", id))
	}
	all := []support.Request{s}
	if err := attachMessages(ctx, q, all); err != nil {
		return support.Request{}, err
	}
	return all[0], nil
}

// List reads requests, unresolved first and newest first. An empty customer
// lists everyone's.
func List(ctx context.Context, q postgres.Querier, customer identity.UserID, limit int) ([]support.Request, error) {
	rows, err := q.Query(ctx, `SELECT `+requestColumns+` FROM support_requests WHERE ($1 = '' OR customer_id = $1)
		ORDER BY status = 'RESOLVED', created_at DESC, id LIMIT $2`, string(customer), limit)
	if err != nil {
		return nil, fmt.Errorf("list support requests: %w", err)
	}
	all, err := pgx.CollectRows(rows, scanRequest)
	if err != nil {
		return nil, fmt.Errorf("list support requests: %w", err)
	}
	return all, attachMessages(ctx, q, all)
}

func attachMessages(ctx context.Context, q postgres.Querier, all []support.Request) error {
	ids := make([]string, len(all))
	for i, r := range all {
		ids[i] = r.ID
	}
	rows, err := q.Query(ctx, `SELECT request_id, sender, author_id, body, at FROM support_messages WHERE request_id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return fmt.Errorf("load support messages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var m support.Message
		if err := rows.Scan(&id, &m.Sender, &m.Author, &m.Body, &m.At); err != nil {
			return fmt.Errorf("load support messages: %w", err)
		}
		m.At = m.At.UTC()
		if i := slices.IndexFunc(all, func(r support.Request) bool { return r.ID == id }); i >= 0 {
			all[i].Messages = append(all[i].Messages, m)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load support messages: %w", err)
	}
	return nil
}
