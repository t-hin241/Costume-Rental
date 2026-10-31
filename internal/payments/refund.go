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

// RefundStatus is the refund lifecycle. Only provider (or counter staff)
// confirmation makes a refund SUCCEEDED.
type RefundStatus string

const (
	RefundRequested  RefundStatus = "REQUESTED"
	RefundApproved   RefundStatus = "APPROVED"
	RefundRejected   RefundStatus = "REJECTED"
	RefundProcessing RefundStatus = "PROCESSING"
	RefundSucceeded  RefundStatus = "SUCCEEDED"
	RefundFailed     RefundStatus = "FAILED"
)

func (s RefundStatus) pending() bool {
	return s == RefundRequested || s == RefundApproved || s == RefundProcessing
}

// Refund returns money from one bucket. Partial refunds are separate records.
type Refund struct {
	ID             RefundID
	IdempotencyKey string
	Bucket         Bucket
	Amount         money.Money
	Reason         string
	Status         RefundStatus
	RequestedBy    identity.Actor
	RequestedAt    time.Time
	DecidedBy      *identity.Actor
	DecidedAt      *time.Time
	DecisionNote   string
	Provider       string
	ProviderRef    string
	// CompletedAt is when the money actually moved, which for a counter
	// refund can be before it was entered.
	CompletedAt   *time.Time
	FailureReason string
	// Counter refunds keep how the money went back and who entered it.
	Method     Method
	Reference  string
	RecordedBy *identity.Actor
	RecordedAt *time.Time
}

// RequestRefund creates a refund awaiting approval. The amount is reserved
// immediately so concurrent requests cannot over-refund. A system actor may
// request (the hold-expiry job does); approval always takes a user. While a
// repricing is staged no refund starts: money collected for it must stay put
// until the modification is confirmed or discarded.
func (l *Ledger) RequestRefund(id RefundID, idempotencyKey string, bucket Bucket, amount money.Money, reason string, actor identity.Actor, now time.Time) (Refund, bool, error) {
	if !actor.Valid() {
		return Refund{}, false, ErrActorRequired
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return Refund{}, false, domainerr.Invalid("idempotency_key", "required")
	}
	if i := slices.IndexFunc(l.Refunds, func(r Refund) bool { return r.IdempotencyKey == idempotencyKey }); i >= 0 {
		r := l.Refunds[i]
		if r.Bucket != bucket || !r.Amount.Equal(amount) {
			return Refund{}, false, ErrIdempotencyConflict
		}
		return r, false, nil
	}
	if l.Staged != nil {
		return Refund{}, false, fmt.Errorf("refund during repricing %s: %w", l.Staged.Ref, ErrRepricingConflict)
	}
	if id == "" || slices.ContainsFunc(l.Refunds, func(r Refund) bool { return r.ID == id }) {
		return Refund{}, false, domainerr.Invalid("refund_id", "required and unique")
	}
	if !bucket.valid() {
		return Refund{}, false, domainerr.Invalid("bucket", "unknown bucket")
	}
	if err := l.checkAmount(amount); err != nil {
		return Refund{}, false, err
	}
	if strings.TrimSpace(reason) == "" {
		return Refund{}, false, ErrReasonRequired
	}
	if amount.Amount() > l.Summary().refundable(bucket) {
		return Refund{}, false, fmt.Errorf("refund %s %s: %w", bucket, amount, ErrExceedsRefundable)
	}
	r := Refund{
		ID: id, IdempotencyKey: idempotencyKey, Bucket: bucket, Amount: amount, Reason: reason,
		Status: RefundRequested, RequestedBy: actor, RequestedAt: now.UTC(),
	}
	l.Refunds = append(l.Refunds, r)
	return r, true, nil
}

// ApproveRefund approves a requested refund, enforcing separation of duties.
func (l *Ledger) ApproveRefund(id RefundID, approver identity.Actor, policy ApprovalPolicy, now time.Time) error {
	r, err := l.refund(id)
	if err != nil {
		return err
	}
	if r.Status != RefundRequested {
		return fmt.Errorf("refund %s %s: %w", id, r.Status, ErrInvalidTransition)
	}
	if err := policy.check(r.RequestedBy, approver, r.Amount); err != nil {
		return err
	}
	r.Status, r.DecidedBy, r.DecidedAt = RefundApproved, &approver, ptr(now)
	return nil
}

// RejectRefund releases the reserved amount.
func (l *Ledger) RejectRefund(id RefundID, actor identity.Actor, note string, now time.Time) error {
	if !actor.IsUser() {
		return ErrActorRequired
	}
	if strings.TrimSpace(note) == "" {
		return ErrReasonRequired
	}
	r, err := l.refund(id)
	if err != nil {
		return err
	}
	if r.Status != RefundRequested && r.Status != RefundApproved {
		return fmt.Errorf("refund %s %s: %w", id, r.Status, ErrInvalidTransition)
	}
	r.Status, r.DecidedBy, r.DecidedAt, r.DecisionNote = RefundRejected, &actor, ptr(now), note
	return nil
}

