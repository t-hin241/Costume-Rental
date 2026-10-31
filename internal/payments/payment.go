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

// Method is how money was or will be collected. Card data never reaches the
// system: gateway payments use hosted checkout/tokenization.
type Method string

const (
	MethodCash         Method = "CASH"
	MethodBankTransfer Method = "BANK_TRANSFER"
	MethodGateway      Method = "GATEWAY"
)

// PaymentStatus is the state of one collection attempt.
type PaymentStatus string

const (
	PaymentPending   PaymentStatus = "PENDING"
	PaymentSucceeded PaymentStatus = "SUCCEEDED"
	PaymentFailed    PaymentStatus = "FAILED"
	// PaymentExpired: we stopped waiting. A later provider success is still
	// recorded because the money really arrived.
	PaymentExpired PaymentStatus = "EXPIRED"
)

// Portion assigns part of a payment to a bucket, decided by the backend when
// the payment is created.
type Portion struct {
	Bucket Bucket
	Amount money.Money
}

// Payment is one collection attempt.
type Payment struct {
	ID             PaymentID
	IdempotencyKey string
	Method         Method
	Provider       string
	ProviderTxnID  string
	Amount         money.Money
	Portions       []Portion
	Status         PaymentStatus
	CreatedBy      identity.Actor
	CreatedAt      time.Time
	SettledAt      *time.Time
	FailureReason  string
	// NeedsReview flags a success that arrived after we had given up
	// (expired/failed) or after the booking was cancelled, which may mean an
	// overpayment to refund.
	NeedsReview bool
	// ModificationRef tags a payment collecting a booking modification's
	// increase.
	ModificationRef string
}

// NewPaymentRequest describes a payment to create.
type NewPaymentRequest struct {
	ID             PaymentID
	IdempotencyKey string
	Method         Method
	// Provider is required for gateway payments.
	Provider string
	Portions []Portion
	// ModificationRef collects the staged increase of a booking modification
	// instead of the current outstanding amount.
	ModificationRef string
	Actor           identity.Actor
	Now             time.Time
}

// StartGatewayPayment creates a PENDING gateway payment. Reusing the
// idempotency key with identical parameters returns the existing payment
// (created=false), so a retried checkout never charges twice.
func (l *Ledger) StartGatewayPayment(req NewPaymentRequest) (Payment, bool, error) {
	if req.Method != MethodGateway || strings.TrimSpace(req.Provider) == "" {
		return Payment{}, false, domainerr.Invalid("method", "gateway payments need a provider")
	}
	if !req.Actor.Valid() {
		return Payment{}, false, ErrActorRequired
	}
	return l.addPayment(req, PaymentPending)
}

// RecordCounterPayment records cash or bank transfer verified by staff.
func (l *Ledger) RecordCounterPayment(req NewPaymentRequest) (Payment, bool, error) {
	if req.Method != MethodCash && req.Method != MethodBankTransfer {
		return Payment{}, false, domainerr.Invalid("method", "counter payments are cash or bank transfer")
	}
	if !req.Actor.IsUser() {
		return Payment{}, false, ErrActorRequired
	}
	return l.addPayment(req, PaymentSucceeded)
}

func (l *Ledger) addPayment(req NewPaymentRequest, status PaymentStatus) (Payment, bool, error) {
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return Payment{}, false, domainerr.Invalid("idempotency_key", "required")
	}
	if i := slices.IndexFunc(l.Payments, func(p Payment) bool { return p.IdempotencyKey == req.IdempotencyKey }); i >= 0 {
		existing := l.Payments[i]
		if existing.Method != req.Method || existing.Provider != req.Provider || !slices.Equal(existing.Portions, req.Portions) {
			return Payment{}, false, ErrIdempotencyConflict
		}
		return existing, false, nil
	}
	if req.ID == "" || slices.ContainsFunc(l.Payments, func(p Payment) bool { return p.ID == req.ID }) {
		return Payment{}, false, domainerr.Invalid("payment_id", "required and unique")
	}
	if len(req.Portions) == 0 {
		return Payment{}, false, domainerr.Invalid("portions", "at least one portion is required")
	}
	s := l.Summary()
	var total, rentClaimed int64
	seen := map[Bucket]bool{}
	for _, pt := range req.Portions {
		if !pt.Bucket.valid() || seen[pt.Bucket] {
			return Payment{}, false, domainerr.Invalid("portions", "buckets must be valid and unique")
		}
		seen[pt.Bucket] = true
		if err := l.checkAmount(pt.Amount); err != nil {
			return Payment{}, false, err
		}
		limit := s.collectable(pt.Bucket)
		switch {
		case req.ModificationRef != "":
			var err error
			if limit, err = l.collectableForModification(req.ModificationRef, pt.Bucket); err != nil {
				return Payment{}, false, err
			}
		case s.sharesRentLimit(pt.Bucket):
			// Portions of one payment share the same rent still open, in
			// whatever order they are listed.
			limit = min(limit, s.collectable(BucketRental)-rentClaimed)
			rentClaimed += pt.Amount.Amount()
		}
		if pt.Amount.Amount() > limit {
			return Payment{}, false, fmt.Errorf("%s portion %s: %w", pt.Bucket, pt.Amount, ErrExceedsOutstanding)
		}
		total += pt.Amount.Amount()
	}
	p := Payment{
		ID: req.ID, IdempotencyKey: req.IdempotencyKey, Method: req.Method, Provider: req.Provider,
		Amount: l.money(total), Portions: slices.Clone(req.Portions), Status: status,
		CreatedBy: req.Actor, CreatedAt: req.Now.UTC(), ModificationRef: req.ModificationRef,
	}
	if status == PaymentSucceeded {
		p.SettledAt = ptr(req.Now)
	}
	l.Payments = append(l.Payments, p)
	return p, true, nil
}

