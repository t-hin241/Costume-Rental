// Package rentalsapp runs the rental use cases against PostgreSQL. Each use
// case authorises the principal, loads state under lock, applies the domain
// rules and persists the booking, its allocations, history and audit in one
// transaction, so a failure leaves nothing half-written.
//
// Lock order, followed everywhere to avoid deadlocks: booking row, then its
// modification row, then asset rows in id order (inventorypg).
package rentalsapp

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/catalog"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/inventory/inventorypg"
	"webee_rentor/internal/platform/ids"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
)

// FundsReader reports the payments a booking has received, read inside the
// caller's transaction. The payments repository implements it; until then
// callers supply their own.
type FundsReader interface {
	Funds(ctx context.Context, q postgres.Querier, b *rentals.Booking) (rentals.Funds, error)
}

// Service holds the dependencies of the rental use cases.
type Service struct {
	pool  *pgxpool.Pool
	funds FundsReader
	loc   *time.Location
	now   func() time.Time
	newID func() string
}

// Option customises a Service.
type Option func(*Service)

// WithClock replaces time.Now, for tests and replaying jobs.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// New builds the service. loc is the business timezone.
func New(pool *pgxpool.Pool, funds FundsReader, loc *time.Location, opts ...Option) *Service {
	s := &Service{pool: pool, funds: funds, loc: loc, now: time.Now, newID: ids.New}
	for _, o := range opts {
		o(s)
	}
	return s
}

// jobActor is the system actor of background jobs.
var jobActor = identity.SystemActor("rentals.jobs")

// config is the active configuration a use case decides with.
type config struct {
	policy     rentals.Policy
	allocation inventory.AllocationPolicy
	calendar   *rentals.StoreCalendar
}

func (s *Service) config(ctx context.Context, q postgres.Querier) (config, error) {
	var c config
	if _, err := postgres.ActiveDocument(ctx, q, postgres.DocRentalPolicy, &c.policy); err != nil {
		return config{}, err
	}
	if _, err := postgres.ActiveDocument(ctx, q, postgres.DocAllocationPolicy, &c.allocation); err != nil {
		return config{}, err
	}
	cal, err := rentalspg.LoadCalendar(ctx, q, s.loc)
	if err != nil {
		return config{}, err
	}
	c.calendar = cal
	return c, nil
}

// lockInventory locks every asset of the variants and reads their active
// allocations, in that order (see inventorypg).
func lockInventory(ctx context.Context, q postgres.Querier, variants []catalog.VariantID, policy inventory.AllocationPolicy) (rentals.InventoryView, error) {
	assets, err := inventorypg.LockAssetsOfVariants(ctx, q, variants)
	if err != nil {
		return rentals.InventoryView{}, err
	}
	allocs, err := inventorypg.ActiveAllocationsOn(ctx, q, assetIDs(assets))
	if err != nil {
		return rentals.InventoryView{}, err
	}
	return rentals.InventoryView{Assets: assets, Allocations: allocs, Policy: policy}, nil
}

func assetIDs(assets []inventory.Asset) []inventory.AssetID {
	out := make([]inventory.AssetID, len(assets))
	for i, a := range assets {
		out[i] = a.ID
	}
	return out
}

func lineVariants(lines []rentals.Line) []catalog.VariantID {
	var out []catalog.VariantID
	for _, l := range lines {
		for _, c := range l.Components {
			out = append(out, c.VariantID)
		}
	}
	return out
}

