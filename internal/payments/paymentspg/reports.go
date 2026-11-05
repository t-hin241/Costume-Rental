package paymentspg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/platform/postgres"
)

// Totals sums the records of many ledgers in the database, per booking: the
// same sums payments.Ledger.Totals makes from one loaded ledger, so the
// balances come from payments.Totals.Summary either way. With no ids it
// covers every ledger.
func Totals(ctx context.Context, q postgres.Querier, ids []string) (map[string]payments.Totals, error) {
	out := map[string]payments.Totals{}
	get := func(id string) payments.Totals {
		t, ok := out[id]
		if !ok {
			t = payments.Totals{
				Required: map[payments.Bucket]int64{}, Received: map[payments.Bucket]int64{}, Pending: map[payments.Bucket]int64{},
				Refunded: map[payments.Bucket]int64{}, PendingRefund: map[payments.Bucket]int64{},
			}
		}
		return t
	}
	all := ids == nil
	rows, err := q.Query(ctx, `SELECT booking_id, credit_reservation_to_rent FROM payment_ledgers WHERE $1 OR booking_id = ANY($2)`, all, ids)
	if err != nil {
		return nil, fmt.Errorf("ledger totals: %w", err)
	}
	for rows.Next() {
		var id string
		var credit bool
		if err := rows.Scan(&id, &credit); err != nil {
			rows.Close()
			return nil, fmt.Errorf("ledger totals: %w", err)
		}
		t := get(id)
		t.CreditsRent = credit
		out[id] = t
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger totals: %w", err)
	}
	// Each query yields booking, bucket and amount; add puts it in place.
	sums := []struct {
		sql string
		add func(t *payments.Totals, b payments.Bucket, v int64)
	}{
		{`SELECT booking_id, CASE kind WHEN 'SECURITY_DEPOSIT' THEN 'SECURITY' WHEN 'RESERVATION_DEPOSIT' THEN 'RESERVATION' ELSE 'RENTAL' END, sum(amount)
			FROM ledger_charges WHERE reversed_at IS NULL AND ($1 OR booking_id = ANY($2)) GROUP BY 1, 2`,
			func(t *payments.Totals, b payments.Bucket, v int64) { t.Required[b] += v }},
		{`SELECT p.booking_id, pt.bucket, sum(pt.amount) FROM payments p JOIN payment_portions pt ON pt.payment_id = p.id
			WHERE p.status = 'SUCCEEDED' AND ($1 OR p.booking_id = ANY($2)) GROUP BY 1, 2`,
			func(t *payments.Totals, b payments.Bucket, v int64) { t.Received[b] += v }},
		{`SELECT p.booking_id, pt.bucket, sum(pt.amount) FROM payments p JOIN payment_portions pt ON pt.payment_id = p.id
			WHERE p.status = 'PENDING' AND ($1 OR p.booking_id = ANY($2)) GROUP BY 1, 2`,
			func(t *payments.Totals, b payments.Bucket, v int64) { t.Pending[b] += v }},
		{`SELECT booking_id, bucket, sum(amount) FROM refunds WHERE status = 'SUCCEEDED' AND ($1 OR booking_id = ANY($2)) GROUP BY 1, 2`,
			func(t *payments.Totals, b payments.Bucket, v int64) { t.Refunded[b] += v }},
		{`SELECT booking_id, bucket, sum(amount) FROM refunds WHERE status IN ('REQUESTED', 'APPROVED', 'PROCESSING')
			AND ($1 OR booking_id = ANY($2)) GROUP BY 1, 2`,
			func(t *payments.Totals, b payments.Bucket, v int64) { t.PendingRefund[b] += v }},
		{`SELECT booking_id, 'SECURITY', sum(amount) FROM security_deductions WHERE status = 'APPROVED' AND ($1 OR booking_id = ANY($2)) GROUP BY 1`,
			func(t *payments.Totals, _ payments.Bucket, v int64) { t.SecurityApplied += v }},
		{`SELECT booking_id, 'SECURITY', sum(amount) FROM security_deductions WHERE status = 'PROPOSED' AND ($1 OR booking_id = ANY($2)) GROUP BY 1`,
			func(t *payments.Totals, _ payments.Bucket, v int64) { t.PendingDeductions += v }},
		{`SELECT booking_id, 'RESERVATION', sum(amount) FROM reservation_resolutions WHERE kind = 'APPLIED_TO_RENTAL'
			AND ($1 OR booking_id = ANY($2)) GROUP BY 1`,
			func(t *payments.Totals, _ payments.Bucket, v int64) { t.ReservationApplied += v }},
	}
	for _, s := range sums {
		rows, err := q.Query(ctx, s.sql, all, ids)
		if err != nil {
			return nil, fmt.Errorf("ledger totals: %w", err)
		}
		for rows.Next() {
			var id string
			var b payments.Bucket
			var v int64
			if err := rows.Scan(&id, &b, &v); err != nil {
				rows.Close()
				return nil, fmt.Errorf("ledger totals: %w", err)
			}
			t := get(id)
			s.add(&t, b, v)
			out[id] = t
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("ledger totals: %w", err)
		}
	}
	return out, nil
}

