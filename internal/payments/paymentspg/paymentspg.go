// Package paymentspg stores the payment ledger of a booking in PostgreSQL.
//
// Every charge, payment, refund, deduction and reservation resolution is its
// own row; balances are never stored. A ledger is written as a whole by the
// use case holding the booking lock: new rows are inserted and the rows the
// domain may change (a reversal, a status) are updated. Each write names the
// version it read, so a concurrent change is reported as postgres.ErrStale.
package paymentspg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/platform/postgres"
)

// Version is the optimistic concurrency token of a stored ledger; zero means
// not stored yet.
type Version int

// SaveLedger inserts (version 0) or updates the ledger and its records,
// returning the new version.
func SaveLedger(ctx context.Context, q postgres.Querier, l *payments.Ledger, v Version, now time.Time) (Version, error) {
	cancellation, err := encode(l.Cancellation)
	if err != nil {
		return 0, fmt.Errorf("encode ledger %s cancellation: %w", l.BookingRef, err)
	}
	staged, err := encode(l.Staged)
	if err != nil {
		return 0, fmt.Errorf("encode ledger %s repricing: %w", l.BookingRef, err)
	}
	if v == 0 {
		_, err = q.Exec(ctx, `
			INSERT INTO payment_ledgers (booking_id, currency, credit_reservation_to_rent, cancellation,
				staged_repricing, committed_repricings, rent_reductions, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			l.BookingRef, l.Currency, l.CreditReservationToRent, cancellation, staged,
			orEmpty(l.CommittedRepricings), orEmpty(l.RentReductions), now.UTC())
		if err != nil {
			return 0, fmt.Errorf("insert ledger %s: %w", l.BookingRef, err)
		}
	} else {
		tag, err := q.Exec(ctx, `
			UPDATE payment_ledgers SET cancellation = $2, staged_repricing = $3, committed_repricings = $4,
				rent_reductions = $5, updated_at = $6, version = version + 1
			WHERE booking_id = $1 AND version = $7`,
			l.BookingRef, cancellation, staged, orEmpty(l.CommittedRepricings), orEmpty(l.RentReductions), now.UTC(), int(v))
		if err != nil {
			return 0, fmt.Errorf("update ledger %s: %w", l.BookingRef, err)
		}
		if tag.RowsAffected() == 0 {
			return 0, fmt.Errorf("ledger %s version %d: %w", l.BookingRef, v, postgres.ErrStale)
		}
	}
	for _, save := range []func(context.Context, postgres.Querier, *payments.Ledger) error{
		saveCharges, savePayments, saveRefunds, saveDeductions, saveResolutions,
	} {
		if err := save(ctx, q, l); err != nil {
			return 0, err
		}
	}
	if err := saveEvents(ctx, q, l, now); err != nil {
		return 0, err
	}
	return v + 1, nil
}

func saveCharges(ctx context.Context, q postgres.Querier, l *payments.Ledger) error {
	for i, c := range l.Charges {
		var reason *string
		var reversedBy *identity.Actor
		var reversedAt *time.Time
		if c.Reversal != nil {
			reason, reversedBy, reversedAt = &c.Reversal.Reason, &c.Reversal.By, &c.Reversal.At
		}
		byKind, byID := actorColumns(reversedBy)
		approvedKind, approvedID := actorColumns(c.ApprovedBy)
		if _, err := q.Exec(ctx, `
			INSERT INTO ledger_charges (id, booking_id, position, kind, amount, reason, evidence,
				created_by_kind, created_by_id, created_at, reversal_reason, reversed_by_kind, reversed_by_id,
				reversed_at, assessment_ref, approved_by_kind, approved_by_id, delivery_leg)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, nullif($15, ''), $16, $17, nullif($18, ''))
			ON CONFLICT (id) DO UPDATE SET reversal_reason = EXCLUDED.reversal_reason,
				reversed_by_kind = EXCLUDED.reversed_by_kind, reversed_by_id = EXCLUDED.reversed_by_id,
				reversed_at = EXCLUDED.reversed_at`,
			c.ID, l.BookingRef, i, c.Kind, c.Amount.Amount(), c.Reason, orEmpty(c.Evidence),
			c.CreatedBy.Kind, c.CreatedBy.ID, c.CreatedAt, reason, byKind, byID, reversedAt,
			c.AssessmentRef, approvedKind, approvedID, c.DeliveryLeg); err != nil {
			return fmt.Errorf("ledger %s charge %s: %w", l.BookingRef, c.ID, err)
		}
	}
	return nil
}

func savePayments(ctx context.Context, q postgres.Querier, l *payments.Ledger) error {
	for _, p := range l.Payments {
		if _, err := q.Exec(ctx, `
			INSERT INTO payments (id, booking_id, idempotency_key, method, provider, provider_txn_id, amount, status,
				created_by_kind, created_by_id, created_at, settled_at, failure_reason, needs_review, modification_ref)
			VALUES ($1, $2, $3, $4, $5, nullif($6, ''), $7, $8, $9, $10, $11, $12, $13, $14, nullif($15, ''))
			ON CONFLICT (id) DO UPDATE SET provider_txn_id = EXCLUDED.provider_txn_id, status = EXCLUDED.status,
				settled_at = EXCLUDED.settled_at, failure_reason = EXCLUDED.failure_reason,
				needs_review = EXCLUDED.needs_review`,
			p.ID, l.BookingRef, p.IdempotencyKey, p.Method, p.Provider, p.ProviderTxnID, p.Amount.Amount(), p.Status,
			p.CreatedBy.Kind, p.CreatedBy.ID, p.CreatedAt, p.SettledAt, p.FailureReason, p.NeedsReview, p.ModificationRef); err != nil {
			return fmt.Errorf("ledger %s payment %s: %w", l.BookingRef, p.ID, err)
		}
		for _, pt := range p.Portions {
			if _, err := q.Exec(ctx, `
				INSERT INTO payment_portions (payment_id, bucket, amount) VALUES ($1, $2, $3)
				ON CONFLICT (payment_id, bucket) DO NOTHING`, p.ID, pt.Bucket, pt.Amount.Amount()); err != nil {
				return fmt.Errorf("ledger %s payment %s portion: %w", l.BookingRef, p.ID, err)
			}
		}
	}
	return nil
}

func saveRefunds(ctx context.Context, q postgres.Querier, l *payments.Ledger) error {
	for _, r := range l.Refunds {
		decidedKind, decidedID := actorColumns(r.DecidedBy)
		recordedKind, recordedID := actorColumns(r.RecordedBy)
		if _, err := q.Exec(ctx, `
			INSERT INTO refunds (id, booking_id, idempotency_key, bucket, amount, reason, status,
				requested_by_kind, requested_by_id, requested_at, decided_by_kind, decided_by_id, decided_at,
				decision_note, provider, provider_ref, completed_at, failure_reason, method, reference,
				recorded_by_kind, recorded_by_id, recorded_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, nullif($16, ''), $17, $18,
				nullif($19, ''), $20, $21, $22, $23)
			ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status, decided_by_kind = EXCLUDED.decided_by_kind,
				decided_by_id = EXCLUDED.decided_by_id, decided_at = EXCLUDED.decided_at,
				decision_note = EXCLUDED.decision_note, provider = EXCLUDED.provider,
				provider_ref = EXCLUDED.provider_ref, completed_at = EXCLUDED.completed_at,
				failure_reason = EXCLUDED.failure_reason, method = EXCLUDED.method, reference = EXCLUDED.reference,
				recorded_by_kind = EXCLUDED.recorded_by_kind, recorded_by_id = EXCLUDED.recorded_by_id,
				recorded_at = EXCLUDED.recorded_at`,
			r.ID, l.BookingRef, r.IdempotencyKey, r.Bucket, r.Amount.Amount(), r.Reason, r.Status,
			r.RequestedBy.Kind, r.RequestedBy.ID, r.RequestedAt, decidedKind, decidedID, r.DecidedAt,
			r.DecisionNote, r.Provider, r.ProviderRef, r.CompletedAt, r.FailureReason, r.Method, r.Reference,
			recordedKind, recordedID, r.RecordedAt); err != nil {
			return fmt.Errorf("ledger %s refund %s: %w", l.BookingRef, r.ID, err)
		}
	}
	return nil
}

func saveDeductions(ctx context.Context, q postgres.Querier, l *payments.Ledger) error {
	for _, d := range l.Deductions {
		decidedKind, decidedID := actorColumns(d.DecidedBy)
		reversedKind, reversedID := actorColumns(d.ReversedBy)
		if _, err := q.Exec(ctx, `
			INSERT INTO security_deductions (id, booking_id, charge_id, amount, reason, evidence, status,
				proposed_by_kind, proposed_by_id, proposed_at, decided_by_kind, decided_by_id, decided_at, decision_note,
				reversed_by_kind, reversed_by_id, reversed_at, reversal_reason)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
			ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status, decided_by_kind = EXCLUDED.decided_by_kind,
				decided_by_id = EXCLUDED.decided_by_id, decided_at = EXCLUDED.decided_at,
				decision_note = EXCLUDED.decision_note, reversed_by_kind = EXCLUDED.reversed_by_kind,
				reversed_by_id = EXCLUDED.reversed_by_id, reversed_at = EXCLUDED.reversed_at,
				reversal_reason = EXCLUDED.reversal_reason`,
			d.ID, l.BookingRef, d.ChargeID, d.Amount.Amount(), d.Reason, orEmpty(d.Evidence), d.Status,
			d.ProposedBy.Kind, d.ProposedBy.ID, d.ProposedAt, decidedKind, decidedID, d.DecidedAt, d.DecisionNote,
			reversedKind, reversedID, d.ReversedAt, d.ReversalReason); err != nil {
			return fmt.Errorf("ledger %s deduction %s: %w", l.BookingRef, d.ID, err)
		}
	}
	return nil
}

func saveResolutions(ctx context.Context, q postgres.Querier, l *payments.Ledger) error {
	for _, r := range l.Resolutions {
		if _, err := q.Exec(ctx, `
			INSERT INTO reservation_resolutions (id, booking_id, kind, amount, reason, created_by_kind, created_by_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (id) DO NOTHING`,
			r.ID, l.BookingRef, r.Kind, r.Amount.Amount(), r.Reason, r.CreatedBy.Kind, r.CreatedBy.ID, r.CreatedAt); err != nil {
			return fmt.Errorf("ledger %s resolution %s: %w", l.BookingRef, r.ID, err)
		}
	}
	return nil
}

func saveEvents(ctx context.Context, q postgres.Querier, l *payments.Ledger, now time.Time) error {
	for key := range l.ProcessedEvents {
		provider, id, ok := strings.Cut(key, ":")
		if !ok {
			return fmt.Errorf("ledger %s event key %q", l.BookingRef, key)
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO provider_events (provider, event_id, booking_id, received_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT (provider, event_id) DO NOTHING`, provider, id, l.BookingRef, now.UTC()); err != nil {
			return fmt.Errorf("ledger %s event %s: %w", l.BookingRef, key, err)
		}
	}
	return nil
}

