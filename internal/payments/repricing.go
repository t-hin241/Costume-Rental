package payments

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
)

// Repricing is a booking modification's new rent and security deposit,
// staged while the customer accepts or pays the increase.
type Repricing struct {
	Ref           string
	NewRent       money.Money
	NewSecurity   money.Money
	RentDelta     money.Money
	SecurityDelta money.Money
	StagedBy      identity.Actor
	StagedAt      time.Time
}

var ErrRepricingConflict = domainerr.New(domainerr.KindConflict, "payments.repricing_conflict", "another booking modification is already staged")

// StageRepricing records the quoted amounts of a modification. Payments
// tagged with the ref may then collect the increase before anything else
// changes; the current charges stay as they are.
func (l *Ledger) StageRepricing(ref string, newRent, newSecurity money.Money, actor identity.Actor, now time.Time) (Repricing, error) {
	if strings.TrimSpace(ref) == "" {
		return Repricing{}, domainerr.Invalid("ref", "required")
	}
	if !actor.Valid() {
		return Repricing{}, ErrActorRequired
	}
	if l.Staged != nil {
		if l.Staged.Ref == ref && l.Staged.NewRent.Equal(newRent) && l.Staged.NewSecurity.Equal(newSecurity) {
			return *l.Staged, nil
		}
		return Repricing{}, ErrRepricingConflict
	}
	if slices.Contains(l.CommittedRepricings, ref) {
		return Repricing{}, fmt.Errorf("repricing %s already committed: %w", ref, ErrInvalidTransition)
	}
	if newRent.Currency() != l.Currency || !newRent.IsPositive() || newSecurity.Currency() != l.Currency || newSecurity.IsNegative() {
		return Repricing{}, ErrInvalidAmount
	}
	if newRent.Amount()+newSecurity.Amount() > money.MaxAmount-l.volume() {
		return Repricing{}, fmt.Errorf("ledger %s volume: %w", l.BookingRef, money.ErrOutOfRange)
	}
	rent, sec := l.activeCharge(ChargeRent), l.activeCharge(ChargeSecurityDeposit)
	r := Repricing{
		Ref: ref, NewRent: newRent, NewSecurity: newSecurity,
		RentDelta: l.money(newRent.Amount() - rent), SecurityDelta: l.money(newSecurity.Amount() - sec),
		StagedBy: actor, StagedAt: now.UTC(),
	}
	l.Staged = &r
	return r, nil
}

// PaidForModification sums succeeded payments tagged with ref.
func (l *Ledger) PaidForModification(ref string) money.Money {
	var v int64
	for _, p := range l.Payments {
		if p.ModificationRef == ref && p.Status == PaymentSucceeded {
			v += p.Amount.Amount()
		}
	}
	return l.money(v)
}

// CommitRepricing replaces the rent and security charges with the staged
// amounts (the old charges are reversed, not edited). Any overpayment then
// shows as refundable per bucket. Committing again is a no-op.
func (l *Ledger) CommitRepricing(ref string, rentChargeID, securityChargeID ChargeID, actor identity.Actor, now time.Time) error {
	if slices.Contains(l.CommittedRepricings, ref) {
		return nil
	}
	if l.Staged == nil || l.Staged.Ref != ref {
		return fmt.Errorf("repricing %s is not staged: %w", ref, ErrInvalidTransition)
	}
	if !actor.Valid() {
		return ErrActorRequired
	}
	r := *l.Staged
	for _, id := range []ChargeID{rentChargeID, securityChargeID} {
		if id == "" || slices.ContainsFunc(l.Charges, func(c Charge) bool { return c.ID == id }) {
			return domainerr.Invalid("charge_id", "required and unique")
		}
	}
	reason := "booking modification " + ref
	for i := range l.Charges {
		c := &l.Charges[i]
		if (c.Kind == ChargeRent || c.Kind == ChargeSecurityDeposit) && c.Reversal == nil {
			c.Reversal = &Reversal{Reason: reason, By: actor, At: now.UTC()}
		}
	}
	l.Charges = append(l.Charges, Charge{ID: rentChargeID, Kind: ChargeRent, Amount: r.NewRent, Reason: reason, CreatedBy: actor, CreatedAt: now.UTC()})
	if r.NewSecurity.IsPositive() {
		l.Charges = append(l.Charges, Charge{ID: securityChargeID, Kind: ChargeSecurityDeposit, Amount: r.NewSecurity, Reason: reason, CreatedBy: actor, CreatedAt: now.UTC()})
	}
	l.Staged = nil
	l.CommittedRepricings = append(l.CommittedRepricings, ref)
	return nil
}

// DiscardRepricing drops a staged repricing when its modification is
// rejected, cancelled or expires. Money already paid for it becomes
// refundable because the charges never changed. Idempotent.
func (l *Ledger) DiscardRepricing(ref string) {
	if l.Staged != nil && l.Staged.Ref == ref {
		l.Staged = nil
	}
}

func (l *Ledger) activeCharge(kind ChargeKind) int64 {
	var v int64
	for _, c := range l.Charges {
		if c.Kind == kind && c.Reversal == nil {
			v += c.Amount.Amount()
		}
	}
	return v
}

// collectableForModification caps a tagged payment portion at the staged
// increase of its bucket minus what was already collected or is pending for
// the same modification.
func (l *Ledger) collectableForModification(ref string, b Bucket) (int64, error) {
	if l.Staged == nil || l.Staged.Ref != ref {
		return 0, fmt.Errorf("repricing %s is not staged: %w", ref, ErrInvalidTransition)
	}
	var increase int64
	switch b {
	case BucketRental:
		increase = l.Staged.RentDelta.Amount()
	case BucketSecurity:
		increase = l.Staged.SecurityDelta.Amount()
	}
	var taken int64
	for _, p := range l.Payments {
		if p.ModificationRef == ref && (p.Status == PaymentSucceeded || p.Status == PaymentPending) {
			for _, pt := range p.Portions {
				if pt.Bucket == b {
					taken += pt.Amount.Amount()
				}
			}
		}
	}
	return max(increase, 0) - taken, nil
}
