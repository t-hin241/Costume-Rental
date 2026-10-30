package rentalsapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/catalog"
	"webee_rentor/internal/catalog/catalogpg"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/inventory/inventorypg"
	"webee_rentor/internal/platform/ids"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
)

// LineInput is what the customer picked for one line. Prices and deposits
// always come from the catalog, never from the client.
type LineInput struct {
	Kind       catalog.OfferingKind
	OfferingID string
	// VariantID is the size/colour of an ITEM line.
	VariantID catalog.VariantID
	// Selections maps bundle component keys to variants; components with a
	// single option may be omitted.
	Selections map[string]catalog.VariantID
	Quantity   int
}

// PlaceInput is a booking request. The customer is the authenticated
// principal; it is never taken from input.
type PlaceInput struct {
	// RequestKey makes client retries safe: the same customer and key always
	// return the booking created first.
	RequestKey string
	PackageID  catalog.PackageID
	PickupDate businesstime.Date
	SlotID     string
	Outbound   rentals.LegMode
	Return     rentals.LegMode
	Lines      []LineInput
}

// errCourierNotWired: courier legs need the delivery zone resolution from
// the customer address, which this step does not persist yet.
var errCourierNotWired = fmt.Errorf("courier legs need the delivery module: %w", rentals.ErrDeliveryNotOffered)

// PlaceBooking creates a booking with its snapshot and allocates every
// component in one transaction. If any component is short nothing is
// written and the error matches inventory.ErrInsufficientInventory.
func (s *Service) PlaceBooking(ctx context.Context, p identity.Principal, in PlaceInput) (*rentals.Booking, error) {
	if err := rentals.AuthorizeCreate(p); err != nil {
		return nil, err
	}
	b, err := s.place(ctx, p, in)
	if errors.Is(err, postgres.ErrDuplicate) && in.RequestKey != "" {
		// A concurrent retry with the same key committed first.
		return s.byRequestKey(ctx, p, in.RequestKey)
	}
	return b, err
}

func (s *Service) place(ctx context.Context, p identity.Principal, in PlaceInput) (*rentals.Booking, error) {
	var out *rentals.Booking
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if in.RequestKey != "" {
			id, ok, err := rentalspg.BookingIDByRequestKey(ctx, tx, string(p.UserID), in.RequestKey)
			if err != nil {
				return err
			}
			if ok {
				out, _, err = rentalspg.LoadBooking(ctx, tx, id, false)
				return err
			}
		}
		now := s.now().UTC()
		req, err := s.placeRequest(ctx, tx, p, in, true)
		if err != nil {
			return err
		}
		req.Now = now
		pl, err := rentals.PlaceBooking(req)
		if err != nil {
			return err
		}
		if _, err := rentalspg.SaveBooking(ctx, tx, pl.Booking, 0, in.RequestKey, now); err != nil {
			return err
		}
		allocs, err := s.materialise(pl.Allocations, now)
		if err != nil {
			return err
		}
		if err := inventorypg.InsertAllocations(ctx, tx, allocs); err != nil {
			return err
		}
		if err := s.record(ctx, tx, pl.Transition); err != nil {
			return err
		}
		out = pl.Booking
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("place booking: %w", err)
	}
	return out, nil
}

func (s *Service) byRequestKey(ctx context.Context, p identity.Principal, key string) (*rentals.Booking, error) {
	id, ok, err := rentalspg.BookingIDByRequestKey(ctx, s.pool, string(p.UserID), key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("booking by request key: %w", postgres.ErrNotFound)
	}
	b, _, err := rentalspg.LoadBooking(ctx, s.pool, id, false)
	return b, err
}

// placeRequest prices the lines from the current catalog, resolves the
// schedule and reads inventory, locking it when lock is set.
func (s *Service) placeRequest(ctx context.Context, q postgres.Querier, p identity.Principal, in PlaceInput, lock bool) (rentals.PlaceRequest, error) {
	if in.Outbound == rentals.LegCourier || in.Return == rentals.LegCourier {
		return rentals.PlaceRequest{}, errCourierNotWired
	}
	cfg, err := s.config(ctx, q)
	if err != nil {
		return rentals.PlaceRequest{}, err
	}
	pkg, err := catalogpg.Package(ctx, q, in.PackageID)
	if err != nil {
		return rentals.PlaceRequest{}, err
	}
	lines, err := quoteLines(ctx, q, pkg, in.Lines)
	if err != nil {
		return rentals.PlaceRequest{}, err
	}
	schedule, err := cfg.calendar.ResolveFulfillment(in.PickupDate, in.Outbound, in.SlotID, pkg.Duration, in.Return)
	if err != nil {
		return rentals.PlaceRequest{}, err
	}
	var variants []catalog.VariantID
	for _, l := range lines {
		for _, c := range l.Quote.Components {
			variants = append(variants, c.VariantID)
		}
	}
	var view rentals.InventoryView
	if lock {
		view, err = lockInventory(ctx, q, variants, cfg.allocation)
	} else {
		view, err = readInventory(ctx, q, variants, cfg.allocation)
	}
	if err != nil {
		return rentals.PlaceRequest{}, err
	}
	return rentals.PlaceRequest{
		BookingID: rentals.BookingID(s.newID()), Code: ids.Code("BK", 8), CustomerID: p.UserID, Actor: p.Actor(),
		Schedule: schedule, Calendar: cfg.calendar, Lines: lines,
		Fulfillment: rentals.FulfillmentRequest{Outbound: in.Outbound, Return: in.Return},
		Policy:      cfg.policy, Inventory: view,
	}, nil
}