// MarkRefundSubmitted records that the refund was sent to the provider. The
// adapter must use the refund ID as the provider idempotency key so a retry
// never refunds twice.
func (l *Ledger) MarkRefundSubmitted(id RefundID, provider, providerRef string) error {
	r, err := l.refund(id)
	if err != nil {
		return err
	}
	if r.Status == RefundProcessing && r.Provider == provider && r.ProviderRef == providerRef {
		return nil
	}
	if r.Status != RefundApproved {
		return fmt.Errorf("refund %s %s: %w", id, r.Status, ErrInvalidTransition)
	}
	if provider == "" || providerRef == "" {
		return domainerr.Invalid("provider", "provider and reference are required")
	}
	r.Status, r.Provider, r.ProviderRef = RefundProcessing, provider, providerRef
	return nil
}

// RefundPayout is how money went back outside any provider.
type RefundPayout struct {
	Method Method
	// Reference is the receipt code, or the bank's transaction code, which a
	// transfer must have.
	Reference string
	// PaidAt is when the money moved; it may be before it is entered.
	PaidAt time.Time
}

// RecordCounterRefund records money handed back in cash or by manual transfer.
// The money moves outside the system, so this confirmation is all it takes,
// whatever the amount, and a one-person shop can run (owner decisions D30,
// D32). A transfer must name its bank transaction, to reconcile with the
// statement. Refunds through a provider still need approval first.
func (l *Ledger) RecordCounterRefund(id RefundID, payout RefundPayout, actor identity.Actor, now time.Time) error {
	if !actor.IsUser() {
		return ErrActorRequired
	}
	if payout.Method != MethodCash && payout.Method != MethodBankTransfer {
		return domainerr.Invalid("method", "counter refunds are cash or bank transfer")
	}
	if payout.Method == MethodBankTransfer && strings.TrimSpace(payout.Reference) == "" {
		return domainerr.Invalid("reference", "a transfer needs its bank transaction code")
	}
	if payout.PaidAt.IsZero() || payout.PaidAt.After(now) {
		return domainerr.Invalid("paid_at", "required and not in the future")
	}
	r, err := l.refund(id)
	if err != nil {
		return err
	}
	if r.Status != RefundApproved && r.Status != RefundRequested {
		return fmt.Errorf("refund %s %s: %w", id, r.Status, ErrInvalidTransition)
	}
	r.Status, r.CompletedAt = RefundSucceeded, ptr(payout.PaidAt)
	r.Method, r.Reference, r.RecordedBy, r.RecordedAt = payout.Method, payout.Reference, &actor, ptr(now)
	return nil
}

// RefundEvent is a verified provider notification about a refund.
type RefundEvent struct {
	Provider      string
	EventID       string
	RefundID      RefundID
	ProviderRef   string
	Amount        money.Money
	Status        EventStatus
	FailureReason string
	OccurredAt    time.Time
}

// ApplyRefundEvent settles a PROCESSING refund. A failed refund frees its
// amount; a new refund must be requested to retry.
func (l *Ledger) ApplyRefundEvent(e RefundEvent) (Outcome, error) {
	if e.Provider == "" || e.EventID == "" {
		return "", domainerr.Invalid("event", "provider and event id are required")
	}
	key := eventKey(e.Provider, e.EventID)
	if l.ProcessedEvents[key] {
		return OutcomeDuplicate, nil
	}
	r, err := l.refund(e.RefundID)
	if err != nil {
		return "", err
	}
	if r.Provider != e.Provider || r.ProviderRef != e.ProviderRef || !r.Amount.Equal(e.Amount) {
		return "", fmt.Errorf("refund %s event %s: %w", r.ID, e.EventID, ErrProviderMismatch)
	}
	outcome := OutcomeApplied
	switch {
	case e.Status == EventSucceeded && r.Status == RefundProcessing:
		r.Status, r.CompletedAt = RefundSucceeded, ptr(e.OccurredAt)
	case e.Status == EventFailed && r.Status == RefundProcessing:
		r.Status, r.CompletedAt, r.FailureReason = RefundFailed, ptr(e.OccurredAt), e.FailureReason
	case e.Status == EventSucceeded && r.Status == RefundSucceeded,
		e.Status == EventFailed && r.Status == RefundFailed:
		outcome = OutcomeNoChange
	case e.Status != EventSucceeded && e.Status != EventFailed:
		return "", domainerr.Invalid("status", "unknown event status")
	default:
		return "", fmt.Errorf("refund %s %s on %s event: %w", r.ID, r.Status, e.Status, ErrInvalidTransition)
	}
	l.ProcessedEvents[key] = true
	return outcome, nil
}

func (l *Ledger) refund(id RefundID) (*Refund, error) {
	i := slices.IndexFunc(l.Refunds, func(r Refund) bool { return r.ID == id })
	if i < 0 {
		return nil, fmt.Errorf("refund %s: %w", id, ErrNotFound)
	}
	return &l.Refunds[i], nil
}
