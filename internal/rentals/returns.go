package rentals

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/money"
)

// Condition classifies what inspection found on a returned item (fee
// policy §3.1). An empty list means no issue.
type Condition string

const (
	// Not chargeable: part of normal use and routine cleaning.
	CondNormalWear     Condition = "NORMAL_WEAR"
	CondRoutineSoiling Condition = "ROUTINE_SOILING"
	// Chargeable with a verified cost.
	CondAbnormalSoiling Condition = "ABNORMAL_SOILING"
	CondMinorRepairable Condition = "MINOR_REPAIRABLE"
	CondMajorRepairable Condition = "MAJOR_REPAIRABLE"
	CondIrreparable     Condition = "IRREPARABLE"
	// CondNeedsVerification keeps inspection open until reclassified.
	CondNeedsVerification Condition = "NEEDS_VERIFICATION"
)

func (c Condition) valid() bool {
	switch c {
	case CondNormalWear, CondRoutineSoiling, CondAbnormalSoiling, CondMinorRepairable,
		CondMajorRepairable, CondIrreparable, CondNeedsVerification:
		return true
	}
	return false
}

// chargeable conditions need photo/document evidence.
func (c Condition) chargeable() bool {
	return c == CondAbnormalSoiling || c == CondMinorRepairable || c == CondMajorRepairable || c == CondIrreparable
}

// InspectionFinding is the inspection result for one returned item.
type InspectionFinding struct {
	Conditions  []Condition
	Notes       string
	Evidence    []string // object-storage keys
	InspectedBy identity.Actor
	InspectedAt time.Time
}

func (f InspectionFinding) has(c Condition) bool { return slices.Contains(f.Conditions, c) }

// RentedItem is one physical asset handed over under the booking.
type RentedItem struct {
	AssetID      inventory.AssetID
	LineID       LineID
	ComponentKey string
	VariantID    catalog.VariantID
	// DailyRate is the snapshot late-fee base for this item.
	DailyRate money.Money
	// PreexistingIssues records defects noted at handover; they are not
	// charged later without evidence of new damage.
	PreexistingIssues string
	// ReturnedAt is when the customer gave the item back (at the counter or
	// to the courier); late fees stop here. ReceivedAt is when it is back in
	// the store; inspection starts here. They are equal for counter returns.
	ReturnedAt *time.Time
	ReceivedAt *time.Time
	// MissingReportedAt starts MISSING_PENDING; LostConfirmedAt ends it.
	MissingReportedAt *time.Time
	LostConfirmedAt   *time.Time
	// CarrierLiable: lost while in the carrier's hands; the customer is not
	// charged for it. CarrierDamageReported: the carrier reported damage, so
	// damage fees need evidence that the customer caused it.
	CarrierLiable         bool
	CarrierDamageReported bool
	// Set by an accepted delivery complaint (decision D20): NotReceived — the
	// customer never got the item; ArrivalIssue — it arrived wrong or damaged,
	// so later damage fees need evidence of new damage; CollectByStore — the
	// store takes it back, so no late fee applies to it.
	NotReceived    bool
	ArrivalIssue   bool
	CollectByStore bool
	// ReplacementFor / ReplacedBy link a replacement to the item it replaces.
	ReplacementFor inventory.AssetID
	ReplacedBy     inventory.AssetID
	Finding        *InspectionFinding
}

// Resolved reports whether the item is back in the store or confirmed lost.
func (i RentedItem) Resolved() bool { return i.ReceivedAt != nil || i.LostConfirmedAt != nil }

// MissingPending reports whether the item is in the verification window.
func (i RentedItem) MissingPending() bool {
	return i.MissingReportedAt != nil && i.ReturnedAt == nil && i.LostConfirmedAt == nil
}

// inspected reports whether the item needs no further inspection.
func (i RentedItem) inspected() bool {
	if i.LostConfirmedAt != nil {
		return true
	}
	return i.ReceivedAt != nil && i.Finding != nil && !i.Finding.has(CondNeedsVerification)
}

// Item finds a handed-over item.
func (b *Booking) Item(asset inventory.AssetID) (*RentedItem, error) {
	i := slices.IndexFunc(b.Items, func(it RentedItem) bool { return it.AssetID == asset })
	if i < 0 {
		return nil, fmt.Errorf("booking %s asset %s was not handed over: %w", b.ID, asset, ErrAssetMismatch)
	}
	return &b.Items[i], nil
}

