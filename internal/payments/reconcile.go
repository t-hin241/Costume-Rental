package payments

import (
	"strings"
	"time"
	"unicode/utf8"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
)

// TxnKind names the ledger record a reconciliation is about.
type TxnKind string

const (
	TxnPayment TxnKind = "PAYMENT"
	TxnRefund  TxnKind = "REFUND"
)

const (
	maxStatementRef = 80
	maxReconNote    = 500
)

var (
	ErrNotReconcilable    = domainerr.New(domainerr.KindInvalidState, "payments.not_reconcilable", "only a succeeded payment or refund can be reconciled")
	ErrAlreadyReconciled  = domainerr.New(domainerr.KindConflict, "payments.already_reconciled", "the transaction is already reconciled")
	ErrReconciliationOver = domainerr.New(domainerr.KindInvalidState, "payments.reconciliation_revoked", "the reconciliation is already revoked")
)

// Reconciliation records that finance matched a succeeded payment or refund
// with the bank statement or the cash book (decision D41, tax-policy §10.3).
// It is kept apart from the ledger: it changes no balance. A mistaken one is
// revoked, never deleted, and the transaction can then be reconciled again.
type Reconciliation struct {
	ID           string
	Kind         TxnKind
	TxnID        string
	BookingRef   string
	StatementRef string
	Note         string
	By           identity.Actor
	At           time.Time
	Revoked      *Revocation
}

// Revocation undoes a reconciliation.
type Revocation struct {
	Reason string
	By     identity.Actor
	At     time.Time
}

// Reconcile marks a succeeded payment or refund of the ledger as matched
// (payment.reconcile). active tells whether it already has a reconciliation
// in force.
func (l *Ledger) Reconcile(p identity.Principal, id string, kind TxnKind, txnID string, active bool, statementRef, note string, now time.Time) (Reconciliation, error) {
	if err := p.Require(identity.PermPaymentReconcile); err != nil {
		return Reconciliation{}, err
	}
	statementRef, note = strings.TrimSpace(statementRef), strings.TrimSpace(note)
	switch {
	case id == "":
		return Reconciliation{}, domainerr.Invalid("id", "required")
	case statementRef == "" || utf8.RuneCountInString(statementRef) > maxStatementRef:
		return Reconciliation{}, domainerr.Invalid("statement_ref", "required, at most 80 characters")
	case utf8.RuneCountInString(note) > maxReconNote:
		return Reconciliation{}, domainerr.Invalid("note", "at most 500 characters")
	}
	succeeded := false
	switch kind {
	case TxnPayment:
		for _, x := range l.Payments {
			if string(x.ID) == txnID {
				succeeded = x.Status == PaymentSucceeded
			}
		}
	case TxnRefund:
		for _, x := range l.Refunds {
			if string(x.ID) == txnID {
				succeeded = x.Status == RefundSucceeded
			}
		}
	default:
		return Reconciliation{}, domainerr.Invalid("kind", "must be PAYMENT or REFUND")
	}
	if !succeeded {
		return Reconciliation{}, ErrNotReconcilable
	}
	if active {
		return Reconciliation{}, ErrAlreadyReconciled
	}
	return Reconciliation{
		ID: id, Kind: kind, TxnID: txnID, BookingRef: l.BookingRef, StatementRef: statementRef, Note: note,
		By: p.Actor(), At: now.UTC(),
	}, nil
}

// Revoke undoes a reconciliation made by mistake (payment.reconcile).
func (r *Reconciliation) Revoke(p identity.Principal, reason string, now time.Time) error {
	if err := p.Require(identity.PermPaymentReconcile); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > maxReconNote {
		return ErrReasonRequired
	}
	if r.Revoked != nil {
		return ErrReconciliationOver
	}
	r.Revoked = &Revocation{Reason: reason, By: p.Actor(), At: now.UTC()}
	return nil
}