// LoadLedger reads a booking's ledger. With lock it is read FOR UPDATE, after
// the booking row (the global lock order).
func LoadLedger(ctx context.Context, q postgres.Querier, bookingRef string, lock bool) (*payments.Ledger, Version, error) {
	query := `SELECT currency, credit_reservation_to_rent, cancellation, staged_repricing, committed_repricings,
		rent_reductions, version FROM payment_ledgers WHERE booking_id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	l := &payments.Ledger{BookingRef: bookingRef, ProcessedEvents: map[string]bool{}}
	var cancellation, staged []byte
	var committed, reductions []string
	var v int
	err := q.QueryRow(ctx, query, bookingRef).Scan(&l.Currency, &l.CreditReservationToRent, &cancellation, &staged,
		&committed, &reductions, &v)
	if err != nil {
		return nil, 0, postgres.NotFound(err, fmt.Sprintf("ledger %s", bookingRef))
	}
	if cancellation != nil {
		l.Cancellation = &payments.Cancellation{}
		if err := json.Unmarshal(cancellation, l.Cancellation); err != nil {
			return nil, 0, fmt.Errorf("decode ledger %s cancellation: %w", bookingRef, err)
		}
	}
	if staged != nil {
		l.Staged = &payments.Repricing{}
		if err := json.Unmarshal(staged, l.Staged); err != nil {
			return nil, 0, fmt.Errorf("decode ledger %s repricing: %w", bookingRef, err)
		}
	}
	l.CommittedRepricings, l.RentReductions = orNil(committed), orNil(reductions)
	if l.Charges, err = loadCharges(ctx, q, l); err != nil {
		return nil, 0, err
	}
	if l.Payments, err = loadPayments(ctx, q, l); err != nil {
		return nil, 0, err
	}
	if l.Refunds, err = loadRefunds(ctx, q, l); err != nil {
		return nil, 0, err
	}
	if l.Deductions, err = loadDeductions(ctx, q, l); err != nil {
		return nil, 0, err
	}
	if l.Resolutions, err = loadResolutions(ctx, q, l); err != nil {
		return nil, 0, err
	}
	if err := loadEvents(ctx, q, l); err != nil {
		return nil, 0, err
	}
	return l, Version(v), nil
}

func loadCharges(ctx context.Context, q postgres.Querier, l *payments.Ledger) ([]payments.Charge, error) {
	rows, err := q.Query(ctx, `SELECT id, kind, amount, reason, evidence, created_by_kind, created_by_id, created_at,
		reversal_reason, reversed_by_kind, reversed_by_id, reversed_at, coalesce(assessment_ref, ''),
		approved_by_kind, approved_by_id, coalesce(delivery_leg, '')
		FROM ledger_charges WHERE booking_id = $1 ORDER BY position`, l.BookingRef)
	if err != nil {
		return nil, fmt.Errorf("ledger %s charges: %w", l.BookingRef, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (payments.Charge, error) {
		var c payments.Charge
		var amount int64
		var reversalReason *string
		var reversedKind, approvedKind *identity.ActorKind
		var reversedID, approvedID *string
		var reversedAt *time.Time
		if err := r.Scan(&c.ID, &c.Kind, &amount, &c.Reason, &c.Evidence, &c.CreatedBy.Kind, &c.CreatedBy.ID, &c.CreatedAt,
			&reversalReason, &reversedKind, &reversedID, &reversedAt, &c.AssessmentRef,
			&approvedKind, &approvedID, &c.DeliveryLeg); err != nil {
			return c, err
		}
		c.CreatedAt, c.Evidence = c.CreatedAt.UTC(), orNil(c.Evidence)
		c.ApprovedBy = actorOf(approvedKind, approvedID)
		if reversalReason != nil {
			c.Reversal = &payments.Reversal{Reason: *reversalReason, By: *actorOf(reversedKind, reversedID), At: reversedAt.UTC()}
		}
		var err error
		c.Amount, err = money.New(amount, l.Currency)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("ledger %s charges: %w", l.BookingRef, err)
	}
	return orNil(out), nil
}

func loadPayments(ctx context.Context, q postgres.Querier, l *payments.Ledger) ([]payments.Payment, error) {
	rows, err := q.Query(ctx, `SELECT id, idempotency_key, method, provider, coalesce(provider_txn_id, ''), amount, status,
		created_by_kind, created_by_id, created_at, settled_at, failure_reason, needs_review, coalesce(modification_ref, '')
		FROM payments WHERE booking_id = $1 ORDER BY created_at, id`, l.BookingRef)
	if err != nil {
		return nil, fmt.Errorf("ledger %s payments: %w", l.BookingRef, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (payments.Payment, error) {
		var p payments.Payment
		var amount int64
		if err := r.Scan(&p.ID, &p.IdempotencyKey, &p.Method, &p.Provider, &p.ProviderTxnID, &amount, &p.Status,
			&p.CreatedBy.Kind, &p.CreatedBy.ID, &p.CreatedAt, &p.SettledAt, &p.FailureReason, &p.NeedsReview, &p.ModificationRef); err != nil {
			return p, err
		}
		p.CreatedAt, p.SettledAt = p.CreatedAt.UTC(), postgres.UTCPtr(p.SettledAt)
		var err error
		p.Amount, err = money.New(amount, l.Currency)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("ledger %s payments: %w", l.BookingRef, err)
	}
	rows, err = q.Query(ctx, `SELECT pt.payment_id, pt.bucket, pt.amount FROM payment_portions pt
		JOIN payments p ON p.id = pt.payment_id WHERE p.booking_id = $1 ORDER BY pt.payment_id, pt.bucket`, l.BookingRef)
	if err != nil {
		return nil, fmt.Errorf("ledger %s portions: %w", l.BookingRef, err)
	}
	portions := map[payments.PaymentID][]payments.Portion{}
	var id payments.PaymentID
	var bucket payments.Bucket
	var amount int64
	_, err = pgx.ForEachRow(rows, []any{&id, &bucket, &amount}, func() error {
		m, err := money.New(amount, l.Currency)
		if err != nil {
			return err
		}
		portions[id] = append(portions[id], payments.Portion{Bucket: bucket, Amount: m})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ledger %s portions: %w", l.BookingRef, err)
	}
	for i := range out {
		out[i].Portions = portions[out[i].ID]
	}
	return orNil(out), nil
}

func loadRefunds(ctx context.Context, q postgres.Querier, l *payments.Ledger) ([]payments.Refund, error) {
	rows, err := q.Query(ctx, `SELECT id, idempotency_key, bucket, amount, reason, status, requested_by_kind, requested_by_id,
		requested_at, decided_by_kind, decided_by_id, decided_at, decision_note, provider, coalesce(provider_ref, ''),
		completed_at, failure_reason, coalesce(method, ''), reference, recorded_by_kind, recorded_by_id, recorded_at
		FROM refunds WHERE booking_id = $1 ORDER BY requested_at, id`, l.BookingRef)
	if err != nil {
		return nil, fmt.Errorf("ledger %s refunds: %w", l.BookingRef, err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (payments.Refund, error) {
		var r payments.Refund
		var amount int64
		var decidedKind, recordedKind *identity.ActorKind
		var decidedID, recordedID *string
		if err := row.Scan(&r.ID, &r.IdempotencyKey, &r.Bucket, &amount, &r.Reason, &r.Status, &r.RequestedBy.Kind,
			&r.RequestedBy.ID, &r.RequestedAt, &decidedKind, &decidedID, &r.DecidedAt, &r.DecisionNote, &r.Provider,
			&r.ProviderRef, &r.CompletedAt, &r.FailureReason, &r.Method, &r.Reference, &recordedKind, &recordedID,
			&r.RecordedAt); err != nil {
			return r, err
		}
		r.RequestedAt, r.DecidedAt, r.CompletedAt = r.RequestedAt.UTC(), postgres.UTCPtr(r.DecidedAt), postgres.UTCPtr(r.CompletedAt)
		r.RecordedAt = postgres.UTCPtr(r.RecordedAt)
		r.DecidedBy, r.RecordedBy = actorOf(decidedKind, decidedID), actorOf(recordedKind, recordedID)
		var err error
		r.Amount, err = money.New(amount, l.Currency)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("ledger %s refunds: %w", l.BookingRef, err)
	}
	return orNil(out), nil
}

func loadDeductions(ctx context.Context, q postgres.Querier, l *payments.Ledger) ([]payments.Deduction, error) {
	rows, err := q.Query(ctx, `SELECT id, charge_id, amount, reason, evidence, status, proposed_by_kind, proposed_by_id,
		proposed_at, decided_by_kind, decided_by_id, decided_at, decision_note, reversed_by_kind, reversed_by_id,
		reversed_at, reversal_reason FROM security_deductions WHERE booking_id = $1 ORDER BY proposed_at, id`, l.BookingRef)
	if err != nil {
		return nil, fmt.Errorf("ledger %s deductions: %w", l.BookingRef, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (payments.Deduction, error) {
		var d payments.Deduction
		var amount int64
		var decidedKind, reversedKind *identity.ActorKind
		var decidedID, reversedID *string
		if err := r.Scan(&d.ID, &d.ChargeID, &amount, &d.Reason, &d.Evidence, &d.Status, &d.ProposedBy.Kind, &d.ProposedBy.ID,
			&d.ProposedAt, &decidedKind, &decidedID, &d.DecidedAt, &d.DecisionNote, &reversedKind, &reversedID,
			&d.ReversedAt, &d.ReversalReason); err != nil {
			return d, err
		}
		d.ProposedAt, d.DecidedAt, d.Evidence = d.ProposedAt.UTC(), postgres.UTCPtr(d.DecidedAt), orNil(d.Evidence)
		d.ReversedAt = postgres.UTCPtr(d.ReversedAt)
		d.DecidedBy, d.ReversedBy = actorOf(decidedKind, decidedID), actorOf(reversedKind, reversedID)
		var err error
		d.Amount, err = money.New(amount, l.Currency)
		return d, err
	})
	if err != nil {
		return nil, fmt.Errorf("ledger %s deductions: %w", l.BookingRef, err)
	}
	return orNil(out), nil
}

func loadResolutions(ctx context.Context, q postgres.Querier, l *payments.Ledger) ([]payments.ReservationResolution, error) {
	rows, err := q.Query(ctx, `SELECT id, kind, amount, reason, created_by_kind, created_by_id, created_at
		FROM reservation_resolutions WHERE booking_id = $1 ORDER BY created_at, id`, l.BookingRef)
	if err != nil {
		return nil, fmt.Errorf("ledger %s resolutions: %w", l.BookingRef, err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (payments.ReservationResolution, error) {
		var r payments.ReservationResolution
		var amount int64
		if err := row.Scan(&r.ID, &r.Kind, &amount, &r.Reason, &r.CreatedBy.Kind, &r.CreatedBy.ID, &r.CreatedAt); err != nil {
			return r, err
		}
		r.CreatedAt = r.CreatedAt.UTC()
		var err error
		r.Amount, err = money.New(amount, l.Currency)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("ledger %s resolutions: %w", l.BookingRef, err)
	}
	return orNil(out), nil
}

func loadEvents(ctx context.Context, q postgres.Querier, l *payments.Ledger) error {
	rows, err := q.Query(ctx, `SELECT provider, event_id FROM provider_events WHERE booking_id = $1`, l.BookingRef)
	if err != nil {
		return fmt.Errorf("ledger %s events: %w", l.BookingRef, err)
	}
	var provider, id string
	_, err = pgx.ForEachRow(rows, []any{&provider, &id}, func() error {
		l.ProcessedEvents[provider+":"+id] = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("ledger %s events: %w", l.BookingRef, err)
	}
	return nil
}

func encode[T any](v *T) (*string, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	s := string(raw)
	return &s, nil
}

func actorColumns(a *identity.Actor) (*identity.ActorKind, *string) {
	if a == nil {
		return nil, nil
	}
	return &a.Kind, &a.ID
}

func actorOf(kind *identity.ActorKind, id *string) *identity.Actor {
	if kind == nil || id == nil {
		return nil
	}
	return &identity.Actor{Kind: *kind, ID: *id}
}

// orEmpty keeps NOT NULL array columns from receiving NULL for a nil slice.
func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// orNil makes an empty read equal to what the domain starts from.
func orNil[T any](v []T) []T {
	if len(v) == 0 {
		return nil
	}
	return v
}