// materialise gives planned allocations their IDs.
func (s *Service) materialise(planned []inventory.PlannedAllocation, now time.Time) ([]inventory.Allocation, error) {
	out := make([]inventory.Allocation, 0, len(planned))
	for _, p := range planned {
		a, err := inventory.NewAllocation(inventory.AllocationID(s.newID()), p, now)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// releaseByID releases the listed allocations from allocs.
func releaseByID(ctx context.Context, q postgres.Querier, allocs []inventory.Allocation, release []inventory.AllocationID, reason string, now time.Time) error {
	for _, id := range release {
		i := slices.IndexFunc(allocs, func(a inventory.Allocation) bool { return a.ID == id })
		if i < 0 {
			return fmt.Errorf("allocation %s to release: %w", id, postgres.ErrNotFound)
		}
		a := allocs[i]
		changed, err := a.Release(now, reason)
		if err != nil {
			return err
		}
		if changed {
			if err := inventorypg.UpdateAllocation(ctx, q, a); err != nil {
				return err
			}
		}
	}
	return nil
}

// record writes the transitions with their audit events and applies the
// allocation releases they ask for.
func (s *Service) record(ctx context.Context, q postgres.Querier, ts ...rentals.Transition) error {
	for _, t := range ts {
		if err := rentalspg.AppendTransition(ctx, q, t); err != nil {
			return err
		}
		e := audit.Event{
			ID: s.newID(), OccurredAt: t.At, Actor: t.Actor, Action: string(t.Action),
			ResourceType: "booking", ResourceID: string(t.BookingID), Result: audit.ResultSuccess, Reason: t.Reason,
		}
		if t.From != t.To {
			e.Changes = map[string]audit.Change{"status": {From: string(t.From), To: string(t.To)}}
		}
		if err := auditpg.Append(ctx, q, e); err != nil {
			return err
		}
		if t.ReleaseAllocations {
			if _, err := inventorypg.ReleaseActive(ctx, q, string(t.BookingID), "", string(t.Action)+": "+t.Reason, t.At); err != nil {
				return err
			}
		}
		if t.ReleaseModificationHolds != "" {
			if _, err := inventorypg.ReleaseActive(ctx, q, "", string(t.ReleaseModificationHolds), string(t.Action)+": "+t.Reason, t.At); err != nil {
				return err
			}
		}
	}
	return nil
}

// recordAsset writes an asset movement and its audit event.
func (s *Service) recordAsset(ctx context.Context, q postgres.Querier, a inventory.Asset, c inventory.StatusChange, bookingID rentals.BookingID) error {
	if err := inventorypg.UpdateAsset(ctx, q, a, c); err != nil {
		return err
	}
	return auditpg.Append(ctx, q, audit.Event{
		ID: s.newID(), OccurredAt: c.At, Actor: c.Actor, Action: string(c.Action),
		ResourceType: "asset", ResourceID: string(a.ID), Result: audit.ResultSuccess, Reason: c.Reason,
		References: map[string]string{"booking_id": string(bookingID)},
		Changes:    map[string]audit.Change{"status": {From: string(c.From), To: string(c.To)}},
	})
}

// change is the body of a booking use case: it mutates b (already locked
// and authorised) and returns the transitions to record.
type change func(tx pgx.Tx, b *rentals.Booking, scope identity.Scope, now time.Time) ([]rentals.Transition, error)

// mutate runs a booking use case: lock, authorise, apply, save, record.
func (s *Service) mutate(ctx context.Context, p identity.Principal, id rentals.BookingID, action rentals.Action, fn change) (*rentals.Booking, error) {
	var out *rentals.Booking
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		b, v, err := rentalspg.LoadBooking(ctx, tx, id, true)
		if err != nil {
			return err
		}
		scope, err := rentals.Authorize(p, action, b)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		ts, err := fn(tx, b, scope, now)
		if err != nil {
			return err
		}
		if _, err := rentalspg.SaveBooking(ctx, tx, b, v, "", now); err != nil {
			return err
		}
		if err := s.record(ctx, tx, ts...); err != nil {
			return err
		}
		out = b
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", action, id, err)
	}
	return out, nil
}

// endOpenModification cancels the booking's open change request, if any,
// before the booking itself ends.
func endOpenModification(ctx context.Context, q postgres.Querier, b *rentals.Booking, actor identity.Actor, note string, now time.Time) ([]rentals.Transition, error) {
	if b.OpenModification == "" {
		return nil, nil
	}
	m, v, err := rentalspg.LoadModification(ctx, q, b.OpenModification, true)
	if err != nil {
		return nil, err
	}
	t, err := m.Cancel(b, actor, note, now)
	if err != nil {
		return nil, err
	}
	if _, err := rentalspg.SaveModification(ctx, q, m, v, now); err != nil {
		return nil, err
	}
	return []rentals.Transition{t}, nil
}
