package payments

import "webee_rentor/internal/money"

// PaymentState is the customer-facing payment status derived from the
// ledger. Attempt-level failures stay on individual payments.
type PaymentState string

const (
	StateUnpaid            PaymentState = "UNPAID"
	StatePartiallyPaid     PaymentState = "PARTIALLY_PAID"
	StatePaid              PaymentState = "PAID"
	StateRefundPending     PaymentState = "REFUND_PENDING"
	StatePartiallyRefunded PaymentState = "PARTIALLY_REFUNDED"
	StateRefunded          PaymentState = "REFUNDED"
)

// Summary is the derived financial position of a booking.
type Summary struct {
	Currency money.Currency

	// RentalDue is rent plus fees, excluding reversed charges.
	RentalDue money.Money
	// RentalPaid is succeeded rental portions minus succeeded rental refunds.
	RentalPaid money.Money
	// SecurityApplied is the security deposit approved to cover fees.
	SecurityApplied money.Money
	// RentalCovered = RentalPaid + SecurityApplied + ReservationApplied.
	RentalCovered money.Money
	// RentalOutstanding = RentalDue - RentalCovered. Positive means the
	// customer owes money; negative means an overpayment to refund.
	RentalOutstanding money.Money

	SecurityRequired money.Money
	SecurityReceived money.Money
	SecurityRefunded money.Money
	// SecurityHeld = received - applied - refunded.
	SecurityHeld money.Money

	ReservationRequired money.Money
	ReservationReceived money.Money
	ReservationApplied  money.Money
	ReservationRefunded money.Money
	// ReservationHeld = received - applied - refunded.
	ReservationHeld money.Money
	// ReservationCredit is held reservation deposit that will be deducted
	// from the rent outstanding (zero unless the ledger credits it to rent).
	ReservationCredit money.Money
	// RentalCoveredWithCredit = RentalCovered + ReservationCredit; this is the
	// value to pass to rentals.Funds.RentalCovered.
	RentalCoveredWithCredit money.Money

	PendingPayments   money.Money
	PendingRefunds    money.Money
	PendingDeductions money.Money
	TotalReceived     money.Money
	TotalRefunded     money.Money

	State PaymentState

	// internals for checks
	pendingByBucket       map[Bucket]int64
	pendingRefundByBucket map[Bucket]int64
	creditsRent           bool
}

// collectable is how much more may be requested for a bucket, counting
// pending payments so a second checkout cannot double charge.
func (s Summary) collectable(b Bucket) int64 {
	rental := s.RentalOutstanding.Amount() - s.pendingByBucket[BucketRental] - s.ReservationCredit.Amount()
	switch b {
	case BucketSecurity:
		return s.SecurityRequired.Amount() - s.SecurityReceived.Amount() - s.pendingByBucket[BucketSecurity]
	case BucketReservation:
		v := s.ReservationRequired.Amount() - s.ReservationReceived.Amount() - s.pendingByBucket[BucketReservation]
		if s.creditsRent {
			// A credited reservation is part of the rent; never collect more
			// than the rent still open.
			v = min(v, rental)
		}
		return v
	}
	return rental
}

// securityFree is held deposit not reserved by pending deductions/refunds.
func (s Summary) securityFree() int64 {
	return s.SecurityHeld.Amount() - s.PendingDeductions.Amount() - s.pendingRefundByBucket[BucketSecurity]
}

// refundable is how much may still be refunded from a bucket.
func (s Summary) refundable(b Bucket) int64 {
	switch b {
	case BucketSecurity:
		return s.securityFree()
	case BucketReservation:
		return s.ReservationHeld.Amount() - s.pendingRefundByBucket[BucketReservation]
	}
	return -s.RentalOutstanding.Amount() - s.pendingRefundByBucket[BucketRental]
}

// IsSettled reports whether nothing is owed, nothing is held, and nothing is
// pending — the precondition for completing a booking.
func (s Summary) IsSettled() bool {
	return s.RentalOutstanding.IsZero() && s.SecurityHeld.IsZero() && s.ReservationHeld.IsZero() &&
		s.PendingPayments.IsZero() && s.PendingRefunds.IsZero() && s.PendingDeductions.IsZero()
}

