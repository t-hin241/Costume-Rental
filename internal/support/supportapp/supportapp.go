// Package supportapp runs the support use cases: customers ask about their own
// matters and staff who can read bookings answer them. A support request
// carries the same customer data as a booking, so it needs the same permission.
package supportapp

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identitypg"
	"webee_rentor/internal/platform/ids"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/support"
	"webee_rentor/internal/support/supportpg"
)

// maxList bounds one listing; old resolved requests fall off the end.
const maxList = 200

// Service holds the dependencies of the support use cases.
type Service struct {
	pool  *pgxpool.Pool
	now   func() time.Time
	newID func() string
}

// New builds the service.
func New(pool *pgxpool.Pool, opts ...Option) *Service {
	s := &Service{pool: pool, now: time.Now, newID: ids.New}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Option configures the service.
type Option func(*Service)

// WithClock replaces time.Now, for tests.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// View is a request with who is who, as its reader may see it.
type View struct {
	support.Request
	CustomerName string
	// Authors names each message, in order.
	Authors []string
}

func (s *Service) views(ctx context.Context, all []support.Request, asCustomer bool) ([]View, error) {
	var users []identity.UserID
	for _, r := range all {
		users = append(users, r.CustomerID)
		for _, m := range r.Messages {
			users = append(users, m.Author)
		}
	}
	people, err := identitypg.Contacts(ctx, s.pool, users)
	if err != nil {
		return nil, err
	}
	out := make([]View, len(all))
	for i, r := range all {
		v := View{Request: r, CustomerName: people[r.CustomerID].Label()}
		for _, m := range r.Messages {
			switch {
			case asCustomer && m.Sender == support.SenderCustomer:
				v.Authors = append(v.Authors, "Bạn")
			case asCustomer:
				v.Authors = append(v.Authors, "Cửa hàng")
			default:
				v.Authors = append(v.Authors, people[m.Author].Label())
			}
		}
		out[i] = v
	}
	return out, nil
}

// Mine lists the principal's own requests.
func (s *Service) Mine(ctx context.Context, p identity.Principal) ([]View, error) {
	all, err := supportpg.List(ctx, s.pool, p.UserID, maxList)
	if err != nil {
		return nil, err
	}
	return s.views(ctx, all, true)
}

// All lists every request for staff.
func (s *Service) All(ctx context.Context, p identity.Principal) ([]View, error) {
	if err := p.Require(identity.PermBookingReadAll); err != nil {
		return nil, err
	}
	all, err := supportpg.List(ctx, s.pool, "", maxList)
	if err != nil {
		return nil, err
	}
	return s.views(ctx, all, false)
}

// CreateInput is a new question.
type CreateInput struct {
	Topic       support.Topic
	Subject     string
	BookingCode string
	Message     string
}

// Create opens a request. A booking code must be one of the customer's own,
// so a request cannot be used to probe other people's bookings.
func (s *Service) Create(ctx context.Context, p identity.Principal, in CreateInput) (View, error) {
	if in.BookingCode != "" {
		var found bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM bookings WHERE code = $1 AND customer_id = $2)`, in.BookingCode, p.UserID).Scan(&found); err != nil {
			return View{}, fmt.Errorf("check booking of support request: %w", err)
		}
		if !found {
			return View{}, domainerr.Invalid("booking_code", "not one of your bookings")
		}
	}
	r, err := support.New(s.newID(), ids.Code("HT", 8), p.UserID, in.Topic, in.Subject, in.Message, in.BookingCode, s.now())
	if err != nil {
		return View{}, err
	}
	if err := supportpg.Insert(ctx, s.pool, r); err != nil {
		return View{}, err
	}
	views, err := s.views(ctx, []support.Request{r}, true)
	if err != nil {
		return View{}, err
	}
	return views[0], nil
}

// Reply answers a request and optionally resolves it.
func (s *Service) Reply(ctx context.Context, p identity.Principal, id, message string, resolve bool) (View, error) {
	if err := p.Require(identity.PermBookingReadAll); err != nil {
		return View{}, err
	}
	var out support.Request
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := supportpg.Load(ctx, tx, id, true)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		m, err := r.Reply(p.UserID, message, resolve, now)
		if err != nil {
			return err
		}
		if err := supportpg.AddMessage(ctx, tx, id, m); err != nil {
			return err
		}
		if err := supportpg.SetStatus(ctx, tx, id, r.Status, now); err != nil {
			return err
		}
		out = r
		return auditpg.Append(ctx, tx, audit.Event{
			ID: s.newID(), OccurredAt: now, Actor: p.Actor(), Action: "support.replied", ResourceType: "support_request",
			ResourceID: id, Result: audit.ResultSuccess,
		})
	})
	if err != nil {
		return View{}, fmt.Errorf("reply to support request: %w", err)
	}
	views, err := s.views(ctx, []support.Request{out}, false)
	if err != nil {
		return View{}, err
	}
	return views[0], nil
}
