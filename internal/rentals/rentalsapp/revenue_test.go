package rentalsapp_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/payments/paymentspg"
	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalsapp"
	"webee_rentor/internal/tax"
	"webee_rentor/internal/tax/taxapp"
	"webee_rentor/internal/tax/taxpg"
)

// revenue returns the revenue service, with a tax profile covering 2026 in
// place. Profiles are shared by the package database: the first test adds it.
func (e *env) revenue() *taxapp.Service {
	e.t.Helper()
	ctx := context.Background()
	loc, err := businesstime.LoadLocation()
	if err != nil {
		e.t.Fatal(err)
	}
	svc := taxapp.New(e.f.Pool, loc, taxapp.WithClock(e.clock.Now))
	h, err := taxpg.LoadHistory(ctx, e.f.Pool, false)
	if err != nil {
		e.t.Fatal(err)
	}
	if len(h.Profiles) == 0 {
		admin := e.f.User("owner", identity.RoleAdmin)
		if _, err := svc.AddProfile(ctx, admin, taxapp.ProfileInput{
			Version: "tax-2026", AnnualRevenueThreshold: pgtest.VND(1_000_000_000),
			ThresholdEffectiveFrom: pgtest.Date(e.t, "2026-01-01"), WarningPercents: []int{70, 85, 95},
			EffectiveFrom: pgtest.Date(e.t, "2026-01-01"),
		}); err != nil {
			e.t.Fatal(err)
		}
	}
	return svc
}

