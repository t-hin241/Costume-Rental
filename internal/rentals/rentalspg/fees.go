package rentalspg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/money"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
)

const oneLateFeeIndex = "fee_assessments_one_late"

const assessmentColumns = `id, booking_id, asset_id, kind, currency, amount, basis, evidence, policy_version, status,
	proposed_by_kind, proposed_by_id, proposed_at, decided_by_kind, decided_by_id, decided_at, decision_note,
	dispute_note, ref, coalesce(supersedes, ''), coalesce(superseded_by, '')`

// InsertAssessment writes a new fee assessment.
func InsertAssessment(ctx context.Context, q postgres.Querier, a rentals.FeeAssessment) error {
	decidedKind, decidedID := decidedColumns(a.DecidedBy)
	evidence := a.Evidence
	if evidence == nil {
		evidence = []string{}
	}
	_, err := q.Exec(ctx, `
		INSERT INTO fee_assessments (id, booking_id, asset_id, kind, currency, amount, basis, evidence, policy_version,
			status, proposed_by_kind, proposed_by_id, proposed_at, decided_by_kind, decided_by_id, decided_at,
			decision_note, dispute_note, ref, supersedes, superseded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19,
			nullif($20, ''), nullif($21, ''))`,
		a.ID, a.BookingID, a.AssetID, a.Kind, a.Amount.Currency(), a.Amount.Amount(), a.Basis, evidence, a.PolicyVersion,
		a.Status, a.ProposedBy.Kind, a.ProposedBy.ID, a.ProposedAt, decidedKind, decidedID, a.DecidedAt,
		a.DecisionNote, a.DisputeNote, a.Ref, a.Supersedes, a.SupersededBy)
	if postgres.IsUniqueViolation(err, oneLateFeeIndex) {
		return fmt.Errorf("asset %s late fee: %w", a.AssetID, rentals.ErrDuplicateFee)
	}
	if err != nil {
		return fmt.Errorf("insert fee assessment %s: %w", a.ID, err)
	}
	return nil
}

// UpdateAssessment writes a decision. The caller holds the booking lock.
func UpdateAssessment(ctx context.Context, q postgres.Querier, a rentals.FeeAssessment) error {
	decidedKind, decidedID := decidedColumns(a.DecidedBy)
	tag, err := q.Exec(ctx, `
		UPDATE fee_assessments SET status = $2, decided_by_kind = $3, decided_by_id = $4, decided_at = $5,
			decision_note = $6, dispute_note = $7, superseded_by = nullif($8, ''), version = version + 1
		WHERE id = $1`,
		a.ID, a.Status, decidedKind, decidedID, a.DecidedAt, a.DecisionNote, a.DisputeNote, a.SupersededBy)
	if err != nil {
		return fmt.Errorf("update fee assessment %s: %w", a.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("fee assessment %s: %w", a.ID, postgres.ErrNotFound)
	}
	return nil
}

// LoadAssessment reads one assessment.
func LoadAssessment(ctx context.Context, q postgres.Querier, id rentals.AssessmentID) (rentals.FeeAssessment, error) {
	rows, err := q.Query(ctx, `SELECT `+assessmentColumns+` FROM fee_assessments WHERE id = $1`, id)
	if err != nil {
		return rentals.FeeAssessment{}, fmt.Errorf("fee assessment %s: %w", id, err)
	}
	a, err := pgx.CollectExactlyOneRow(rows, scanAssessment)
	if err != nil {
		return rentals.FeeAssessment{}, postgres.NotFound(err, fmt.Sprintf("fee assessment %s", id))
	}
	return a, nil
}

// AssessmentsOfBooking lists a booking's fee assessments, oldest first.
func AssessmentsOfBooking(ctx context.Context, q postgres.Querier, id rentals.BookingID) ([]rentals.FeeAssessment, error) {
	rows, err := q.Query(ctx, `SELECT `+assessmentColumns+` FROM fee_assessments WHERE booking_id = $1 ORDER BY proposed_at, id`, id)
	if err != nil {
		return nil, fmt.Errorf("booking %s fee assessments: %w", id, err)
	}
	out, err := pgx.CollectRows(rows, scanAssessment)
	if err != nil {
		return nil, fmt.Errorf("booking %s fee assessments: %w", id, err)
	}
	return out, nil
}

func scanAssessment(r pgx.CollectableRow) (rentals.FeeAssessment, error) {
	var a rentals.FeeAssessment
	var asset string
	var currency money.Currency
	var amount int64
	var decidedKind *identity.ActorKind
	var decidedID *string
	if err := r.Scan(&a.ID, &a.BookingID, &asset, &a.Kind, &currency, &amount, &a.Basis, &a.Evidence, &a.PolicyVersion, &a.Status,
		&a.ProposedBy.Kind, &a.ProposedBy.ID, &a.ProposedAt, &decidedKind, &decidedID, &a.DecidedAt, &a.DecisionNote,
		&a.DisputeNote, &a.Ref, &a.Supersedes, &a.SupersededBy); err != nil {
		return a, err
	}
	a.AssetID = inventory.AssetID(asset)
	a.ProposedAt, a.DecidedAt = a.ProposedAt.UTC(), postgres.UTCPtr(a.DecidedAt)
	if len(a.Evidence) == 0 {
		a.Evidence = nil
	}
	if decidedKind != nil && decidedID != nil {
		a.DecidedBy = &identity.Actor{Kind: *decidedKind, ID: *decidedID}
	}
	var err error
	a.Amount, err = money.New(amount, currency)
	return a, err
}

func decidedColumns(a *identity.Actor) (*identity.ActorKind, *string) {
	if a == nil {
		return nil, nil
	}
	return &a.Kind, &a.ID
}

// RentedDueBy lists rented bookings due at or before now, oldest first. The
// caller decides lateness with Booking.IsOverdue, which knows the grace
// period and which items are still out.
func RentedDueBy(ctx context.Context, q postgres.Querier, now time.Time, limit int) ([]rentals.BookingID, error) {
	rows, err := q.Query(ctx, `SELECT id FROM bookings WHERE status = 'RENTED' AND due_at <= $1
		ORDER BY due_at, id LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("rented bookings due: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[rentals.BookingID])
	if err != nil {
		return nil, fmt.Errorf("rented bookings due: %w", err)
	}
	return ids, nil
}