// AllocatedAsset is an asset the booking's active allocations hold.
type AllocatedAsset struct {
	AssetID      inventory.AssetID
	LineID       LineID
	ComponentKey string
}

// DeliveredItem is an asset scanned at the counter with its noted condition.
type DeliveredItem struct {
	AssetID           inventory.AssetID
	PreexistingIssues string
}

// HandoverRequest records the items given to the customer.
type HandoverRequest struct {
	Actor identity.Actor
	// Allocated come from the booking's active allocations. Substituting an
	// asset requires re-allocating first.
	Allocated          []AllocatedAsset
	Delivered          []DeliveredItem
	ChecklistCompleted bool
	Funds              Funds
	Now                time.Time
}

// RecordHandover moves PREPARING → RENTED and starts one RentedItem per
// delivered asset.
func (b *Booking) RecordHandover(req HandoverRequest) (Transition, error) {
	if !req.Actor.IsUser() {
		return Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionHandover, StatusPreparing); err != nil {
		return Transition{}, err
	}
	if b.OpenModification != "" {
		return Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrModificationOpen)
	}
	if !req.ChecklistCompleted {
		return Transition{}, ErrChecklistIncomplete
	}
	if !req.Now.Before(b.Schedule.DueAt) {
		return Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrHandoverTooLate)
	}
	allocated := make([]inventory.AssetID, 0, len(req.Allocated))
	for _, a := range req.Allocated {
		allocated = append(allocated, a.AssetID)
	}
	delivered := make([]inventory.AssetID, 0, len(req.Delivered))
	for _, d := range req.Delivered {
		delivered = append(delivered, d.AssetID)
	}
	if len(allocated) == 0 || !sameSet(allocated, delivered) {
		return Transition{}, fmt.Errorf("booking %s handover: %w", b.ID, ErrAssetMismatch)
	}
	items := make([]RentedItem, 0, len(req.Allocated))
	for _, a := range req.Allocated {
		comp, err := b.component(a.LineID, a.ComponentKey)
		if err != nil {
			return Transition{}, err
		}
		d := req.Delivered[slices.IndexFunc(req.Delivered, func(d DeliveredItem) bool { return d.AssetID == a.AssetID })]
		items = append(items, RentedItem{
			AssetID: a.AssetID, LineID: a.LineID, ComponentKey: a.ComponentKey,
			VariantID: comp.VariantID, DailyRate: comp.UnitDailyRate,
			PreexistingIssues: strings.TrimSpace(d.PreexistingIssues),
		})
	}
	ok, err := b.Policy.Handover.SatisfiedBy(b.amounts(), req.Funds)
	if err != nil {
		return Transition{}, fmt.Errorf("booking %s handover: %w", b.ID, err)
	}
	if !ok {
		return Transition{}, fmt.Errorf("booking %s handover: %w", b.ID, ErrInsufficientFunds)
	}
	b.HandedOverAt = ptr(req.Now)
	b.Items = items
	return b.transition(ActionHandover, StatusRented, req.Actor, req.Now, ""), nil
}

func (b *Booking) component(line LineID, key string) (catalog.ComponentDemand, error) {
	for _, l := range b.Lines {
		if l.ID != line {
			continue
		}
		for _, c := range l.Components {
			if c.Key == key {
				return c, nil
			}
		}
	}
	return catalog.ComponentDemand{}, fmt.Errorf("booking %s line %s component %s: %w", b.ID, line, key, ErrAssetMismatch)
}

// IsOverdue reports whether some item is still out past due_at + grace.
// Lateness is derived, not a separate status.
func (b *Booking) IsOverdue(now time.Time) bool {
	if b.Status != StatusRented || now.Before(b.Schedule.DueAt.Add(b.Policy.LateFee.Grace)) {
		return false
	}
	return slices.ContainsFunc(b.Items, func(i RentedItem) bool {
		return i.ReturnedAt == nil && i.LostConfirmedAt == nil && !i.NotReceived && !i.CollectByStore
	})
}

