// Package inventoryapp runs the inventory use cases against PostgreSQL:
// registering assets, physical turnaround (inspection, cleaning, repair,
// loss, retirement) and the late-return conflict view. Handover and return
// change asset status through rentalsapp, together with the booking.
package inventoryapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/catalog"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/inventory/inventorypg"
	"webee_rentor/internal/platform/ids"
	"webee_rentor/internal/platform/postgres"
)

// Service holds the dependencies of the inventory use cases.
type Service struct {
	pool  *pgxpool.Pool
	now   func() time.Time
	newID func() string
}

// Option customises a Service.
type Option func(*Service)

// WithClock replaces time.Now, for tests.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// New builds the service.
func New(pool *pgxpool.Pool, opts ...Option) *Service {
	s := &Service{pool: pool, now: time.Now, newID: ids.New}
	for _, o := range opts {
		o(s)
	}
	return s
}

// actionPermissions says which permission each physical change needs
// (decision D22): rental staff or the inventory manager may pass inspection
// and confirm cleaning; repair, loss, found and retirement are inventory
// management.
var actionPermissions = map[inventory.AssetAction][]identity.Permission{
	inventory.ActionPassInspection:   {identity.PermRentalReturnInspect, identity.PermInventoryManage},
	inventory.ActionRequireCleaning:  {identity.PermRentalReturnInspect, identity.PermInventoryManage},
	inventory.ActionCompleteCleaning: {identity.PermRentalReturnInspect, identity.PermInventoryManage},
	inventory.ActionSendToRepair:     {identity.PermInventoryManage},
	inventory.ActionCompleteRepair:   {identity.PermInventoryManage},
	inventory.ActionReportMissing:    {identity.PermInventoryManage},
	inventory.ActionRecordFound:      {identity.PermInventoryManage},
	inventory.ActionRetire:           {identity.PermInventoryManage},
}

func authorize(p identity.Principal, action inventory.AssetAction) error {
	perms, ok := actionPermissions[action]
	if !ok {
		// Handover and return only happen through the booking use cases.
		return fmt.Errorf("asset action %s: %w", action, identity.ErrForbidden)
	}
	for _, perm := range perms {
		if p.Can(perm) {
			return nil
		}
	}
	return p.Require(perms[0])
}

// RegisterAsset adds a physical unit. It starts NEEDS_INSPECTION and must
// pass inspection before it can be handed over (decision D18).
func (s *Service) RegisterAsset(ctx context.Context, p identity.Principal, code string, variant catalog.VariantID) (inventory.Asset, error) {
	if err := p.Require(identity.PermInventoryManage); err != nil {
		return inventory.Asset{}, err
	}
	a, intake, err := inventory.NewAsset(inventory.AssetID(s.newID()), code, variant, p.Actor(), s.now())
	if err != nil {
		return inventory.Asset{}, err
	}
	err = postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := inventorypg.InsertAsset(ctx, tx, a, intake); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, intake)
	})
	if err != nil {
		return inventory.Asset{}, fmt.Errorf("register asset %s: %w", code, err)
	}
	return a, nil
}

// StatusInput is a physical change. Checklist is used by inspection
// actions; Reason is required by repair, missing, found and retire.
type StatusInput struct {
	Action    inventory.AssetAction
	Checklist inventory.InspectionChecklist
	Reason    string
}

// ChangeStatus applies a physical transition. When the asset is resolved
// after a rental (READY or REPAIR after return, or MISSING/RETIRED), the
// allocation it came back under is closed, which frees the rest of its
// turnaround buffer for new bookings. Allocations of future bookings are
// never touched: if their asset is lost, staff re-allocate them.
//
// A RENTED asset is refused: its booking owns what happens to it, through
// rentalsapp.RecordReturn, ReportMissing and ConfirmLost.
func (s *Service) ChangeStatus(ctx context.Context, p identity.Principal, id inventory.AssetID, in StatusInput) (inventory.Asset, error) {
	if err := authorize(p, in.Action); err != nil {
		return inventory.Asset{}, err
	}
	var out inventory.Asset
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		assets, err := inventorypg.LockAssets(ctx, tx, []inventory.AssetID{id})
		if err != nil {
			return err
		}
		a := assets[0]
		if a.Status == inventory.AssetRented {
			return inventory.ErrAssetOnRental
		}
		now := s.now().UTC()
		c, err := apply(&a, in, p.Actor(), now)
		if err != nil {
			return err
		}
		if err := inventorypg.UpdateAsset(ctx, tx, a, c); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, a, c); err != nil {
			return err
		}
		if err := closeResolved(ctx, tx, a, now); err != nil {
			return err
		}
		out = a
		return nil
	})
	if err != nil {
		return inventory.Asset{}, fmt.Errorf("asset %s %s: %w", id, in.Action, err)
	}
	return out, nil
}

