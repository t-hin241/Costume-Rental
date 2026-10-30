package rentals

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/delivery"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/money"
)

// FeeKind is a post-rental charge (fee policy §2–4). The use case records an
// approved assessment in the payment ledger as: LATE_RETURN → LATE_FEE,
// CLEANING → CLEANING_FEE, DAMAGE_REPAIR → DAMAGE_FEE, DAMAGE_REPLACEMENT
// and LOST_ITEM → LOSS_FEE, REDELIVERY → REDELIVERY_FEE.
type FeeKind string

const (
	FeeLateReturn        FeeKind = "LATE_RETURN"
	FeeCleaning          FeeKind = "CLEANING"
	FeeDamageRepair      FeeKind = "DAMAGE_REPAIR"
	FeeDamageReplacement FeeKind = "DAMAGE_REPLACEMENT"
	FeeLostItem          FeeKind = "LOST_ITEM"
	// FeeRedelivery is the zone re-attempt fee for a delivery or pickup the
	// customer caused to fail (REDELIVERY_FEE in the ledger).
	FeeRedelivery FeeKind = "REDELIVERY"
)

// AssessmentStatus is the decision state of a fee.
type AssessmentStatus string

const (
	AssessmentProposed   AssessmentStatus = "PROPOSED"
	AssessmentApproved   AssessmentStatus = "APPROVED"
	AssessmentRejected   AssessmentStatus = "REJECTED"
	AssessmentDisputed   AssessmentStatus = "DISPUTED"
	AssessmentWaived     AssessmentStatus = "WAIVED"
	AssessmentSuperseded AssessmentStatus = "SUPERSEDED"
)

// active assessments count toward the no-double-charge rule.
func (s AssessmentStatus) active() bool {
	return s == AssessmentProposed || s == AssessmentApproved || s == AssessmentDisputed
}

type AssessmentID string

var (
	ErrFeeNotAllowed      = domainerr.New(domainerr.KindInvalid, "rentals.fee_not_allowed", "the inspection or item state does not support this fee")
	ErrDuplicateFee       = domainerr.New(domainerr.KindConflict, "rentals.duplicate_fee", "a fee for the same loss is already recorded for this item")
	ErrEvidenceRequired   = domainerr.New(domainerr.KindInvalid, "rentals.evidence_required", "evidence is required for this fee")
	ErrNewDamageUnproven  = domainerr.New(domainerr.KindInvalid, "rentals.new_damage_unproven", "the item had issues at handover; confirm new damage with evidence")
	ErrApproverMustDiffer = domainerr.New(domainerr.KindForbidden, "rentals.approver_must_differ", "fees above the manager threshold need a different approver")
	ErrAdjustmentInvalid  = domainerr.New(domainerr.KindInvalid, "rentals.adjustment_invalid", "an adjusted fee must be positive and lower than the original")
)

// LateFeeQuote is the late-return fee computed for one item.
type LateFeeQuote struct {
	AssetID   inventory.AssetID
	Lateness  time.Duration
	Percent   int
	DailyRate money.Money
	// Amount is Percent of DailyRate, rounded down to the đồng.
	Amount money.Money
	Basis  string
}