// ReturnRequest records items brought back at one visit. Items may come
// back at different times; each keeps its own returned_at.
type ReturnRequest struct {
	Actor  identity.Actor
	Assets []inventory.AssetID
	Now    time.Time
}

// RecordReturn records returned items. A missing-pending item that turns up
// is returned normally. When every item is resolved the booking moves to
// RETURN_INSPECTION.
func (b *Booking) RecordReturn(req ReturnRequest) (Transition, error) {
	if !req.Actor.IsUser() {
		return Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionReturn, StatusRented); err != nil {
		return Transition{}, err
	}
	if len(req.Assets) == 0 || len(slices.Compact(sorted(req.Assets))) != len(req.Assets) {
		return Transition{}, fmt.Errorf("booking %s return: %w", b.ID, ErrAssetMismatch)
	}
	if req.Now.Before(*b.HandedOverAt) {
		return Transition{}, domainerr.Invalid("returned_at", "cannot be before handover")
	}
	for _, a := range req.Assets {
		it, err := b.Item(a)
		if err != nil {
			return Transition{}, err
		}
		if it.Resolved() || it.ReturnedAt != nil {
			return Transition{}, fmt.Errorf("booking %s asset %s already returned: %w", b.ID, a, ErrAssetMismatch)
		}
	}
	for _, a := range req.Assets {
		it, _ := b.Item(a)
		it.ReturnedAt, it.ReceivedAt = ptr(req.Now), ptr(req.Now)
	}
	return b.afterItemResolved(ActionReturn, req.Actor, req.Now, strings.Join(assetStrings(req.Assets), ",")), nil
}

// ReportMissing marks an item not brought back as MISSING_PENDING. The
// customer is notified and has Policy.Loss.VerificationWindow to find it.
func (b *Booking) ReportMissing(actor identity.Actor, asset inventory.AssetID, now time.Time) (Transition, error) {
	if !actor.IsUser() {
		return Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionReportMissing, StatusRented); err != nil {
		return Transition{}, err
	}
	it, err := b.Item(asset)
	if err != nil {
		return Transition{}, err
	}
	if it.Resolved() || it.ReturnedAt != nil || it.MissingReportedAt != nil {
		return Transition{}, fmt.Errorf("booking %s asset %s already reported or returned: %w", b.ID, asset, ErrAssetMismatch)
	}
	it.MissingReportedAt = ptr(now)
	return Transition{BookingID: b.ID, Action: ActionReportMissing, From: b.Status, To: b.Status, Actor: actor, At: now.UTC(), Reason: string(asset)}, nil
}

// VerificationDeadline is when a missing-pending item may be confirmed lost.
func (b *Booking) VerificationDeadline(asset inventory.AssetID) (time.Time, error) {
	it, err := b.Item(asset)
	if err != nil {
		return time.Time{}, err
	}
	if it.MissingReportedAt == nil {
		return time.Time{}, fmt.Errorf("booking %s asset %s not reported missing: %w", b.ID, asset, ErrAssetMismatch)
	}
	return it.MissingReportedAt.Add(b.Policy.Loss.VerificationWindow), nil
}

