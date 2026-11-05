// Package retentionapp runs the data retention use cases (decision D39): an
// admin reviews customer profiles of unused accounts and erases them, and a
// job deletes support conversations some time after they were resolved.
package retentionapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/payments/paymentspg"
	"webee_rentor/internal/platform/ids"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
	"webee_rentor/internal/retention"
	"webee_rentor/internal/retention/retentionpg"
)

// Service holds the dependencies of the retention use cases.
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

// ProfileReview is a profile due for review and what still keeps it.
type ProfileReview struct {
	retentionpg.Profile
	Blockers []retention.Blocker
}

// InactiveProfiles lists the customer profiles of accounts unused for the
// retention period, with what keeps each one.
func (s *Service) InactiveProfiles(ctx context.Context, p identity.Principal, page, size int) ([]ProfileReview, int, error) {
	if err := p.Require(identity.PermCustomerProfileErase); err != nil {
		return nil, 0, err
	}
	now := s.now().UTC()
	rows, total, err := retentionpg.InactiveProfiles(ctx, s.pool, retention.ProfileReviewBefore(now), page, size)
	if err != nil {
		return nil, 0, err
	}
	out := make([]ProfileReview, len(rows))
	for i, r := range rows {
		f, err := factsOf(ctx, s.pool, r)
		if err != nil {
			return nil, 0, err
		}
		out[i] = ProfileReview{Profile: r, Blockers: f.Blockers(now)}
	}
	return out, total, nil
}

// factsOf adds what the ledgers and fees of finished bookings say.
func factsOf(ctx context.Context, q postgres.Querier, r retentionpg.Profile) (retention.ProfileFacts, error) {
	f := retention.ProfileFacts{LastActivity: r.LastActivity, OpenBookings: r.OpenBookings, OpenSupport: r.OpenSupport}
	for _, id := range r.Finished {
		l, _, err := paymentspg.LoadLedger(ctx, q, string(id), false)
		if err != nil {
			return retention.ProfileFacts{}, err
		}
		if !l.Summary().IsSettled() {
			f.UnsettledBookings++
		}
		fees, err := rentalspg.AssessmentsOfBooking(ctx, q, id)
		if err != nil {
			return retention.ProfileFacts{}, err
		}
		f.OpenFees += rentals.CountOpenAssessments(fees)
	}
	return f, nil
}

// EraseProfile deletes a customer's name and phone once the account has been
// unused for the retention period and nothing keeps the profile. The account,
// its email and its bookings stay. The customer's lock queues the erasure
// with a booking being placed, so a new booking either counts as activity or
// waits.
func (s *Service) EraseProfile(ctx context.Context, p identity.Principal, user identity.UserID, reason string) error {
	if err := p.Require(identity.PermCustomerProfileErase); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return retention.ErrReasonRequired
	}
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := rentalspg.LockCustomerHolds(ctx, tx, user); err != nil {
			return err
		}
		r, err := retentionpg.ProfileOf(ctx, tx, user)
		if err != nil {
			return err
		}
		f, err := factsOf(ctx, tx, r)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if err := f.CheckErasable(now); err != nil {
			return err
		}
		if err := retentionpg.DeleteProfile(ctx, tx, user); err != nil {
			return err
		}
		return auditpg.Append(ctx, tx, audit.Event{
			ID: s.newID(), OccurredAt: now, Actor: p.Actor(), Action: "customer.profile_erased", ResourceType: "user",
			ResourceID: string(user), Result: audit.ResultSuccess, Reason: reason,
			References: map[string]string{"last_activity": r.LastActivity.Format(time.RFC3339)},
		})
	})
	if err != nil {
		return fmt.Errorf("erase profile of %s: %w", user, err)
	}
	return nil
}

// PurgeSupport deletes up to batch conversations resolved longer ago than the
// retention period, each with an audit record of its code. Safe to run
// concurrently and repeatedly.
func (s *Service) PurgeSupport(ctx context.Context, batch int) (int, error) {
	n := 0
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		n = 0
		now := s.now().UTC()
		due, err := retentionpg.ResolvedBefore(ctx, tx, retention.SupportPurgeBefore(now), batch)
		if err != nil {
			return err
		}
		for _, r := range due {
			if err := retentionpg.DeleteSupportRequest(ctx, tx, r.ID); err != nil {
				return err
			}
			if err := auditpg.Append(ctx, tx, audit.Event{
				ID: s.newID(), OccurredAt: now, Actor: identity.SystemActor("retention"), Action: "support.request_purged",
				ResourceType: "support_request", ResourceID: r.ID, Result: audit.ResultSuccess, Reason: "decision D39",
				References: map[string]string{"code": r.Code, "resolved_at": r.ResolvedAt.Format(time.RFC3339)},
			}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("purge resolved support requests: %w", err)
	}
	return n, nil
}