// QuoteLateFee computes the late fee for a resolved item from the snapshot
// policy: lateness runs from due_at to the actual return, or to the loss
// confirmation for a lost item (no further accrual). affectedNext says
// whether the late return disrupted the next booking of the asset, which
// voids the grace period.
func (b *Booking) QuoteLateFee(asset inventory.AssetID, affectedNext bool) (LateFeeQuote, error) {
	it, err := b.Item(asset)
	if err != nil {
		return LateFeeQuote{}, err
	}
	var end time.Time
	switch {
	case it.ReturnedAt != nil && !it.NotReceived && !it.CollectByStore:
		end = *it.ReturnedAt
	case it.CarrierLiable || it.NotReceived || it.CollectByStore:
		end = b.Schedule.DueAt // never with the customer, or the store collects it
	case it.LostConfirmedAt != nil:
		end = *it.LostConfirmedAt
	default:
		return LateFeeQuote{}, fmt.Errorf("booking %s asset %s is still out: %w", b.ID, asset, ErrFeeNotAllowed)
	}
	// Lateness caused by the store or the carrier is never the customer's
	// (decision D19): a failed courier pickup the customer was ready for
	// stops the clock, and late outbound delivery moves the due time.
	if r := b.Fulfillment.ReturnReadyAt; r != nil && r.Before(end) {
		end = *r
	}
	due := b.Schedule.DueAt.Add(b.Fulfillment.ServiceDelay(b.Schedule.PickupAt))
	late := max(end.Sub(due), 0)
	pct := b.Policy.LateFee.percentFor(late, affectedNext)
	amount, err := money.New(it.DailyRate.Amount()*int64(pct)/100, it.DailyRate.Currency())
	if err != nil {
		return LateFeeQuote{}, fmt.Errorf("late fee: %w", err)
	}
	basis := fmt.Sprintf("returned %s after due; %d%% of daily rate %s (policy %s)", late, pct, it.DailyRate, b.Policy.Version)
	if affectedNext {
		basis += "; affected next booking"
	}
	return LateFeeQuote{AssetID: asset, Lateness: late, Percent: pct, DailyRate: it.DailyRate, Amount: amount, Basis: basis}, nil
}

// FeeAssessment is one fee for one item, decided once and never edited:
// waivers and adjustments create new states or records.
type FeeAssessment struct {
	ID            AssessmentID
	BookingID     BookingID
	AssetID       inventory.AssetID
	Kind          FeeKind
	Amount        money.Money
	Basis         string
	Evidence      []string
	PolicyVersion string
	Status        AssessmentStatus
	ProposedBy    identity.Actor
	ProposedAt    time.Time
	DecidedBy     *identity.Actor
	DecidedAt     *time.Time
	DecisionNote  string
	DisputeNote   string
	// Ref is the failed delivery attempt a redelivery fee is for.
	Ref string
	// Supersedes/SupersededBy link an adjustment to the original.
	Supersedes   AssessmentID
	SupersededBy AssessmentID
}

// CountOpenAssessments counts fees still awaiting a decision.
func CountOpenAssessments(as []FeeAssessment) int {
	n := 0
	for _, a := range as {
		if a.Status == AssessmentProposed || a.Status == AssessmentDisputed {
			n++
		}
	}
	return n
}

// AssessLateFee records the policy-computed late fee for an item as
// APPROVED: it involves no discretion. A zero fee records nothing.
func (b *Booking) AssessLateFee(existing []FeeAssessment, id AssessmentID, asset inventory.AssetID, affectedNext bool, actor identity.Actor, now time.Time) (FeeAssessment, bool, error) {
	if !actor.Valid() {
		return FeeAssessment{}, false, ErrActorRequired
	}
	if err := b.requireStatus(ActionInspectAsset, StatusRented, StatusReturnInspection); err != nil {
		return FeeAssessment{}, false, err
	}
	q, err := b.QuoteLateFee(asset, affectedNext)
	if err != nil {
		return FeeAssessment{}, false, err
	}
	if err := checkNoDuplicate(existing, asset, FeeLateReturn); err != nil {
		return FeeAssessment{}, false, err
	}
	if !q.Amount.IsPositive() {
		return FeeAssessment{}, false, nil
	}
	a := b.newAssessment(id, asset, FeeLateReturn, q.Amount, q.Basis, nil, actor, now)
	a.Status, a.DecidedBy, a.DecidedAt, a.DecisionNote = AssessmentApproved, &actor, ptr(now), "computed from policy"
	return a, true, nil
}