// Movement is money that moved in a period, by method and bucket.
type Movement struct {
	Method payments.Method
	Bucket payments.Bucket
	Count  int
	Amount money.Money
}

// CashMovements sums the payments that succeeded and the refunds paid out in
// [from, to), by method and bucket. A gateway refund has no counter method
// and counts as GATEWAY.
func CashMovements(ctx context.Context, q postgres.Querier, from, to time.Time) (received, refunded []Movement, err error) {
	scan := func(sql string) ([]Movement, error) {
		rows, err := q.Query(ctx, sql, from.UTC(), to.UTC())
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Movement, error) {
			var m Movement
			var v int64
			if err := r.Scan(&m.Method, &m.Bucket, &m.Count, &v); err != nil {
				return Movement{}, err
			}
			m.Amount, err = money.New(v, money.VND)
			return m, err
		})
	}
	if received, err = scan(`SELECT p.method, pt.bucket, count(DISTINCT p.id), sum(pt.amount)
		FROM payments p JOIN payment_portions pt ON pt.payment_id = p.id
		WHERE p.status = 'SUCCEEDED' AND p.settled_at >= $1 AND p.settled_at < $2 GROUP BY 1, 2 ORDER BY 1, 2`); err != nil {
		return nil, nil, fmt.Errorf("money received: %w", err)
	}
	if refunded, err = scan(`SELECT coalesce(method, 'GATEWAY'), bucket, count(*), sum(amount) FROM refunds
		WHERE status = 'SUCCEEDED' AND completed_at >= $1 AND completed_at < $2 GROUP BY 1, 2 ORDER BY 1, 2`); err != nil {
		return nil, nil, fmt.Errorf("money refunded: %w", err)
	}
	return received, refunded, nil
}

// TxnFilter selects succeeded payments and refunds by when the money moved.
type TxnFilter struct {
	From, To time.Time
	// Kind is empty for both.
	Kind payments.TxnKind
	// Reconciled: nil for all, true or false for only those.
	Reconciled *bool
}

// Txn is a succeeded payment or refund with its reconciliation in force.
type Txn struct {
	Kind           payments.TxnKind
	ID             string
	BookingRef     string
	BookingCode    string
	Method         payments.Method
	Reference      string
	Amount         money.Money
	Buckets        []payments.Bucket
	At             time.Time
	Reconciliation *payments.Reconciliation
}

const txns = `
	SELECT 'PAYMENT' AS kind, p.id, p.booking_id, p.method, coalesce(p.provider_txn_id, '') AS reference, p.amount,
		(SELECT array_agg(pt.bucket ORDER BY pt.bucket) FROM payment_portions pt WHERE pt.payment_id = p.id) AS buckets,
		p.settled_at AS at
	FROM payments p WHERE p.status = 'SUCCEEDED' AND p.settled_at >= $1 AND p.settled_at < $2
	UNION ALL
	SELECT 'REFUND', r.id, r.booking_id, coalesce(r.method, 'GATEWAY'), coalesce(nullif(r.reference, ''), r.provider_ref, ''), r.amount,
		ARRAY[r.bucket], r.completed_at
	FROM refunds r WHERE r.status = 'SUCCEEDED' AND r.completed_at >= $1 AND r.completed_at < $2`

