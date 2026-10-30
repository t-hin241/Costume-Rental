package payments

import (
	"slices"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
)

type ResolutionID string

// ResolutionKind is what happened to (part of) the reservation deposit.
// Refunds of the reservation deposit use the normal refund flow with
// BucketReservation.
type ResolutionKind string

// ResolutionAppliedToRental moves reservation deposit into the rental bucket
// to pay rent (at handover) or a cancellation fee (on cancellation).
const ResolutionAppliedToRental ResolutionKind = "APPLIED_TO_RENTAL"

// ReservationResolution moves reservation deposit out of the held balance
// without paying it back.
type ReservationResolution struct {
	ID        ResolutionID
	Kind      ResolutionKind
	Amount    money.Money
	Reason    string
	CreatedBy identity.Actor
	CreatedAt time.Time
}

var ErrNotCredited = domainerr.New(domainerr.KindInvalidState, "payments.reservation_not_credited", "this booking's reservation deposit is not deducted from rent")

// ApplyReservationToRent deducts the held reservation deposit from the rent
// still owed. The use case calls it at handover, before checking handover
// funds (owner decision D17). A reservation deposit kept as a separate
// guarantee is never used for late, damage or loss fees; only the security
// deposit is. It applies at most the rent outstanding; nothing to apply is a
// no-op (applied=false), so retries are safe.
func (l *Ledger) ApplyReservationToRent(id ResolutionID, actor identity.Actor, now time.Time) (ReservationResolution, bool, error) {
	if !l.CreditReservationToRent {
		return ReservationResolution{}, false, ErrNotCredited
	}
	if !actor.Valid() {
		return ReservationResolution{}, false, ErrActorRequired
	}
	return l.applyReservation(id, l.Summary().RentalOutstanding.Amount(), "applied to rent at handover", actor, now)
}

// applyReservation moves up to limit of the free reservation deposit into
// the rental bucket.
func (l *Ledger) applyReservation(id ResolutionID, limit int64, reason string, actor identity.Actor, now time.Time) (ReservationResolution, bool, error) {
	amount := min(l.Summary().refundable(BucketReservation), limit)
	if amount <= 0 {
		return ReservationResolution{}, false, nil
	}
	if id == "" || slices.ContainsFunc(l.Resolutions, func(r ReservationResolution) bool { return r.ID == id }) {
		return ReservationResolution{}, false, domainerr.Invalid("resolution_id", "required and unique")
	}
	r := ReservationResolution{ID: id, Kind: ResolutionAppliedToRental, Amount: l.money(amount), Reason: reason, CreatedBy: actor, CreatedAt: now.UTC()}
	l.Resolutions = append(l.Resolutions, r)
	return r, true, nil
}
