package rentals

import (
	"slices"
	"time"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
)

// BookingAction names what a user can do next, for the client to show. The
// methods that carry out each action still check everything themselves.
type BookingAction string

const (
	DoConfirm          BookingAction = "confirm"
	DoStartPreparation BookingAction = "start_preparation"
	DoHandover         BookingAction = "handover"
	DoRecordReturn     BookingAction = "record_return"
	DoReportMissing    BookingAction = "report_missing"
	DoConfirmLost      BookingAction = "confirm_lost"
	DoInspect          BookingAction = "inspect"
	DoProposeFee       BookingAction = "propose_fee"
	DoRecordPayment    BookingAction = "record_payment"
	DoSettle           BookingAction = "settle"
	DoComplete         BookingAction = "complete"
	DoCancel           BookingAction = "cancel"
	DoNoShow           BookingAction = "no_show"
	DoReschedule       BookingAction = "reschedule"
)

// ActionFacts are the money and fee facts the actions depend on.
type ActionFacts struct {
	// MoneyOwed: the ledger can still collect something.
	MoneyOwed bool
	// MoneyToClose: settling would apply the deposit or refund something.
	MoneyToClose bool
	// Settled: nothing is owed, held or pending.
	Settled  bool
	OpenFees int
}

// Actions lists what p may do with the booking right now, using the same
// states and permissions as the methods themselves.
func (b *Booking) Actions(p identity.Principal, f ActionFacts, now time.Time) []BookingAction {
	var out []BookingAction
	add := func(a BookingAction, ok bool) {
		if ok {
			out = append(out, a)
		}
	}
	can := func(a Action) bool {
		_, err := Authorize(p, a, b)
		return err == nil
	}
	free := b.OpenModification == ""
	beforeHandover := slices.Contains([]Status{StatusPendingConfirmation, StatusConfirmed, StatusPreparing}, b.Status)
	noShowAt := b.Schedule.PickupAt.Add(b.Policy.Cancellation.NoShowAfter)
	holdLive := b.Status != StatusPendingConfirmation || now.Before(b.HoldExpiresAt)
	allocated := b.Status == StatusRented || b.Status == StatusReturnInspection

	add(DoConfirm, b.Status == StatusPendingConfirmation && holdLive && can(ActionConfirm))
	add(DoStartPreparation, b.Status == StatusConfirmed && can(ActionStartPreparation))
	add(DoHandover, b.Status == StatusPreparing && free && now.Before(b.Schedule.DueAt) && can(ActionHandover))
	add(DoRecordReturn, b.Status == StatusRented && slices.ContainsFunc(b.Items, func(i RentedItem) bool { return !i.Resolved() }) && can(ActionReturn))
	add(DoReportMissing, b.Status == StatusRented && slices.ContainsFunc(b.Items, reportable) && can(ActionReportMissing))
	add(DoConfirmLost, b.Status == StatusRented && slices.ContainsFunc(b.Items, b.lossConfirmable(now)) && can(ActionConfirmLost))
	add(DoInspect, allocated && slices.ContainsFunc(b.Items, func(i RentedItem) bool { return i.ReceivedAt != nil }) && can(ActionInspectAsset))
	add(DoProposeFee, allocated && p.Can(identity.PermFeeAssess))
	add(DoRecordPayment, f.MoneyOwed && !slices.Contains([]Status{StatusExpired}, b.Status) && p.Can(identity.PermPaymentRecord))
	inspected := b.Status == StatusReturnInspection && b.InspectionCompletedAt != nil && f.OpenFees == 0
	add(DoSettle, inspected && f.MoneyToClose && p.Can(identity.PermRefundCreate))
	add(DoComplete, inspected && f.Settled && can(ActionComplete))
	if scope, err := Authorize(p, ActionCancel, b); err == nil && beforeHandover {
		add(DoCancel, now.Before(noShowAt))
		add(DoNoShow, scope == identity.ScopeAny && !now.Before(noShowAt))
	}
	if scope, err := Authorize(p, ActionReschedule, b); err == nil && free && holdLive &&
		(b.Status == StatusPendingConfirmation || b.Status == StatusConfirmed) {
		add(DoReschedule, scope == identity.ScopeAny || now.Before(b.Schedule.PickupAt.Add(-b.Policy.CustomerRescheduleCutoff)))
	}
	return out
}

func reportable(i RentedItem) bool {
	return !i.Resolved() && i.ReturnedAt == nil && i.MissingReportedAt == nil
}

func (b *Booking) lossConfirmable(now time.Time) func(RentedItem) bool {
	return func(i RentedItem) bool {
		return i.MissingPending() && !now.Before(i.MissingReportedAt.Add(b.Policy.Loss.VerificationWindow))
	}
}

// FeeAction names what a user can do with a fee.
type FeeAction string

const (
	FeeDoApprove FeeAction = "approve"
	FeeDoReject  FeeAction = "reject"
	FeeDoWaive   FeeAction = "waive"
)

// Actions lists what p may decide about the fee now.
func (a FeeAssessment) Actions(p identity.Principal, approvalLimit money.Money) []FeeAction {
	var out []FeeAction
	switch a.Status {
	case AssessmentProposed:
		if p.Can(identity.PermFeeApprove) {
			if a.Amount.Amount() <= approvalLimit.Amount() || p.Can(identity.PermFeeApproveHigh) {
				out = append(out, FeeDoApprove)
			}
			out = append(out, FeeDoReject)
		}
	case AssessmentApproved, AssessmentDisputed:
		if p.Can(identity.PermFeeWaive) {
			out = append(out, FeeDoWaive)
		}
	}
	return out
}

// PaymentTerms are the amounts a customer has to reach at each step, derived
// from the booking's snapshot policy. Clients show them; they never add up
// amounts themselves.
type PaymentTerms struct {
	// DueToConfirm must be received for the store to hold the schedule.
	DueToConfirm money.Money
	// DueBeforeHandover must be received before the goods change hands.
	DueBeforeHandover money.Money
	// Refundable is the part of DueBeforeHandover that comes back after return.
	Refundable money.Money
}

func (r FundsRequirement) due(a Amounts) int64 {
	switch r.Kind {
	case FundsReservationDeposit:
		return a.Reservation.Amount()
	case FundsFullRent:
		return a.Rent.Amount() + a.Delivery.Amount()
	case FundsFullRentAndSecurity:
		return a.Rent.Amount() + a.Delivery.Amount() + a.Security.Amount()
	}
	return 0
}

// PaymentTerms computes the amounts for this booking.
func (b *Booking) PaymentTerms() PaymentTerms {
	a := b.amounts()
	handover, refundable := b.Policy.Handover.due(a), int64(0)
	if b.Policy.Handover.Kind == FundsFullRentAndSecurity {
		refundable = a.Security.Amount()
	}
	if b.ReservationDeposit.IsPositive() && !b.ReservationCreditsRent() && b.Policy.Handover.Kind != FundsReservationDeposit {
		// A separate guarantee is paid on top of the rent and comes back.
		handover += a.Reservation.Amount()
		refundable += a.Reservation.Amount()
	}
	v := func(n int64) money.Money {
		m, _ := money.New(n, b.Currency) // sums of validated snapshot amounts
		return m
	}
	return PaymentTerms{DueToConfirm: v(b.Policy.Confirmation.Funds.due(a)), DueBeforeHandover: v(handover), Refundable: v(refundable)}
}

// ItemCount is how many physical units the booking covers.
func (b *Booking) ItemCount() int {
	n := 0
	for _, l := range b.Lines {
		for _, c := range l.Components {
			n += c.Quantity
		}
	}
	return n
}
