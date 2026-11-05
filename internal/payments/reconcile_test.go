package payments_test

import (
	"errors"
	"testing"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/payments"
)

// Decision D41: only money that really moved is matched with the statement.
func TestOnlySucceededTransactionsAreReconciled(t *testing.T) {
	fin, err := identity.NewPrincipal("finance1", []identity.Role{identity.RoleFinance}, identity.ReferencePermissionMatrix())
	if err != nil {
		t.Fatal(err)
	}
	staff, err := identity.NewPrincipal("staff1", []identity.Role{identity.RoleRentalStaff}, identity.ReferencePermissionMatrix())
	if err != nil {
		t.Fatal(err)
	}
	l := ledger(t)
	payAtCounter(t, l, "p1", 200_000, 0)
	gateway(t, l, "p2", 0, 550_000) // still pending

	if _, err := l.Reconcile(staff, "r0", payments.TxnPayment, "p1", false, "SK-1", "", now); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff: err = %v", err)
	}
	for name, c := range map[string]struct {
		kind payments.TxnKind
		id   string
	}{"pending": {payments.TxnPayment, "p2"}, "unknown": {payments.TxnPayment, "p9"}, "refund that is a payment": {payments.TxnRefund, "p1"}} {
		if _, err := l.Reconcile(fin, "r1", c.kind, c.id, false, "SK-1", "", now); !errors.Is(err, payments.ErrNotReconcilable) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := l.Reconcile(fin, "r1", payments.TxnPayment, "p1", true, "SK-1", "", now); !errors.Is(err, payments.ErrAlreadyReconciled) {
		t.Fatalf("already reconciled: err = %v", err)
	}
	r, err := l.Reconcile(fin, "r1", payments.TxnPayment, "p1", false, "  SK-1  ", "", now)
	if err != nil || r.StatementRef != "SK-1" || r.BookingRef != "bk-1" || r.By != fin.Actor() {
		t.Fatalf("reconciliation = %+v, %v", r, err)
	}
	if err := r.Revoke(fin, " ", now); !errors.Is(err, payments.ErrReasonRequired) {
		t.Fatalf("revoking without a reason: err = %v", err)
	}
	if err := r.Revoke(fin, "nhầm dòng", now); err != nil || r.Revoked == nil {
		t.Fatalf("revoke: %+v, %v", r.Revoked, err)
	}
	if err := r.Revoke(fin, "lần nữa", now); !errors.Is(err, payments.ErrReconciliationOver) {
		t.Fatalf("revoking twice: err = %v", err)
	}
}