// ProposeFeeRequest proposes a discretionary fee for one item.
type ProposeFeeRequest struct {
	ID     AssessmentID
	Asset  inventory.AssetID
	Kind   FeeKind
	Amount money.Money
	// Basis cites the public price list entry, quote or receipt, or the
	// replacement valuation.
	Basis    string
	Evidence []string
	// ConfirmedNewDamage is required when the item had issues at handover or
	// the carrier reported damage.
	ConfirmedNewDamage bool
	// Attempt is the failed delivery/pickup attempt for a redelivery fee.
	Attempt *delivery.Attempt
}

// ProposeFee validates a cleaning, repair, replacement or loss fee against
// the inspection finding and the no-double-charge rules (fee policy §3–4).
// Amounts come from staff with a basis; they are never computed from
// inventory status alone.
func (b *Booking) ProposeFee(existing []FeeAssessment, req ProposeFeeRequest, p identity.Principal, now time.Time) (FeeAssessment, error) {
	if err := p.Require(identity.PermFeeAssess); err != nil {
		return FeeAssessment{}, err
	}
	if err := b.requireStatus(ActionInspectAsset, StatusRented, StatusReturnInspection); err != nil {
		return FeeAssessment{}, err
	}
	if req.Kind == FeeRedelivery {
		return b.proposeRedelivery(existing, req, p, now)
	}
	it, err := b.Item(req.Asset)
	if err != nil {
		return FeeAssessment{}, err
	}
	if req.Amount.Currency() != b.Currency || !req.Amount.IsPositive() {
		return FeeAssessment{}, domainerr.Invalid("amount", "must be positive in the booking currency")
	}
	if strings.TrimSpace(req.Basis) == "" {
		return FeeAssessment{}, ErrReasonRequired
	}
	if req.Kind == FeeLateReturn {
		return FeeAssessment{}, domainerr.Invalid("kind", "use AssessLateFee for late returns")
	}
	if err := checkNoDuplicate(existing, req.Asset, req.Kind); err != nil {
		return FeeAssessment{}, err
	}
	switch req.Kind {
	case FeeLostItem:
		if it.LostConfirmedAt == nil || it.CarrierLiable || it.NotReceived {
			return FeeAssessment{}, fmt.Errorf("asset %s is not a customer loss: %w", req.Asset, ErrFeeNotAllowed)
		}
	case FeeCleaning, FeeDamageRepair, FeeDamageReplacement:
		need := map[FeeKind][]Condition{
			FeeCleaning:          {CondAbnormalSoiling},
			FeeDamageRepair:      {CondMinorRepairable, CondMajorRepairable},
			FeeDamageReplacement: {CondIrreparable},
		}[req.Kind]
		if it.Finding == nil || !slices.ContainsFunc(need, it.Finding.has) {
			return FeeAssessment{}, fmt.Errorf("asset %s finding does not support %s: %w", req.Asset, req.Kind, ErrFeeNotAllowed)
		}
		if len(req.Evidence) == 0 && len(it.Finding.Evidence) == 0 {
			return FeeAssessment{}, ErrEvidenceRequired
		}
		if (it.PreexistingIssues != "" || it.CarrierDamageReported || it.ArrivalIssue) && (!req.ConfirmedNewDamage || len(req.Evidence) == 0) {
			return FeeAssessment{}, fmt.Errorf("asset %s: %w", req.Asset, ErrNewDamageUnproven)
		}
	default:
		return FeeAssessment{}, domainerr.Invalid("kind", "unknown fee kind")
	}
	return b.newAssessment(req.ID, req.Asset, req.Kind, req.Amount, req.Basis, req.Evidence, p.Actor(), now), nil
}