// ExpirePayment stops waiting for a pending payment. Idempotent.
func (l *Ledger) ExpirePayment(id PaymentID, now time.Time) (bool, error) {
	p, err := l.payment(id)
	if err != nil {
		return false, err
	}
	switch p.Status {
	case PaymentExpired, PaymentFailed:
		return false, nil
	case PaymentSucceeded:
		return false, fmt.Errorf("payment %s: %w", id, ErrInvalidTransition)
	}
	p.Status, p.SettledAt = PaymentExpired, ptr(now)
	return true, nil
}

// ExpirePendingPayments stops waiting for every pending payment, as when the
// booking ended. A later success is still recorded, flagged for review.
func (l *Ledger) ExpirePendingPayments(now time.Time) {
	for i := range l.Payments {
		if p := &l.Payments[i]; p.Status == PaymentPending {
			p.Status, p.SettledAt = PaymentExpired, ptr(now)
		}
	}
}

// EventStatus is the provider-reported outcome.
type EventStatus string

const (
	EventSucceeded EventStatus = "SUCCEEDED"
	EventFailed    EventStatus = "FAILED"
)

// PaymentEvent is a provider notification whose signature, timestamp and
// replay window the adapter has already verified.
type PaymentEvent struct {
	Provider      string
	EventID       string
	PaymentID     PaymentID // our reference sent to the provider
	ProviderTxnID string
	Amount        money.Money
	Status        EventStatus
	FailureReason string
	OccurredAt    time.Time
}

// Outcome reports what applying an event did.
type Outcome string

const (
	OutcomeApplied Outcome = "APPLIED"
	// OutcomeAppliedNeedsReview: success after expiry/failure or on a cancelled
	// booking; finance must review and refund any overpayment.
	OutcomeAppliedNeedsReview Outcome = "APPLIED_NEEDS_REVIEW"
	OutcomeDuplicate          Outcome = "DUPLICATE"
	OutcomeNoChange           Outcome = "NO_CHANGE"
)

func eventKey(provider, id string) string { return provider + ":" + id }

// ApplyPaymentEvent updates a payment from a verified provider event. It
// checks reference, provider, amount and currency before changing anything;
// a mismatch returns an error and changes nothing so it can be alerted on.
func (l *Ledger) ApplyPaymentEvent(e PaymentEvent) (Outcome, error) {
	if e.Provider == "" || e.EventID == "" {
		return "", domainerr.Invalid("event", "provider and event id are required")
	}
	key := eventKey(e.Provider, e.EventID)
	if l.ProcessedEvents[key] {
		return OutcomeDuplicate, nil
	}
	p, err := l.payment(e.PaymentID)
	if err != nil {
		return "", err
	}
	if p.Method != MethodGateway || p.Provider != e.Provider || !p.Amount.Equal(e.Amount) {
		return "", fmt.Errorf("payment %s event %s: %w", p.ID, e.EventID, ErrProviderMismatch)
	}
	if e.Status == EventSucceeded && e.ProviderTxnID == "" {
		return "", domainerr.Invalid("provider_txn_id", "required for success")
	}
	outcome := OutcomeApplied
	switch e.Status {
	case EventSucceeded:
		switch p.Status {
		case PaymentSucceeded:
			if p.ProviderTxnID != e.ProviderTxnID {
				return "", fmt.Errorf("payment %s txn %s: %w", p.ID, e.ProviderTxnID, ErrProviderMismatch)
			}
			outcome = OutcomeNoChange
		case PaymentExpired, PaymentFailed, PaymentPending:
			// Money for a cancelled booking is never an ordinary success: the
			// booking must not resume, and finance has to refund it.
			if p.Status != PaymentPending || l.Cancellation != nil {
				outcome = OutcomeAppliedNeedsReview
				p.NeedsReview = true
			}
			p.Status, p.ProviderTxnID, p.SettledAt, p.FailureReason = PaymentSucceeded, e.ProviderTxnID, ptr(e.OccurredAt), ""
		}
	case EventFailed:
		switch p.Status {
		case PaymentSucceeded:
			// Reversals/chargebacks need a reconciliation workflow.
			return "", fmt.Errorf("payment %s failed after success: %w", p.ID, ErrInvalidTransition)
		case PaymentFailed, PaymentExpired:
			outcome = OutcomeNoChange
		case PaymentPending:
			p.Status, p.SettledAt, p.FailureReason = PaymentFailed, ptr(e.OccurredAt), e.FailureReason
		}
	default:
		return "", domainerr.Invalid("status", "unknown event status")
	}
	l.ProcessedEvents[key] = true
	return outcome, nil
}

func (l *Ledger) payment(id PaymentID) (*Payment, error) {
	i := slices.IndexFunc(l.Payments, func(p Payment) bool { return p.ID == id })
	if i < 0 {
		return nil, fmt.Errorf("payment %s: %w", id, ErrNotFound)
	}
	return &l.Payments[i], nil
}

func ptr(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}