const reconColumns = `c.id, c.statement_ref, c.note, c.reconciled_by_kind, c.reconciled_by_id, c.reconciled_at`

// Txns lists succeeded payments and refunds, newest first, with the total.
func Txns(ctx context.Context, q postgres.Querier, f TxnFilter, page, size int) ([]Txn, int, error) {
	rows, err := q.Query(ctx, `
		SELECT t.kind, t.id, t.booking_id, b.code, t.method, t.reference, t.amount, t.buckets, t.at,
			`+reconColumns+`, count(*) OVER ()
		FROM (`+txns+`) t
		JOIN bookings b ON b.id = t.booking_id
		LEFT JOIN transaction_reconciliations c ON c.kind = t.kind AND c.transaction_id = t.id AND c.revoked_at IS NULL
		WHERE ($3 = '' OR t.kind = $3) AND ($4::boolean IS NULL OR (c.id IS NOT NULL) = $4)
		ORDER BY t.at DESC, t.id LIMIT $5 OFFSET $6`,
		f.From.UTC(), f.To.UTC(), string(f.Kind), f.Reconciled, size, (page-1)*size)
	if err != nil {
		return nil, 0, fmt.Errorf("transactions: %w", err)
	}
	defer rows.Close()
	var out []Txn
	total := 0
	for rows.Next() {
		var t Txn
		var amount int64
		var rid, ref, note, byID *string
		var byKind *identity.ActorKind
		var rat *time.Time
		if err := rows.Scan(&t.Kind, &t.ID, &t.BookingRef, &t.BookingCode, &t.Method, &t.Reference, &amount, &t.Buckets, &t.At,
			&rid, &ref, &note, &byKind, &byID, &rat, &total); err != nil {
			return nil, 0, fmt.Errorf("transactions: %w", err)
		}
		if t.Amount, err = money.New(amount, money.VND); err != nil {
			return nil, 0, err
		}
		t.At = t.At.UTC()
		if rid != nil {
			t.Reconciliation = &payments.Reconciliation{ID: *rid, Kind: t.Kind, TxnID: t.ID, BookingRef: t.BookingRef,
				StatementRef: *ref, Note: *note, By: *actorOf(byKind, byID), At: rat.UTC()}
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("transactions: %w", err)
	}
	return out, total, nil
}

// TxnBooking returns the booking a payment or refund belongs to.
func TxnBooking(ctx context.Context, q postgres.Querier, kind payments.TxnKind, id string) (string, error) {
	var table string
	switch kind {
	case payments.TxnPayment:
		table = "payments"
	case payments.TxnRefund:
		table = "refunds"
	default:
		return "", domainerr.Invalid("kind", "must be PAYMENT or REFUND")
	}
	var booking string
	if err := q.QueryRow(ctx, `SELECT booking_id FROM `+table+` WHERE id = $1`, id).Scan(&booking); err != nil {
		return "", postgres.NotFound(err, fmt.Sprintf("%s %s", kind, id))
	}
	return booking, nil
}

// ReconciliationInForce reports whether a transaction has a reconciliation
// that is not revoked.
func ReconciliationInForce(ctx context.Context, q postgres.Querier, kind payments.TxnKind, id string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM transaction_reconciliations
		WHERE kind = $1 AND transaction_id = $2 AND revoked_at IS NULL)`, kind, id).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("reconciliation of %s %s: %w", kind, id, err)
	}
	return ok, nil
}

// InsertReconciliation stores a reconciliation. A concurrent one for the same
// transaction is reported as already reconciled.
func InsertReconciliation(ctx context.Context, q postgres.Querier, r payments.Reconciliation) error {
	_, err := q.Exec(ctx, `INSERT INTO transaction_reconciliations (id, kind, transaction_id, booking_id, statement_ref, note,
		reconciled_by_kind, reconciled_by_id, reconciled_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		r.ID, r.Kind, r.TxnID, r.BookingRef, r.StatementRef, r.Note, r.By.Kind, r.By.ID, r.At.UTC())
	switch {
	case postgres.IsUniqueViolation(err, "transaction_reconciliations_in_force"):
		return fmt.Errorf("%s %s: %w", r.Kind, r.TxnID, payments.ErrAlreadyReconciled)
	case err != nil:
		return fmt.Errorf("insert reconciliation of %s %s: %w", r.Kind, r.TxnID, err)
	}
	return nil
}

