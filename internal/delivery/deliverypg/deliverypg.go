// Package deliverypg stores delivery tasks (one shipment per leg) with their
// failed attempts and the append-only event log in PostgreSQL.
//
// A task is written as a whole. Every write names the version it read, so a
// concurrent change is reported as postgres.ErrStale instead of being
// overwritten. Addresses are personal data: they are stored as JSONB on the
// task only and never logged.
package deliverypg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/delivery"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/period"
	"webee_rentor/internal/platform/postgres"
)

// Version is the optimistic concurrency token of a stored task; zero means
// not stored yet.
type Version int

// Unique indexes a write may hit.
const (
	openLegIndex  = "delivery_tasks_one_open_leg"
	followUpIndex = "delivery_tasks_one_follow_up"
	shipmentIndex = "delivery_tasks_shipment"
)

// ErrLegInProgress means the leg already has a shipment in progress.
var ErrLegInProgress = fmt.Errorf("the leg already has a shipment in progress: %w", postgres.ErrDuplicate)

const taskColumns = `id, booking_id, customer_id, direction, status, progress, status_before_cancel, provider_code,
	client_reference, provider_shipment_code, service_code, from_address, to_address, window_from, window_until,
	expected_at, picked_up_at, delivered_at, received_at, parcel, currency, customer_fee, carrier_fee_estimated,
	carrier_fee_final, failure_code, failure_reason, created_at, gave_up_at, gave_up_attempt, follow_up,
	closed_by_kind, closed_by_id, closed_reason, replaces, replaced_by, version`