func readInventory(ctx context.Context, q postgres.Querier, variants []catalog.VariantID, policy inventory.AllocationPolicy) (rentals.InventoryView, error) {
	assets, err := inventorypg.Assets(ctx, q, variants)
	if err != nil {
		return rentals.InventoryView{}, err
	}
	allocs, err := inventorypg.ActiveAllocationsOn(ctx, q, assetIDs(assets))
	if err != nil {
		return rentals.InventoryView{}, err
	}
	return rentals.InventoryView{Assets: assets, Allocations: allocs, Policy: policy}, nil
}

// maxLinesQuoted bounds catalog reads before the policy limit is applied.
const maxLinesQuoted = 50

// quoteLines prices the customer's lines; line IDs are L1, L2, ...
func quoteLines(ctx context.Context, q postgres.Querier, pkg catalog.RentalPackage, in []LineInput) ([]rentals.LineRequest, error) {
	if len(in) == 0 || len(in) > maxLinesQuoted {
		return nil, domainerr.Invalid("lines", fmt.Sprintf("between 1 and %d lines are required", maxLinesQuoted))
	}
	bundles := map[catalog.BundleID]catalog.Bundle{}
	var itemIDs []catalog.ItemID
	for _, l := range in {
		switch l.Kind {
		case catalog.KindItem:
			itemIDs = append(itemIDs, catalog.ItemID(l.OfferingID))
		case catalog.KindBundle:
			id := catalog.BundleID(l.OfferingID)
			if _, ok := bundles[id]; ok {
				continue
			}
			b, err := catalogpg.Bundle(ctx, q, id)
			if err != nil {
				return nil, err
			}
			bundles[id] = b
			for _, c := range b.Components {
				itemIDs = append(itemIDs, c.ItemID)
			}
		default:
			return nil, domainerr.Invalid("lines.kind", "must be ITEM or BUNDLE")
		}
	}
	items, err := catalogpg.Items(ctx, q, itemIDs)
	if err != nil {
		return nil, err
	}
	out := make([]rentals.LineRequest, 0, len(in))
	for i, l := range in {
		var quote catalog.LineQuote
		if l.Kind == catalog.KindItem {
			quote, err = catalog.QuoteItem(items[catalog.ItemID(l.OfferingID)], l.VariantID, pkg, l.Quantity)
		} else {
			quote, err = catalog.QuoteBundle(bundles[catalog.BundleID(l.OfferingID)], items, l.Selections, pkg, l.Quantity)
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		out = append(out, rentals.LineRequest{ID: rentals.LineID(fmt.Sprintf("L%d", i+1)), Quote: quote})
	}
	return out, nil
}

// Availability is a reference answer for the booking form. It is not a
// reservation: PlaceBooking checks again under lock.
type Availability struct {
	Available bool
	Shortages []inventory.Shortage
}

// CheckAvailability runs the placement rules on a consistent read-only
// snapshot without locking or writing anything.
func (s *Service) CheckAvailability(ctx context.Context, in PlaceInput) (Availability, error) {
	var out Availability
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		guest := identity.Principal{UserID: "availability-check"}
		req, err := s.placeRequest(ctx, tx, guest, in, false)
		if err != nil {
			return err
		}
		req.Actor, req.Now = jobActor, s.now().UTC()
		_, err = rentals.PlaceBooking(req)
		var short *inventory.ShortageError
		switch {
		case errors.As(err, &short):
			out.Shortages = short.Shortages
		case err != nil:
			return err
		default:
			out.Available = true
		}
		return nil
	})
	if err != nil {
		return Availability{}, fmt.Errorf("check availability: %w", err)
	}
	return out, nil
}