// proposeRedelivery charges the snapshot zone re-attempt fee for an attempt
// staff classified as the customer's fault; one fee per attempt.
func (b *Booking) proposeRedelivery(existing []FeeAssessment, req ProposeFeeRequest, p identity.Principal, now time.Time) (FeeAssessment, error) {
	if req.Attempt == nil || req.Attempt.Fault != delivery.FaultCustomer {
		return FeeAssessment{}, fmt.Errorf("redelivery needs a customer-caused failed attempt: %w", ErrFeeNotAllowed)
	}
	if !b.Fulfillment.courier() || !b.Fulfillment.RedeliveryFee.IsPositive() {
		return FeeAssessment{}, fmt.Errorf("booking %s has no redelivery fee: %w", b.ID, ErrFeeNotAllowed)
	}
	for _, e := range existing {
		if e.Kind == FeeRedelivery && e.Ref == req.Attempt.ID && e.Status.active() {
			return FeeAssessment{}, fmt.Errorf("attempt %s: %w", req.Attempt.ID, ErrDuplicateFee)
		}
	}
	basis := req.Basis
	if strings.TrimSpace(basis) == "" {
		basis = "failed attempt " + req.Attempt.ID + ": " + req.Attempt.Reason
	}
	a := b.newAssessment(req.ID, "", FeeRedelivery, b.Fulfillment.RedeliveryFee, basis, req.Evidence, p.Actor(), now)
	a.Ref = req.Attempt.ID
	return a, nil
}

// conflicting kinds charge for the same loss: a repaired item is not also
// replaced, and a replaced or lost item is not also cleaned.
var conflicting = map[FeeKind][]FeeKind{
	FeeLateReturn:        {FeeLateReturn},
	FeeCleaning:          {FeeCleaning, FeeDamageReplacement, FeeLostItem},
	FeeDamageRepair:      {FeeDamageRepair, FeeDamageReplacement, FeeLostItem},
	FeeDamageReplacement: {FeeDamageRepair, FeeDamageReplacement, FeeLostItem, FeeCleaning},
	FeeLostItem:          {FeeDamageRepair, FeeDamageReplacement, FeeLostItem, FeeCleaning},
}

func checkNoDuplicate(existing []FeeAssessment, asset inventory.AssetID, kind FeeKind) error {
	for _, e := range existing {
		if e.AssetID == asset && e.Status.active() && slices.Contains(conflicting[kind], e.Kind) {
			return fmt.Errorf("asset %s %s vs existing %s %s: %w", asset, kind, e.Kind, e.ID, ErrDuplicateFee)
		}
	}
	return nil
}

func (b *Booking) newAssessment(id AssessmentID, asset inventory.AssetID, kind FeeKind, amount money.Money, basis string, evidence []string, actor identity.Actor, now time.Time) FeeAssessment {
	return FeeAssessment{
		ID: id, BookingID: b.ID, AssetID: asset, Kind: kind, Amount: amount, Basis: basis,
		Evidence: slices.Clone(evidence), PolicyVersion: b.Policy.Version,
		Status: AssessmentProposed, ProposedBy: actor, ProposedAt: now.UTC(),
	}
}

// Approve accepts a proposed fee. Above the snapshot manager threshold the
// approver needs fee.approve_high and must not be the proposer.
func (a *FeeAssessment) Approve(b *Booking, p identity.Principal, now time.Time) error {
	if err := p.Require(identity.PermFeeApprove); err != nil {
		return err
	}
	if err := a.require(AssessmentProposed); err != nil {
		return err
	}
	if a.Amount.Amount() > b.Policy.FeeApproval.ManagerApprovalAbove.Amount() {
		if err := p.Require(identity.PermFeeApproveHigh); err != nil {
			return err
		}
		if p.Actor() == a.ProposedBy {
			return ErrApproverMustDiffer
		}
	}
	a.decide(AssessmentApproved, p.Actor(), "", now)
	return nil
}

// Reject declines a proposed fee.
func (a *FeeAssessment) Reject(p identity.Principal, note string, now time.Time) error {
	if err := p.Require(identity.PermFeeApprove); err != nil {
		return err
	}
	if strings.TrimSpace(note) == "" {
		return ErrReasonRequired
	}
	if err := a.require(AssessmentProposed); err != nil {
		return err
	}
	a.decide(AssessmentRejected, p.Actor(), note, now)
	return nil
}

