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

// Cancellation records that the booking's charges were settled after a
// cancellation or expiry.
type Cancellation struct {
	FeeChargeID        ChargeID
	Fee                money.Money
	ReservationApplied money.Money
	Basis              string
	By                 identity.Actor
	At                 time.Time
}

// CancellationRequest carries the fee computed by
// rentals.Booking.CancellationFee.
type CancellationRequest struct {
	Fee         money.Money
	Basis       string
	FeeChargeID ChargeID
	// ResolutionID names the record if reservation deposit covers the fee.
	ResolutionID ResolutionID
	// DispatchedLegs are courier legs already handed to the carrier; their
	// fees stay. Fees of legs not yet dispatched are waived (decision D19).
	DispatchedLegs []DeliveryLeg
	Actor          identity.Actor
	Now            time.Time
}

// CancellationResult tells the use case what to refund and what is owed.
type CancellationResult struct {
	Fee                money.Money
	ReservationApplied money.Money
	// Owed is fee still unpaid. It is requested from the customer, never
	// charged automatically; finance may waive it by reversing the fee.
	Owed money.Money
	// Refundable amounts per bucket, to request with RequestRefund (each
	// goes through approval like any other refund).
	RefundableRental      money.Money
	RefundableReservation money.Money
	RefundableSecurity    money.Money
}

var ErrAlreadyCancelled = domainerr.New(domainerr.KindInvalidState, "payments.already_cancelled", "the ledger was already settled for a cancellation with a different fee")

// SettleCancellation closes the booking's obligations after a cancellation
// (owner decisions D8 and D5): rent, security deposit and reservation deposit
// are no longer owed; the cancellation fee — a share of the rent only — is
// charged; held reservation deposit covers the fee before any prepaid rent; everything else
// paid becomes refundable. The security deposit is never used for the fee.
// Booking-time delivery fees of legs not yet dispatched are waived; other
// delivery fees are left for finance to decide (owner decisions D17, D19).
// Repeating the call with the same fee is a no-op (created=false).
func (l *Ledger) SettleCancellation(req CancellationRequest) (CancellationResult, bool, error) {
	if l.Cancellation != nil {
		if !l.Cancellation.Fee.Equal(req.Fee) {
			return CancellationResult{}, false, ErrAlreadyCancelled
		}
		return l.cancellationResult(), false, nil
	}
	if !req.Actor.Valid() {
		return CancellationResult{}, false, ErrActorRequired
	}
	if strings.TrimSpace(req.Basis) == "" {
		return CancellationResult{}, false, ErrReasonRequired
	}
	if req.Fee.Currency() != l.Currency || req.Fee.IsNegative() {
		return CancellationResult{}, false, ErrInvalidAmount
	}
	if req.Fee.IsPositive() {
		if err := l.checkAmount(req.Fee); err != nil {
			return CancellationResult{}, false, err
		}
		if req.FeeChargeID == "" || slices.ContainsFunc(l.Charges, func(c Charge) bool { return c.ID == req.FeeChargeID }) {
			return CancellationResult{}, false, domainerr.Invalid("fee_charge_id", "required and unique")
		}
	}

	reason := "booking cancelled: " + req.Basis
	for i := range l.Charges {
		c := &l.Charges[i]
		switch c.Kind {
		case ChargeRent, ChargeSecurityDeposit, ChargeReservationDeposit:
			if c.Reversal == nil {
				c.Reversal = &Reversal{Reason: reason, By: req.Actor, At: req.Now.UTC()}
			}
		case ChargeDeliveryFee:
			if c.DeliveryLeg != "" && c.Reversal == nil && !slices.Contains(req.DispatchedLegs, c.DeliveryLeg) {
				c.Reversal = &Reversal{Reason: reason + " (leg not dispatched)", By: req.Actor, At: req.Now.UTC()}
			}
		}
	}
	if req.Fee.IsPositive() {
		l.Charges = append(l.Charges, Charge{
			ID: req.FeeChargeID, Kind: ChargeCancellationFee, Amount: req.Fee,
			Reason: req.Basis, CreatedBy: req.Actor, CreatedAt: req.Now.UTC(),
		})
	}
	// The reservation deposit covers the fee first, even if rent was also
	// prepaid; prepaid rent then becomes refundable.
	applied, _, err := l.applyReservation(req.ResolutionID, req.Fee.Amount(), "covers cancellation fee", req.Actor, req.Now)
	if err != nil {
		return CancellationResult{}, false, fmt.Errorf("apply reservation to fee: %w", err)
	}
	l.Cancellation = &Cancellation{
		FeeChargeID: req.FeeChargeID, Fee: req.Fee, ReservationApplied: l.money(applied.Amount.Amount()),
		Basis: req.Basis, By: req.Actor, At: req.Now.UTC(),
	}
	return l.cancellationResult(), true, nil
}

func (l *Ledger) cancellationResult() CancellationResult {
	s := l.Summary()
	return CancellationResult{
		Fee:                   l.Cancellation.Fee,
		ReservationApplied:    l.Cancellation.ReservationApplied,
		Owed:                  l.money(max(s.RentalOutstanding.Amount(), 0)),
		RefundableRental:      l.money(max(s.refundable(BucketRental), 0)),
		RefundableReservation: l.money(max(s.refundable(BucketReservation), 0)),
		RefundableSecurity:    l.money(max(s.refundable(BucketSecurity), 0)),
	}
}
