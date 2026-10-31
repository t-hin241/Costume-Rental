package rentalsapp

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/catalog/catalogpg"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/inventory/inventorypg"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/payments/paymentspg"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
)

// LineChangeInput replaces the choice on one existing line.
type LineChangeInput struct {
	LineID rentals.LineID
	Line   LineInput
}

// ModificationInput is a size, bundle or package change request.
type ModificationInput struct {
	Changes []LineChangeInput
	// NewPackageID changes the package (and so the due time) of every line.
	NewPackageID catalog.PackageID
	Reason       string
	StoreFault   bool
}

// RequestModification records a change request. It holds nothing yet;
// QuoteModification prices it and holds the inventory.
func (s *Service) RequestModification(ctx context.Context, p identity.Principal, id rentals.BookingID, in ModificationInput) (*rentals.Modification, error) {
	var out *rentals.Modification
	_, err := s.mutate(ctx, p, id, rentals.ActionModify, func(tx pgx.Tx, b *rentals.Booking, scope identity.Scope, now time.Time) ([]rentals.Transition, error) {
		pkgID := b.Package.ID
		var newPkg *catalog.RentalPackage
		if in.NewPackageID != "" {
			pkg, err := catalogpg.Package(ctx, tx, in.NewPackageID)
			if err != nil {
				return nil, err
			}
			newPkg, pkgID = &pkg, pkg.ID
		}
		pkg, err := catalogpg.Package(ctx, tx, pkgID)
		if err != nil {
			return nil, err
		}
		var changes []rentals.LineChange
		if len(in.Changes) > 0 {
			lines := make([]LineInput, len(in.Changes))
			for i, c := range in.Changes {
				lines[i] = c.Line
			}
			quoted, err := quoteLines(ctx, tx, pkg, lines)
			if err != nil {
				return nil, err
			}
			for i, c := range in.Changes {
				changes = append(changes, rentals.LineChange{LineID: c.LineID, Quote: quoted[i].Quote})
			}
		}
		m, t, err := b.RequestModification(rentals.ModificationRequest{
			ID: rentals.ModificationID(s.newID()), Actor: p.Actor(), Scope: scope, Reason: in.Reason,
			StoreFault: in.StoreFault, Changes: changes, NewPackage: newPkg, Now: now,
		})
		if err != nil {
			return nil, err
		}
		if _, err := rentalspg.SaveModification(ctx, tx, m, 0, now); err != nil {
			return nil, err
		}
		out = m
		return []rentals.Transition{t}, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// QuoteModification prices a requested change and holds the inventory it
// needs as tentative allocations, without touching the current ones. The
// holds block other bookings until the change is confirmed, rejected,
// cancelled or its hold expires.
func (s *Service) QuoteModification(ctx context.Context, p identity.Principal, id rentals.ModificationID) (*rentals.Modification, error) {
	return s.withModification(ctx, p, id, func(tx pgx.Tx, b *rentals.Booking, m *rentals.Modification, scope identity.Scope, now time.Time) (rentals.Transition, error) {
		cfg, err := s.config(ctx, tx)
		if err != nil {
			return rentals.Transition{}, err
		}
		variants := lineVariants(b.Lines)
		for _, c := range m.Changes {
			for _, comp := range c.Quote.Components {
				variants = append(variants, comp.VariantID)
			}
		}
		view, err := lockBookingInventory(ctx, tx, b.ID, variants, cfg.allocation)
		if err != nil {
			return rentals.Transition{}, err
		}
		t, err := m.Quote(b, rentals.QuoteInput{Actor: p.Actor(), Scope: scope, Calendar: cfg.calendar, Inventory: view, Now: now})
		if err != nil {
			return rentals.Transition{}, err
		}
		holds, err := s.materialise(m.Plan.Hold, now)
		if err != nil {
			return rentals.Transition{}, err
		}
		if err := inventorypg.InsertAllocations(ctx, tx, holds); err != nil {
			return rentals.Transition{}, err
		}
		l, lv, err := paymentspg.LoadLedger(ctx, tx, string(b.ID), true)
		if err != nil {
			return rentals.Transition{}, err
		}
		if _, err := l.StageRepricing(string(m.ID), m.After.Rent, m.After.Security, p.Actor(), now); err != nil {
			return rentals.Transition{}, err
		}
		_, err = paymentspg.SaveLedger(ctx, tx, l, lv, now)
		return t, err
	})
}

// ConfirmModification applies a quoted change once its increase is paid: in
// one transaction the booking takes the new terms, the holds replace the old
// allocations and the ledger is re-priced. What the customer overpaid goes to
// a refund awaiting approval.
func (s *Service) ConfirmModification(ctx context.Context, p identity.Principal, id rentals.ModificationID) (*rentals.Modification, error) {
	return s.withModification(ctx, p, id, func(tx pgx.Tx, b *rentals.Booking, m *rentals.Modification, _ identity.Scope, now time.Time) (rentals.Transition, error) {
		l, lv, err := paymentspg.LoadLedger(ctx, tx, string(b.ID), true)
		if err != nil {
			return rentals.Transition{}, err
		}
		t, err := m.Confirm(b, p.Actor(), l.PaidForModification(string(m.ID)), now)
		if err != nil {
			return rentals.Transition{}, err
		}
		if err := applyChangePlan(ctx, tx, m, now); err != nil {
			return rentals.Transition{}, err
		}
		if err := l.CommitRepricing(string(m.ID), payments.ChargeID(s.newID()), payments.ChargeID(s.newID()), p.Actor(), now); err != nil {
			return rentals.Transition{}, err
		}
		if err := s.refundExcess(ctx, tx, l, fmt.Sprintf("booking modification %s", m.ID), p.Actor(), now); err != nil {
			return rentals.Transition{}, err
		}
		_, err = paymentspg.SaveLedger(ctx, tx, l, lv, now)
		return t, err
	})
}

// applyChangePlan moves the allocations to a confirmed plan. Extension holds
// are released first so reblocking their allocation cannot overlap them.
func applyChangePlan(ctx context.Context, q postgres.Querier, m *rentals.Modification, now time.Time) error {
	allocs, err := inventorypg.AllocationsOfBooking(ctx, q, string(m.BookingID), true)
	if err != nil {
		return err
	}
	if _, err := inventorypg.LockAssets(ctx, q, allocatedAssets(allocs)); err != nil {
		return err
	}
	var extensions, adopted []inventory.AllocationID
	for _, a := range allocs {
		switch {
		case a.ModificationRef != string(m.ID):
		case a.ExtendsAllocation != "":
			extensions = append(extensions, a.ID)
		default:
			adopted = append(adopted, a.ID)
		}
	}
	if err := releaseByID(ctx, q, allocs, extensions, "modification confirmed", now); err != nil {
		return err
	}
	for _, r := range m.Plan.Reblock {
		i := slices.IndexFunc(allocs, func(a inventory.Allocation) bool { return a.ID == r.AllocationID })
		if i < 0 {
			return fmt.Errorf("allocation %s to reblock: %w", r.AllocationID, postgres.ErrNotFound)
		}
		if err := allocs[i].Reblock(r.Block); err != nil {
			return err
		}
		if err := inventorypg.UpdateAllocation(ctx, q, allocs[i]); err != nil {
			return err
		}
	}
	for _, id := range adopted {
		i := slices.IndexFunc(allocs, func(a inventory.Allocation) bool { return a.ID == id })
		if err := allocs[i].Adopt(); err != nil {
			return err
		}
		if err := inventorypg.UpdateAllocation(ctx, q, allocs[i]); err != nil {
			return err
		}
	}
	return releaseByID(ctx, q, allocs, m.Plan.Release, "modification confirmed", now)
}

// dropRepricing discards the staged prices of a change that ended without
// applying; money already paid for it goes to a refund awaiting approval.
func (s *Service) dropRepricing(ctx context.Context, q postgres.Querier, m *rentals.Modification, actor identity.Actor, now time.Time) error {
	l, lv, err := paymentspg.LoadLedger(ctx, q, string(m.BookingID), true)
	if err != nil {
		return err
	}
	l.DiscardRepricing(string(m.ID))
	if err := s.refundExcess(ctx, q, l, fmt.Sprintf("booking modification %s ended", m.ID), actor, now); err != nil {
		return err
	}
	_, err = paymentspg.SaveLedger(ctx, q, l, lv, now)
	return err
}

// ModificationOutcome ends an open change request without applying it.
type ModificationOutcome string

const (
	// ModificationRejected: staff decline the change.
	ModificationRejected ModificationOutcome = "REJECT"
	// ModificationWithdrawn: the requester withdraws it.
	ModificationWithdrawn ModificationOutcome = "CANCEL"
)

// EndModification rejects or withdraws a change request and releases its
// holds; the booking keeps its terms and allocations.
func (s *Service) EndModification(ctx context.Context, p identity.Principal, id rentals.ModificationID, outcome ModificationOutcome, note string) (*rentals.Modification, error) {
	return s.withModification(ctx, p, id, func(tx pgx.Tx, b *rentals.Booking, m *rentals.Modification, scope identity.Scope, now time.Time) (rentals.Transition, error) {
		var t rentals.Transition
		var err error
		switch outcome {
		case ModificationRejected:
			if scope != identity.ScopeAny {
				return rentals.Transition{}, fmt.Errorf("reject modification %s: %w", id, identity.ErrForbidden)
			}
			t, err = m.Reject(b, p.Actor(), note, now)
		case ModificationWithdrawn:
			if strings.TrimSpace(note) == "" {
				note = "withdrawn"
			}
			t, err = m.Cancel(b, p.Actor(), note, now)
		default:
			return rentals.Transition{}, domainerr.Invalid("outcome", "must be REJECT or CANCEL")
		}
		if err != nil {
			return rentals.Transition{}, err
		}
		return t, s.dropRepricing(ctx, tx, m, p.Actor(), now)
	})
}

// ExpireModificationHolds ends quoted changes whose hold ran out and
// releases their holds; safe to run repeatedly.
func (s *Service) ExpireModificationHolds(ctx context.Context, limit int) (int, error) {
	due, err := rentalspg.ExpiredModificationHolds(ctx, s.pool, s.now(), limit)
	if err != nil {
		return 0, err
	}
	return expireEach(ctx, due, func(id rentals.ModificationID) (bool, error) {
		changed := false
		err := s.inModification(ctx, id, func(tx pgx.Tx, b *rentals.Booking, m *rentals.Modification, now time.Time) (*rentals.Transition, error) {
			t, ok, err := m.Expire(b, jobActor, now)
			changed = ok
			if err != nil || !ok {
				return nil, err
			}
			return &t, s.dropRepricing(ctx, tx, m, jobActor, now)
		})
		return changed, err
	})
}

type modChange func(tx pgx.Tx, b *rentals.Booking, m *rentals.Modification, scope identity.Scope, now time.Time) (rentals.Transition, error)

// withModification authorises the principal on the modification's booking
// and runs fn with both locked.
func (s *Service) withModification(ctx context.Context, p identity.Principal, id rentals.ModificationID, fn modChange) (*rentals.Modification, error) {
	var out *rentals.Modification
	err := s.inModification(ctx, id, func(tx pgx.Tx, b *rentals.Booking, m *rentals.Modification, now time.Time) (*rentals.Transition, error) {
		scope, err := rentals.Authorize(p, rentals.ActionModify, b)
		if err != nil {
			return nil, err
		}
		t, err := fn(tx, b, m, scope, now)
		if err != nil {
			return nil, err
		}
		out = m
		return &t, nil
	})
	if err != nil {
		return nil, fmt.Errorf("modification %s: %w", id, err)
	}
	return out, nil
}

// inModification locks the booking then the modification (the global lock
// order), runs fn and saves both when fn returns a transition.
func (s *Service) inModification(ctx context.Context, id rentals.ModificationID, fn func(tx pgx.Tx, b *rentals.Booking, m *rentals.Modification, now time.Time) (*rentals.Transition, error)) error {
	return postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		peek, _, err := rentalspg.LoadModification(ctx, tx, id, false)
		if err != nil {
			return err
		}
		b, bv, err := rentalspg.LoadBooking(ctx, tx, peek.BookingID, true)
		if err != nil {
			return err
		}
		m, mv, err := rentalspg.LoadModification(ctx, tx, id, true)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		t, err := fn(tx, b, m, now)
		if err != nil || t == nil {
			return err
		}
		if _, err := rentalspg.SaveModification(ctx, tx, m, mv, now); err != nil {
			return err
		}
		if _, err := rentalspg.SaveBooking(ctx, tx, b, bv, "", now); err != nil {
			return err
		}
		return s.record(ctx, tx, *t)
	})
}