// SaveTask inserts (v == 0) or updates the task with its attempts and new
// events, and returns the new version.
func SaveTask(ctx context.Context, q postgres.Querier, t *delivery.Task, v Version, now time.Time) (Version, error) {
	from, err := json.Marshal(t.From)
	if err != nil {
		return 0, fmt.Errorf("encode task %s address: %w", t.ID, err)
	}
	to, err := json.Marshal(t.To)
	if err != nil {
		return 0, fmt.Errorf("encode task %s address: %w", t.ID, err)
	}
	parcel, err := json.Marshal(t.Parcel)
	if err != nil {
		return 0, fmt.Errorf("encode task %s parcel: %w", t.ID, err)
	}
	var closedKind, closedID *string
	if t.ClosedBy != nil {
		closedKind, closedID = ptr(string(t.ClosedBy.Kind)), &t.ClosedBy.ID
	}
	args := []any{t.ID, t.BookingRef, t.CustomerID, t.Direction, t.Status, nonEmpty(string(t.Progress)), nonEmpty(string(t.StatusBeforeCancel)),
		t.ProviderCode, t.ClientReference, nonEmpty(t.ProviderShipmentCode), t.ServiceCode, from, to, t.Window.Start(), t.Window.End(),
		t.ExpectedAt, t.PickedUpAt, t.DeliveredAt, t.ReceivedAt, parcel, t.CustomerFee.Currency(), t.CustomerFee.Amount(),
		amountOf(t.CarrierFeeEstimated), amountOf(t.CarrierFeeFinal), t.FailureCode, t.FailureReason, t.CreatedAt,
		t.GaveUpAt, t.GaveUpAttempt, string(t.FollowUp), closedKind, closedID, t.ClosedReason,
		nonEmpty(string(t.Replaces)), nonEmpty(string(t.ReplacedBy)), now.UTC()}
	if v == 0 {
		_, err = q.Exec(ctx, `
			INSERT INTO delivery_tasks (id, booking_id, customer_id, direction, status, progress, status_before_cancel, provider_code,
				client_reference, provider_shipment_code, service_code, from_address, to_address, window_from, window_until,
				expected_at, picked_up_at, delivered_at, received_at, parcel, currency, customer_fee, carrier_fee_estimated,
				carrier_fee_final, failure_code, failure_reason, created_at, gave_up_at, gave_up_attempt, follow_up,
				closed_by_kind, closed_by_id, closed_reason, replaces, replaced_by, updated_at, version)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23,
				$24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36, 1)`, args...)
	} else {
		var tag interface{ RowsAffected() int64 }
		tag, err = q.Exec(ctx, `
			UPDATE delivery_tasks SET status = $5, progress = $6, status_before_cancel = $7,
				provider_shipment_code = $10, service_code = $11, from_address = $12, to_address = $13,
				window_from = $14, window_until = $15, expected_at = $16, picked_up_at = $17, delivered_at = $18,
				received_at = $19, parcel = $20, customer_fee = $22, carrier_fee_estimated = $23, carrier_fee_final = $24,
				failure_code = $25, failure_reason = $26, gave_up_at = $28, gave_up_attempt = $29, follow_up = $30,
				closed_by_kind = $31, closed_by_id = $32, closed_reason = $33, replaces = $34, replaced_by = $35,
				updated_at = $36, version = version + 1
			WHERE id = $1 AND version = $37 AND booking_id = $2 AND customer_id = $3 AND direction = $4
				AND provider_code = $8 AND client_reference = $9 AND currency = $21 AND created_at = $27`,
			append(args, int(v))...)
		if err == nil && tag.RowsAffected() == 0 {
			return 0, fmt.Errorf("delivery task %s version %d: %w", t.ID, v, postgres.ErrStale)
		}
	}
	switch {
	case postgres.IsUniqueViolation(err, openLegIndex):
		return 0, fmt.Errorf("booking %s %s: %w", t.BookingRef, t.Direction, ErrLegInProgress)
	case postgres.IsUniqueViolation(err, followUpIndex):
		return 0, fmt.Errorf("task %s follow-up: %w", t.Replaces, postgres.ErrDuplicate)
	case postgres.IsUniqueViolation(err, shipmentIndex):
		return 0, fmt.Errorf("shipment %s is already recorded on another task: %w", t.ProviderShipmentCode, postgres.ErrDuplicate)
	case err != nil:
		return 0, fmt.Errorf("save delivery task %s: %w", t.ID, err)
	}
	for _, a := range t.Attempts {
		var kind, id *string
		if a.ClassifiedBy != nil {
			kind, id = ptr(string(a.ClassifiedBy.Kind)), &a.ClassifiedBy.ID
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO delivery_attempts (task_id, id, status, at, fault, reason, evidence, classified_by_kind, classified_by_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (task_id, id) DO UPDATE SET fault = EXCLUDED.fault, reason = EXCLUDED.reason,
				classified_by_kind = EXCLUDED.classified_by_kind, classified_by_id = EXCLUDED.classified_by_id`,
			t.ID, a.ID, a.Status, a.At, a.Fault, a.Reason, a.Evidence, kind, id); err != nil {
			return 0, fmt.Errorf("save attempt %s of task %s: %w", a.ID, t.ID, err)
		}
	}
	for i, e := range t.Events {
		var kind, id *string
		if e.Actor != nil {
			kind, id = ptr(string(e.Actor.Kind)), &e.Actor.ID
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO delivery_events (task_id, key, position, source, status, provider_status, occurred_at, received_at,
				actor_kind, actor_id, reason, evidence, applied)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (task_id, key) DO NOTHING`,
			t.ID, e.Key, i, e.Source, e.Status, e.ProviderStatus, e.OccurredAt, e.ReceivedAt, kind, id, e.Reason, e.Evidence, e.Applied); err != nil {
			return 0, fmt.Errorf("save event %s of task %s: %w", e.Key, t.ID, err)
		}
	}
	return v + 1, nil
}

// LoadTask reads a task, locking its row when lock is set.
func LoadTask(ctx context.Context, q postgres.Querier, id delivery.TaskID, lock bool) (*delivery.Task, Version, error) {
	sql := `SELECT ` + taskColumns + ` FROM delivery_tasks WHERE id = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	t, v, err := scanTask(q.QueryRow(ctx, sql, id))
	if err != nil {
		return nil, 0, postgres.NotFound(err, "delivery task "+string(id))
	}
	if err := loadHistory(ctx, q, []*delivery.Task{t}); err != nil {
		return nil, 0, err
	}
	return t, v, nil
}

// Stored is a task with the version it was read at.
type Stored struct {
	Task    *delivery.Task
	Version Version
}

// TasksOfBooking reads every task of a booking, oldest first, locking them
// when lock is set (callers hold the booking lock first).
func TasksOfBooking(ctx context.Context, q postgres.Querier, booking string, lock bool) ([]Stored, error) {
	sql := `SELECT ` + taskColumns + ` FROM delivery_tasks WHERE booking_id = $1 ORDER BY created_at, id`
	if lock {
		sql += ` FOR UPDATE`
	}
	return queryTasks(ctx, q, sql, booking)
}

// Filter selects tasks for the staff board.
type Filter struct {
	// Open keeps tasks still needing work: not delivered, received,
	// cancelled, lost or closed.
	Open bool
	// WindowBefore keeps tasks whose window starts before it.
	WindowBefore *time.Time
	Limit        int
	Offset       int
}

// ListTasks reads tasks by window start, with the total before paging.
func ListTasks(ctx context.Context, q postgres.Querier, f Filter) ([]*delivery.Task, int, error) {
	var where []string
	var args []any
	if f.Open {
		where = append(where, `status NOT IN ('DELIVERED', 'RECEIVED_AT_STORE', 'CANCELLED', 'LOST', 'CLOSED_UNFULFILLED')`)
	}
	if f.WindowBefore != nil {
		args = append(args, f.WindowBefore.UTC())
		where = append(where, fmt.Sprintf("window_from < $%d", len(args)))
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM delivery_tasks`+cond, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count delivery tasks: %w", err)
	}
	args = append(args, f.Limit, f.Offset)
	stored, err := queryTasks(ctx, q, `SELECT `+taskColumns+` FROM delivery_tasks`+cond+
		fmt.Sprintf(` ORDER BY window_from, id LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	out := make([]*delivery.Task, len(stored))
	for i, s := range stored {
		out[i] = s.Task
	}
	return out, total, nil
}

func queryTasks(ctx context.Context, q postgres.Querier, sql string, args ...any) ([]Stored, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query delivery tasks: %w", err)
	}
	var out []Stored
	for rows.Next() {
		t, v, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, Stored{Task: t, Version: v})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read delivery tasks: %w", err)
	}
	tasks := make([]*delivery.Task, len(out))
	for i, s := range out {
		tasks[i] = s.Task
	}
	if err := loadHistory(ctx, q, tasks); err != nil {
		return nil, err
	}
	return out, nil
}

func scanTask(row pgx.Row) (*delivery.Task, Version, error) {
	var t delivery.Task
	var progress, beforeCancel, shipment, closedKind, closedID, replaces, replacedBy *string
	var from, to, parcel []byte
	var start, end time.Time
	var currency money.Currency
	var fee int64
	var estimated, final *int64
	var followUp string
	var v int
	err := row.Scan(&t.ID, &t.BookingRef, &t.CustomerID, &t.Direction, &t.Status, &progress, &beforeCancel, &t.ProviderCode,
		&t.ClientReference, &shipment, &t.ServiceCode, &from, &to, &start, &end,
		&t.ExpectedAt, &t.PickedUpAt, &t.DeliveredAt, &t.ReceivedAt, &parcel, &currency, &fee, &estimated,
		&final, &t.FailureCode, &t.FailureReason, &t.CreatedAt, &t.GaveUpAt, &t.GaveUpAttempt, &followUp,
		&closedKind, &closedID, &t.ClosedReason, &replaces, &replacedBy, &v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, err
	}
	if err != nil {
		return nil, 0, fmt.Errorf("scan delivery task: %w", err)
	}
	t.Progress, t.StatusBeforeCancel = delivery.Status(deref(progress)), delivery.Status(deref(beforeCancel))
	t.ProviderShipmentCode, t.FollowUp = deref(shipment), delivery.FollowUp(followUp)
	t.Replaces, t.ReplacedBy = delivery.TaskID(deref(replaces)), delivery.TaskID(deref(replacedBy))
	if closedKind != nil && closedID != nil {
		t.ClosedBy = &identity.Actor{Kind: identity.ActorKind(*closedKind), ID: *closedID}
	}
	if err := json.Unmarshal(from, &t.From); err != nil {
		return nil, 0, fmt.Errorf("decode task %s address: %w", t.ID, err)
	}
	if err := json.Unmarshal(to, &t.To); err != nil {
		return nil, 0, fmt.Errorf("decode task %s address: %w", t.ID, err)
	}
	if err := json.Unmarshal(parcel, &t.Parcel); err != nil {
		return nil, 0, fmt.Errorf("decode task %s parcel: %w", t.ID, err)
	}
	if t.Window, err = period.New(start, end); err != nil {
		return nil, 0, fmt.Errorf("task %s window: %w", t.ID, err)
	}
	if t.CustomerFee, err = money.New(fee, currency); err != nil {
		return nil, 0, fmt.Errorf("task %s fee: %w", t.ID, err)
	}
	if t.CarrierFeeEstimated, err = moneyPtr(estimated, currency); err != nil {
		return nil, 0, err
	}
	if t.CarrierFeeFinal, err = moneyPtr(final, currency); err != nil {
		return nil, 0, err
	}
	for _, p := range []**time.Time{&t.ExpectedAt, &t.PickedUpAt, &t.DeliveredAt, &t.ReceivedAt, &t.GaveUpAt} {
		if *p != nil {
			u := (*p).UTC()
			*p = &u
		}
	}
	t.CreatedAt = t.CreatedAt.UTC()
	return &t, Version(v), nil
}

// loadHistory fills the attempts and events of the tasks.
func loadHistory(ctx context.Context, q postgres.Querier, tasks []*delivery.Task) error {
	if len(tasks) == 0 {
		return nil
	}
	byID := make(map[delivery.TaskID]*delivery.Task, len(tasks))
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		byID[t.ID], ids[i] = t, string(t.ID)
	}
	rows, err := q.Query(ctx, `
		SELECT task_id, id, status, at, fault, reason, evidence, classified_by_kind, classified_by_id
		FROM delivery_attempts WHERE task_id = ANY($1) ORDER BY task_id, at, id`, ids)
	if err != nil {
		return fmt.Errorf("query delivery attempts: %w", err)
	}
	for rows.Next() {
		var a delivery.Attempt
		var kind, id *string
		if err := rows.Scan(&a.TaskID, &a.ID, &a.Status, &a.At, &a.Fault, &a.Reason, &a.Evidence, &kind, &id); err != nil {
			rows.Close()
			return fmt.Errorf("scan delivery attempt: %w", err)
		}
		a.At = a.At.UTC()
		if kind != nil && id != nil {
			a.ClassifiedBy = &identity.Actor{Kind: identity.ActorKind(*kind), ID: *id}
		}
		byID[a.TaskID].Attempts = append(byID[a.TaskID].Attempts, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read delivery attempts: %w", err)
	}
	rows, err = q.Query(ctx, `
		SELECT task_id, key, source, status, provider_status, occurred_at, received_at, actor_kind, actor_id, reason, evidence, applied
		FROM delivery_events WHERE task_id = ANY($1) ORDER BY task_id, position`, ids)
	if err != nil {
		return fmt.Errorf("query delivery events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var task delivery.TaskID
		var e delivery.Event
		var kind, id *string
		if err := rows.Scan(&task, &e.Key, &e.Source, &e.Status, &e.ProviderStatus, &e.OccurredAt, &e.ReceivedAt, &kind, &id, &e.Reason, &e.Evidence, &e.Applied); err != nil {
			return fmt.Errorf("scan delivery event: %w", err)
		}
		e.OccurredAt, e.ReceivedAt = e.OccurredAt.UTC(), e.ReceivedAt.UTC()
		if kind != nil && id != nil {
			e.Actor = &identity.Actor{Kind: identity.ActorKind(*kind), ID: *id}
		}
		byID[task].Events = append(byID[task].Events, e)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read delivery events: %w", err)
	}
	return nil
}

func ptr(s string) *string { return &s }

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func amountOf(m *money.Money) *int64 {
	if m == nil {
		return nil
	}
	a := m.Amount()
	return &a
}

func moneyPtr(v *int64, c money.Currency) (*money.Money, error) {
	if v == nil {
		return nil, nil
	}
	m, err := money.New(*v, c)
	if err != nil {
		return nil, fmt.Errorf("carrier fee: %w", err)
	}
	return &m, nil
}