func apply(a *inventory.Asset, in StatusInput, actor identity.Actor, now time.Time) (inventory.StatusChange, error) {
	switch in.Action {
	case inventory.ActionPassInspection:
		return a.PassInspection(actor, now, in.Checklist)
	case inventory.ActionRequireCleaning:
		return a.RequireCleaning(actor, now, in.Checklist)
	case inventory.ActionCompleteCleaning:
		return a.CompleteCleaning(actor, now)
	case inventory.ActionSendToRepair:
		return a.SendToRepair(actor, now, in.Reason)
	case inventory.ActionCompleteRepair:
		return a.CompleteRepair(actor, now)
	case inventory.ActionReportMissing:
		return a.ReportMissing(actor, now, in.Reason)
	case inventory.ActionRecordFound:
		return a.RecordFound(actor, now, in.Reason)
	case inventory.ActionRetire:
		return a.Retire(actor, now, in.Reason)
	}
	return inventory.StatusChange{}, domainerr.Invalid("action", "unknown asset action")
}

// closeResolved closes the handed-over allocations of the asset that the
// new status resolves.
func closeResolved(ctx context.Context, q postgres.Querier, a inventory.Asset, now time.Time) error {
	allocs, err := inventorypg.ActiveAllocationsOn(ctx, q, []inventory.AssetID{a.ID})
	if err != nil {
		return err
	}
	for _, al := range allocs {
		if al.HandedOverAt == nil {
			continue
		}
		err := al.Close(now, a)
		if errors.Is(err, inventory.ErrAssetNotResolved) {
			continue
		}
		if err != nil {
			return err
		}
		if err := inventorypg.UpdateAllocation(ctx, q, al); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) audit(ctx context.Context, q postgres.Querier, a inventory.Asset, c inventory.StatusChange) error {
	return auditpg.Append(ctx, q, audit.Event{
		ID: s.newID(), OccurredAt: c.At, Actor: c.Actor, Action: string(c.Action),
		ResourceType: "asset", ResourceID: string(a.ID), Result: audit.ResultSuccess, Reason: c.Reason,
		Changes: map[string]audit.Change{"status": {From: string(c.From), To: string(c.To)}},
	})
}

// Conflict is a late return and the upcoming allocations of the same asset
// it endangers. Staff resolve it (equivalent asset, reschedule, contact the
// customer, cancel per policy); the system never double-allocates.
type Conflict struct {
	Overdue    inventory.Allocation
	Endangered []inventory.Allocation
}

// maxOverdueScan bounds one conflict scan.
const maxOverdueScan = 200

// OverdueConflicts lists late returns whose asset is needed by another
// booking whose block starts within horizon.
func (s *Service) OverdueConflicts(ctx context.Context, p identity.Principal, horizon time.Duration) ([]Conflict, error) {
	if !p.Can(identity.PermInventoryRead) && !p.Can(identity.PermBookingReadAll) {
		return nil, p.Require(identity.PermInventoryRead)
	}
	now := s.now().UTC()
	overdue, err := inventorypg.InUseEndedBefore(ctx, s.pool, now, maxOverdueScan)
	if err != nil {
		return nil, err
	}
	var out []Conflict
	for _, o := range overdue {
		others, err := inventorypg.ActiveAllocationsOn(ctx, s.pool, []inventory.AssetID{o.AssetID})
		if err != nil {
			return nil, err
		}
		if c := inventory.OverdueConflicts(o, others, now, horizon); len(c) > 0 {
			out = append(out, Conflict{Overdue: o, Endangered: c})
		}
	}
	return out, nil
}