// Dispute records the customer contesting an approved fee; the booking
// cannot complete until it is resolved.
func (a *FeeAssessment) Dispute(p identity.Principal, note string, now time.Time) error {
	if err := p.Require(identity.PermFeeAssess); err != nil {
		return err
	}
	if strings.TrimSpace(note) == "" {
		return ErrReasonRequired
	}
	if err := a.require(AssessmentApproved); err != nil {
		return err
	}
	a.Status, a.DisputeNote = AssessmentDisputed, note
	return nil
}

// UpholdDispute keeps a disputed fee after review.
func (a *FeeAssessment) UpholdDispute(p identity.Principal, note string, now time.Time) error {
	if err := p.Require(identity.PermFeeApprove); err != nil {
		return err
	}
	if strings.TrimSpace(note) == "" {
		return ErrReasonRequired
	}
	if err := a.require(AssessmentDisputed); err != nil {
		return err
	}
	a.decide(AssessmentApproved, p.Actor(), note, now)
	return nil
}

// Waive cancels an approved or disputed fee, e.g. a late return caused by the
// store. The use case reverses the ledger charge.
func (a *FeeAssessment) Waive(p identity.Principal, note string, now time.Time) error {
	if err := p.Require(identity.PermFeeWaive); err != nil {
		return err
	}
	if strings.TrimSpace(note) == "" {
		return ErrReasonRequired
	}
	if err := a.require(AssessmentApproved, AssessmentDisputed); err != nil {
		return err
	}
	a.decide(AssessmentWaived, p.Actor(), note, now)
	return nil
}

// Adjust lowers an approved or disputed fee by superseding it with a new
// approved assessment; the use case reverses the old charge and records the
// new one. Increases need a new proposal and approval.
func (a *FeeAssessment) Adjust(newID AssessmentID, amount money.Money, p identity.Principal, note string, now time.Time) (FeeAssessment, error) {
	if err := p.Require(identity.PermFeeWaive); err != nil {
		return FeeAssessment{}, err
	}
	if strings.TrimSpace(note) == "" {
		return FeeAssessment{}, ErrReasonRequired
	}
	if err := a.require(AssessmentApproved, AssessmentDisputed); err != nil {
		return FeeAssessment{}, err
	}
	if amount.Currency() != a.Amount.Currency() || !amount.IsPositive() || amount.Amount() >= a.Amount.Amount() {
		return FeeAssessment{}, ErrAdjustmentInvalid
	}
	if newID == "" || newID == a.ID {
		return FeeAssessment{}, domainerr.Invalid("id", "a new assessment id is required")
	}
	actor := p.Actor()
	n := *a
	n.ID, n.Amount, n.Supersedes, n.SupersededBy, n.DisputeNote = newID, amount, a.ID, "", ""
	n.Evidence = slices.Clone(a.Evidence)
	n.Status, n.DecidedBy, n.DecidedAt, n.DecisionNote = AssessmentApproved, &actor, ptr(now), note
	n.ProposedBy, n.ProposedAt = actor, now.UTC()
	a.decide(AssessmentSuperseded, actor, note, now)
	a.SupersededBy = newID
	return n, nil
}

func (a *FeeAssessment) require(allowed ...AssessmentStatus) error {
	if !slices.Contains(allowed, a.Status) {
		return fmt.Errorf("fee %s from %s: %w", a.ID, a.Status, ErrInvalidTransition)
	}
	return nil
}

func (a *FeeAssessment) decide(to AssessmentStatus, actor identity.Actor, note string, now time.Time) {
	a.Status, a.DecidedBy, a.DecidedAt, a.DecisionNote = to, &actor, ptr(now), note
}