// LoadReconciliation reads a reconciliation FOR UPDATE.
func LoadReconciliation(ctx context.Context, q postgres.Querier, id string) (payments.Reconciliation, error) {
	var r payments.Reconciliation
	var revReason, revByID *string
	var revByKind *identity.ActorKind
	var revAt *time.Time
	err := q.QueryRow(ctx, `SELECT c.kind, c.transaction_id, c.booking_id, `+reconColumns+`,
			c.revoke_reason, c.revoked_by_kind, c.revoked_by_id, c.revoked_at
		FROM transaction_reconciliations c WHERE c.id = $1 FOR UPDATE`, id).
		Scan(&r.Kind, &r.TxnID, &r.BookingRef, &r.ID, &r.StatementRef, &r.Note, &r.By.Kind, &r.By.ID, &r.At,
			&revReason, &revByKind, &revByID, &revAt)
	if err != nil {
		return payments.Reconciliation{}, postgres.NotFound(err, "reconciliation "+id)
	}
	r.At = r.At.UTC()
	if revAt != nil {
		r.Revoked = &payments.Revocation{Reason: *revReason, By: *actorOf(revByKind, revByID), At: revAt.UTC()}
	}
	return r, nil
}

// RevokeReconciliation stores the revocation of a reconciliation.
func RevokeReconciliation(ctx context.Context, q postgres.Querier, r payments.Reconciliation) error {
	if r.Revoked == nil {
		return fmt.Errorf("reconciliation %s is not revoked", r.ID)
	}
	_, err := q.Exec(ctx, `UPDATE transaction_reconciliations SET revoke_reason = $2, revoked_by_kind = $3, revoked_by_id = $4,
		revoked_at = $5 WHERE id = $1 AND revoked_at IS NULL`, r.ID, r.Revoked.Reason, r.Revoked.By.Kind, r.Revoked.By.ID, r.Revoked.At.UTC())
	if err != nil {
		return fmt.Errorf("revoke reconciliation %s: %w", r.ID, err)
	}
	return nil
}

// Attention is a payment or refund finance should look at.
type Attention struct {
	Kind        payments.TxnKind
	ID          string
	BookingRef  string
	BookingCode string
	Amount      money.Money
	Status      string
	Reason      string
	At          time.Time
}

// NeedingAttention lists, newest first, succeeded payments flagged for review
// (late success, possible overpayment) and refunds that failed.
func NeedingAttention(ctx context.Context, q postgres.Querier, limit int) ([]Attention, int, error) {
	rows, err := q.Query(ctx, `
		SELECT a.*, count(*) OVER () FROM (
			SELECT 'PAYMENT' AS kind, p.id, p.booking_id, b.code, p.amount, p.status, p.failure_reason, coalesce(p.settled_at, p.created_at) AS at
			FROM payments p JOIN bookings b ON b.id = p.booking_id WHERE p.needs_review
			UNION ALL
			SELECT 'REFUND', r.id, r.booking_id, b.code, r.amount, r.status, r.failure_reason, coalesce(r.completed_at, r.requested_at)
			FROM refunds r JOIN bookings b ON b.id = r.booking_id WHERE r.status = 'FAILED') a
		ORDER BY a.at DESC, a.id LIMIT $1`, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("transactions needing attention: %w", err)
	}
	defer rows.Close()
	var out []Attention
	total := 0
	for rows.Next() {
		var a Attention
		var amount int64
		if err := rows.Scan(&a.Kind, &a.ID, &a.BookingRef, &a.BookingCode, &amount, &a.Status, &a.Reason, &a.At, &total); err != nil {
			return nil, 0, fmt.Errorf("transactions needing attention: %w", err)
		}
		if a.Amount, err = money.New(amount, money.VND); err != nil {
			return nil, 0, err
		}
		a.At = a.At.UTC()
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("transactions needing attention: %w", err)
	}
	return out, total, nil
}