// ConfirmLost turns a missing-pending item into a confirmed loss after the
// verification window (authorised with rental.confirm_lost). Late fees stop
// accruing for that item at this moment.
func (b *Booking) ConfirmLost(actor identity.Actor, asset inventory.AssetID, reason string, now time.Time) (Transition, error) {
	if !actor.IsUser() {
		return Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionConfirmLost, StatusRented); err != nil {
		return Transition{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return Transition{}, ErrReasonRequired
	}
	it, err := b.Item(asset)
	if err != nil {
		return Transition{}, err
	}
	if !it.MissingPending() {
		return Transition{}, fmt.Errorf("booking %s asset %s is not missing-pending: %w", b.ID, asset, ErrInvalidTransition)
	}
	deadline := it.MissingReportedAt.Add(b.Policy.Loss.VerificationWindow)
	if now.Before(deadline) {
		return Transition{}, fmt.Errorf("booking %s asset %s until %s: %w", b.ID, asset, deadline, ErrVerificationWindowOpen)
	}
	it.LostConfirmedAt = ptr(now)
	return b.afterItemResolved(ActionConfirmLost, actor, now, string(asset)+": "+reason), nil
}

// afterItemResolved moves to RETURN_INSPECTION once nothing is outstanding.
func (b *Booking) afterItemResolved(action Action, actor identity.Actor, now time.Time, reason string) Transition {
	b.refreshInspection(now)
	if slices.ContainsFunc(b.Items, func(i RentedItem) bool { return !i.Resolved() }) {
		return Transition{BookingID: b.ID, Action: action, From: b.Status, To: b.Status, Actor: actor, At: now.UTC(), Reason: reason}
	}
	b.ReturnedAt = ptr(now)
	return b.transition(action, StatusReturnInspection, actor, now, reason)
}

// RecordInspection records (or replaces) the finding for a returned item.
// Items may be inspected as soon as they come back. Chargeable conditions
// need evidence; NEEDS_VERIFICATION stands alone and keeps inspection open.
func (b *Booking) RecordInspection(actor identity.Actor, asset inventory.AssetID, f InspectionFinding, now time.Time) (Transition, error) {
	if !actor.IsUser() {
		return Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionInspectAsset, StatusRented, StatusReturnInspection); err != nil {
		return Transition{}, err
	}
	it, err := b.Item(asset)
	if err != nil {
		return Transition{}, err
	}
	if it.ReceivedAt == nil {
		return Transition{}, fmt.Errorf("booking %s asset %s is not back in the store: %w", b.ID, asset, ErrAssetMismatch)
	}
	seen := map[Condition]bool{}
	for _, c := range f.Conditions {
		if !c.valid() || seen[c] {
			return Transition{}, domainerr.Invalid("conditions", "unknown or duplicated condition")
		}
		seen[c] = true
		if c.chargeable() && len(f.Evidence) == 0 {
			return Transition{}, domainerr.Invalid("evidence", fmt.Sprintf("%s requires photo or document evidence", c))
		}
	}
	if seen[CondNeedsVerification] && len(f.Conditions) > 1 {
		return Transition{}, domainerr.Invalid("conditions", "NEEDS_VERIFICATION cannot be combined with other conditions")
	}
	f.Conditions, f.Evidence = slices.Clone(f.Conditions), slices.Clone(f.Evidence)
	f.InspectedBy, f.InspectedAt = actor, now.UTC()
	it.Finding = &f
	b.refreshInspection(now)
	return Transition{BookingID: b.ID, Action: ActionInspectAsset, From: b.Status, To: b.Status, Actor: actor, At: now.UTC(), Reason: string(asset)}, nil
}

func (b *Booking) refreshInspection(now time.Time) {
	done := len(b.Items) > 0 && !slices.ContainsFunc(b.Items, func(i RentedItem) bool { return !i.inspected() })
	switch {
	case done && b.InspectionCompletedAt == nil:
		b.InspectionCompletedAt = ptr(now)
	case !done:
		b.InspectionCompletedAt = nil
	}
}

// CompletionCheck carries cross-aggregate facts Complete depends on.
type CompletionCheck struct {
	LedgerSettled bool
	// OpenFeeAssessments counts PROPOSED or DISPUTED assessments (see
	// CountOpenAssessments).
	OpenFeeAssessments int
}

// Complete closes the booking once every item is resolved and inspected, no
// fee decision is open and the ledger is settled. It does not make assets
// READY; their cleaning may still be open.
func (b *Booking) Complete(actor identity.Actor, check CompletionCheck, now time.Time) (Transition, error) {
	if !actor.Valid() {
		return Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionComplete, StatusReturnInspection); err != nil {
		return Transition{}, err
	}
	if b.InspectionCompletedAt == nil {
		return Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrInspectionIncomplete)
	}
	if check.OpenFeeAssessments > 0 {
		return Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrOpenFeeAssessments)
	}
	if !check.LedgerSettled {
		return Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrNotSettled)
	}
	b.CompletedAt = ptr(now)
	return b.transition(ActionComplete, StatusCompleted, actor, now, ""), nil
}

func sorted(a []inventory.AssetID) []inventory.AssetID {
	c := slices.Clone(a)
	slices.Sort(c)
	return c
}

func assetStrings(a []inventory.AssetID) []string {
	out := make([]string, len(a))
	for i, v := range a {
		out[i] = string(v)
	}
	return out
}