func (e *env) entries(id rentals.BookingID) []tax.RevenueEntry {
	e.t.Helper()
	es, err := taxpg.EntriesOfBooking(context.Background(), e.f.Pool, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return es
}

func netOf(es []tax.RevenueEntry, c tax.Category) int64 {
	var v int64
	for _, e := range es {
		if e.Category == c {
			v += e.Net.Amount()
		}
	}
	return v
}

// D41: completion recognises rent and fees, never the deposits, on the day
// the booking ended; a fee waived later is reversed on the day it was waived.
func TestCompletionRecognisesRevenueAndLaterCorrections(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	rev := e.revenue()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	fee := e.settledFee(b, asset)
	if got := e.entries(b.ID); len(got) != 0 {
		t.Fatalf("revenue before the booking ended: %+v", got)
	}
	refund := refundOf(t, e.ledger(b.ID), payments.BucketSecurity)
	if _, err := e.svc.ApproveRefund(ctx, e.finance, b.ID, refund.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RecordCounterRefund(ctx, e.staff, b.ID, refund.ID, cash); err != nil {
		t.Fatal(err)
	}
	done, err := e.svc.Complete(ctx, e.staff, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	es := e.entries(b.ID)
	if len(es) != 2 || netOf(es, tax.CatRentalRevenue) != b.RentTotal.Amount() || netOf(es, tax.CatCleaningService) != 100_000 {
		t.Fatalf("entries at completion = %+v", es)
	}
	for _, x := range es {
		if !x.RecognizedAt.Equal(*done.CompletedAt) || x.Source != tax.SourceBooking || x.ProfileVersion != "tax-2026" {
			t.Fatalf("entry = %+v, completed at %s", x, done.CompletedAt)
		}
	}

	waived := pgtest.Local(t, "2026-11-20 10:00")
	e.clock.Set(waived)
	if _, err := e.svc.DecideFee(ctx, e.finance, fee.ID, rentalsapp.FeeWaive, "khách khiếu nại đúng"); err != nil {
		t.Fatal(err)
	}
	es = e.entries(b.ID)
	if len(es) != 3 || netOf(es, tax.CatCleaningService) != 0 {
		t.Fatalf("entries after the waiver = %+v", es)
	}
	r := es[2]
	if r.Reverses == "" || !r.RecognizedAt.Equal(waived) || r.Reason != "khách khiếu nại đúng" {
		t.Fatalf("reversal = %+v", r)
	}

	rep, err := rev.Reconciliation(ctx, e.finance, taxapp.Period{From: pgtest.Date(t, "2026-11-01"), To: pgtest.Date(t, "2026-11-30")})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rep.Mismatched.Items {
		if c.ID == b.ID {
			t.Fatalf("a settled booking is reported mismatched: %+v", c)
		}
	}
	if _, err := rev.Reconciliation(ctx, e.staff, taxapp.Period{From: pgtest.Date(t, "2026-11-01"), To: pgtest.Date(t, "2026-11-30")}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff reading the reconciliation: err = %v", err)
	}
	y, err := rev.Year(ctx, e.finance, 2026)
	if err != nil || y.Summary == nil || y.Summary.Revenue.Amount() < b.RentTotal.Amount() {
		t.Fatalf("year = %+v, %v", y, err)
	}
}

// What a cancellation keeps is a cancellation charge; the reversed rent and
// the refunded deposits are not revenue.
func TestCancellationRecognisesOnlyTheFee(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.revenue()
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.payInFull(b)
	if _, err := e.svc.Confirm(ctx, e.staff, b.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(pgtest.Local(t, "2026-11-08 09:00"))
	if _, err := e.svc.Cancel(ctx, a, b.ID, rentalsapp.CancelInput{Reason: rentals.CancelCustomerRequest}); err != nil {
		t.Fatal(err)
	}
	es := e.entries(b.ID)
	if len(es) != 1 || es[0].Category != tax.CatCancellationCharge || es[0].Net.Amount() != 15_000 {
		t.Fatalf("entries = %+v", es)
	}
}

// The catch-up job recognises what a booking transaction did not record,
// here a charge reversed straight in the database.
func TestCatchUpRecognisesWhatATransactionMissed(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	rev := e.revenue()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	fee := e.settledFee(b, asset)
	refund := refundOf(t, e.ledger(b.ID), payments.BucketSecurity)
	if _, err := e.svc.ApproveRefund(ctx, e.finance, b.ID, refund.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RecordCounterRefund(ctx, e.staff, b.ID, refund.ID, cash); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Complete(ctx, e.staff, b.ID); err != nil {
		t.Fatal(err)
	}
	charge, _ := e.ledger(b.ID).ChargeOfAssessment(string(fee.ID))
	later := pgtest.Local(t, "2026-11-25 09:00")
	if _, err := e.f.Pool.Exec(ctx, `UPDATE ledger_charges SET reversed_at = $2, reversal_reason = 'sửa tay',
		reversed_by_kind = 'system', reversed_by_id = 'test' WHERE id = $1`, string(charge.ID), later); err != nil {
		t.Fatal(err)
	}
	pending, _, err := taxpg.PendingBookings(ctx, e.f.Pool, true, 1000)
	if err != nil || !slices.ContainsFunc(pending, func(p taxpg.Pending) bool { return p.ID == b.ID }) {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if _, err := rev.RecognizePending(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	es := e.entries(b.ID)
	if netOf(es, tax.CatCleaningService) != 0 || !es[len(es)-1].RecognizedAt.Equal(later) {
		t.Fatalf("entries after catch-up = %+v", es)
	}
	if n, err := rev.RecognizePending(ctx, 1000); err != nil || n != 0 {
		t.Fatalf("second pass = %d, %v", n, err)
	}
}

// D41 (T8): finance marks a succeeded payment as matched with the statement;
// a mistaken mark is revoked, never deleted, and the payment marked again.
func TestReconcilingAPayment(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	rev := e.revenue()
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.payInFull(b)
	pay := e.ledger(b.ID).Payments[0]
	day := taxapp.Period{From: pgtest.Date(t, "2026-11-01"), To: pgtest.Date(t, "2026-11-01")}
	no := false
	open, _, err := rev.Transactions(ctx, e.finance, day, payments.TxnPayment, &no, 1, 100)
	if err != nil || !slices.ContainsFunc(open, func(x paymentspg.Txn) bool { return x.ID == string(pay.ID) }) {
		t.Fatalf("unreconciled = %+v, %v", open, err)
	}
	if _, err := rev.Reconcile(ctx, e.staff, payments.TxnPayment, string(pay.ID), "SK-01", ""); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff reconciling: err = %v", err)
	}
	if _, err := rev.Reconcile(ctx, e.finance, payments.TxnPayment, string(pay.ID), " ", ""); err == nil {
		t.Fatal("a reconciliation needs the statement reference")
	}
	c, err := rev.Reconcile(ctx, e.finance, payments.TxnPayment, string(pay.ID), "Sổ quỹ 01/11 dòng 3", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rev.Reconcile(ctx, e.finance, payments.TxnPayment, string(pay.ID), "lần hai", ""); !errors.Is(err, payments.ErrAlreadyReconciled) {
		t.Fatalf("second reconciliation: err = %v", err)
	}
	yes := true
	done, _, err := rev.Transactions(ctx, e.finance, day, payments.TxnPayment, &yes, 1, 100)
	if err != nil || !slices.ContainsFunc(done, func(x paymentspg.Txn) bool {
		return x.ID == string(pay.ID) && x.Reconciliation != nil && x.Reconciliation.StatementRef == "Sổ quỹ 01/11 dòng 3"
	}) {
		t.Fatalf("reconciled = %+v, %v", done, err)
	}
	if err := rev.RevokeReconciliation(ctx, e.finance, c.ID, "nhầm dòng sổ quỹ"); err != nil {
		t.Fatal(err)
	}
	if err := rev.RevokeReconciliation(ctx, e.finance, c.ID, "lần hai"); !errors.Is(err, payments.ErrReconciliationOver) {
		t.Fatalf("revoking twice: err = %v", err)
	}
	if _, err := rev.Reconcile(ctx, e.finance, payments.TxnPayment, string(pay.ID), "Sổ quỹ 01/11 dòng 4", ""); err != nil {
		t.Fatalf("reconciling again after a revocation: %v", err)
	}
}

// The balances the reports derive from database sums are the ones the
// ledger derives from its records.
func TestDatabaseTotalsMatchTheLedger(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	rev := e.revenue()
	e.f.Assets(e.c.JacketM, 3)
	a := e.f.User("a", identity.RoleCustomer)
	before, err := rev.CashFlow(ctx, e.finance, taxapp.Period{From: pgtest.Date(t, "2026-11-01"), To: pgtest.Date(t, "2026-11-30")})
	if err != nil {
		t.Fatal(err)
	}

	unpaid := e.place(a, e.at("2026-11-12", "morning", e.item(e.c.JacketM)))
	paid := e.place(a, e.at("2026-11-12", "morning", e.item(e.c.JacketM)))
	e.payInFull(paid)
	returned := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.settledFee(returned, e.active(returned.ID)[0].AssetID)

	ids := []string{string(unpaid.ID), string(paid.ID), string(returned.ID)}
	totals, err := paymentspg.Totals(ctx, e.f.Pool, ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		want := e.ledger(rentals.BookingID(id)).Summary()
		if got := totals[id].Summary(money.VND); got.State != want.State || got.RentalOutstanding != want.RentalOutstanding ||
			got.SecurityHeld != want.SecurityHeld || got.ReservationHeld != want.ReservationHeld || got.PendingRefunds != want.PendingRefunds ||
			got.RentalCovered != want.RentalCovered || got.ReservationCredit != want.ReservationCredit || got.TotalReceived != want.TotalReceived {
			t.Errorf("booking %s: database %+v, ledger %+v", id, got, want)
		}
	}

	after, err := rev.CashFlow(ctx, e.finance, taxapp.Period{From: pgtest.Date(t, "2026-11-01"), To: pgtest.Date(t, "2026-11-30")})
	if err != nil {
		t.Fatal(err)
	}
	// The paid booking holds its whole deposit; the returned one has its
	// deposit applied and the rest waiting as a refund.
	if d := after.Position.SecurityHeld.Amount() - before.Position.SecurityHeld.Amount(); d != paid.SecurityDepositTotal.Amount()+returned.SecurityDepositTotal.Amount()-100_000 {
		t.Fatalf("security held grew by %d", d)
	}
	if after.Position.DueOpen.Amount() <= before.Position.DueOpen.Amount() {
		t.Fatalf("the unpaid hold is not counted as due: %+v -> %+v", before.Position, after.Position)
	}
	var cashIn int64
	for _, m := range after.Received {
		if m.Method == payments.MethodCash {
			cashIn += m.Amount.Amount()
		}
	}
	if cashIn == 0 {
		t.Fatal("cash received is missing")
	}
}