// Summary derives balances from every record.
func (l *Ledger) Summary() Summary {
	required := map[Bucket]int64{}
	for _, c := range l.Charges {
		if c.Reversal == nil {
			required[c.Kind.Bucket()] += c.Amount.Amount()
		}
	}
	received := map[Bucket]int64{}
	pending := map[Bucket]int64{}
	for _, p := range l.Payments {
		for _, pt := range p.Portions {
			switch p.Status {
			case PaymentSucceeded:
				received[pt.Bucket] += pt.Amount.Amount()
			case PaymentPending:
				pending[pt.Bucket] += pt.Amount.Amount()
			}
		}
	}
	refunded := map[Bucket]int64{}
	pendingRefund := map[Bucket]int64{}
	for _, r := range l.Refunds {
		switch {
		case r.Status == RefundSucceeded:
			refunded[r.Bucket] += r.Amount.Amount()
		case r.Status.pending():
			pendingRefund[r.Bucket] += r.Amount.Amount()
		}
	}
	var secApplied, pendingDed int64
	for _, d := range l.Deductions {
		switch d.Status {
		case DeductionApproved:
			secApplied += d.Amount.Amount()
		case DeductionProposed:
			pendingDed += d.Amount.Amount()
		}
	}
	var resApplied int64
	for _, r := range l.Resolutions {
		if r.Kind == ResolutionAppliedToRental {
			resApplied += r.Amount.Amount()
		}
	}

	rentalPaid := received[BucketRental] - refunded[BucketRental]
	covered := rentalPaid + secApplied + resApplied
	outstanding := required[BucketRental] - covered
	resHeld := received[BucketReservation] - resApplied - refunded[BucketReservation]
	var credit int64
	if l.CreditReservationToRent {
		credit = max(min(resHeld-pendingRefund[BucketReservation], outstanding), 0)
	}
	sum := func(m map[Bucket]int64) int64 {
		return m[BucketRental] + m[BucketSecurity] + m[BucketReservation]
	}
	totalReceived, totalRefunded := sum(received), sum(refunded)

	s := Summary{
		Currency:                l.Currency,
		RentalDue:               l.money(required[BucketRental]),
		RentalPaid:              l.money(rentalPaid),
		SecurityApplied:         l.money(secApplied),
		RentalCovered:           l.money(covered),
		RentalOutstanding:       l.money(outstanding),
		SecurityRequired:        l.money(required[BucketSecurity]),
		SecurityReceived:        l.money(received[BucketSecurity]),
		SecurityRefunded:        l.money(refunded[BucketSecurity]),
		SecurityHeld:            l.money(received[BucketSecurity] - secApplied - refunded[BucketSecurity]),
		ReservationRequired:     l.money(required[BucketReservation]),
		ReservationReceived:     l.money(received[BucketReservation]),
		ReservationApplied:      l.money(resApplied),
		ReservationRefunded:     l.money(refunded[BucketReservation]),
		ReservationHeld:         l.money(resHeld),
		ReservationCredit:       l.money(credit),
		RentalCoveredWithCredit: l.money(covered + credit),
		PendingPayments:         l.money(sum(pending)),
		PendingRefunds:          l.money(sum(pendingRefund)),
		PendingDeductions:       l.money(pendingDed),
		TotalReceived:           l.money(totalReceived),
		TotalRefunded:           l.money(totalRefunded),
		pendingByBucket:         pending,
		pendingRefundByBucket:   pendingRefund,
		creditsRent:             l.CreditReservationToRent,
	}
	// A credited reservation is part of the rent, not an extra amount due.
	totalDue := required[BucketRental] + required[BucketSecurity]
	if !l.CreditReservationToRent {
		totalDue += required[BucketReservation]
	}
	switch {
	case s.PendingRefunds.IsPositive():
		s.State = StateRefundPending
	case totalRefunded > 0 && totalRefunded >= totalReceived:
		s.State = StateRefunded
	case totalRefunded > 0:
		s.State = StatePartiallyRefunded
	case totalReceived == 0:
		s.State = StateUnpaid
	case totalReceived < totalDue:
		s.State = StatePartiallyPaid
	default:
		s.State = StatePaid
	}
	return s
}
