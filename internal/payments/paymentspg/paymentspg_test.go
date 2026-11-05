package paymentspg_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/catalog"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/payments/paymentspg"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/rentals/rentalsapp"
)

func TestMain(m *testing.M) { pgtest.Main(m, pgtest.SeedConfig) }

// bookingRef places a real booking, which opens its ledger.
func bookingRef(t *testing.T, f *pgtest.Fixture) string {
	t.Helper()
	c := f.Catalog()
	f.Assets(c.JacketM, 1)
	loc, err := businesstime.LoadLocation()
	if err != nil {
		t.Fatal(err)
	}
	clock := pgtest.NewClock(pgtest.Now)
	svc := rentalsapp.New(f.Pool, loc, rentalsapp.WithClock(clock.Now))
	b, err := svc.PlaceBooking(context.Background(), f.User("cust", identity.RoleCustomer), rentalsapp.PlaceInput{
		PackageID: c.OneDay.ID, PickupDate: pgtest.Date(t, "2026-11-10"), SlotID: "morning",
		Lines: []rentalsapp.LineInput{{Kind: catalog.KindItem, OfferingID: string(c.Jacket.ID), VariantID: c.JacketM, Quantity: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b.ID)
}

// Every record kind, in every state the columns allow, reads back as written.
func TestLedgerRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	ref := bookingRef(t, f)
	staff := identity.UserActor("staff")
	now := pgtest.Now

	l, v, err := paymentspg.LoadLedger(ctx, f.Pool, ref, false)
	if err != nil {
		t.Fatal(err)
	}
	id := func(s string) string { return f.Prefix + "-" + s }
	if _, _, err := l.RecordCounterPayment(payments.NewPaymentRequest{
		ID: payments.PaymentID(id("p1")), IdempotencyKey: id("k1"), Method: payments.MethodBankTransfer,
		Portions: []payments.Portion{{Bucket: payments.BucketRental, Amount: pgtest.VND(35_000)}, {Bucket: payments.BucketReservation, Amount: pgtest.VND(15_000)}},
		Actor:    staff, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	gateway, _, err := l.StartGatewayPayment(payments.NewPaymentRequest{
		ID: payments.PaymentID(id("p2")), IdempotencyKey: id("k2"), Method: payments.MethodGateway, Provider: "fakepay",
		Portions: []payments.Portion{{Bucket: payments.BucketSecurity, Amount: pgtest.VND(300_000)}},
		Actor:    identity.SystemActor("checkout"), Now: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.ApplyPaymentEvent(payments.PaymentEvent{
		Provider: "fakepay", EventID: id("e1"), PaymentID: gateway.ID, ProviderTxnID: id("txn"),
		Amount: gateway.Amount, Status: payments.EventSucceeded, OccurredAt: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	approver := identity.UserActor("manager")
	if _, _, err := l.AddApprovedFee(payments.ChargeID(id("c1")), payments.ChargeLateFee, pgtest.VND(25_000), id("a1"), "5 giờ", []string{"photos/1"}, approver, now); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ApplySecurityToApprovedFees(id("settle"), staff, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.ApplyReservationToRent(payments.ResolutionID(id("res1")), staff, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.RequestRefund(payments.RefundID(id("r1")), id("rk1"), payments.BucketSecurity, pgtest.VND(100_000), "hoàn cọc", staff, now); err != nil {
		t.Fatal(err)
	}
	if err := l.ApproveRefund(payments.RefundID(id("r1")), approver, payments.ApprovalPolicy{DistinctApproverAbove: pgtest.VND(0)}, now); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkRefundSubmitted(payments.RefundID(id("r1")), "fakepay", id("ref")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.RequestRefund(payments.RefundID(id("r2")), id("rk2"), payments.BucketSecurity, pgtest.VND(50_000), "thu sai", approver, now); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordCounterRefund(payments.RefundID(id("r2")), payments.RefundPayout{Method: payments.MethodCash, Reference: "PC-1", PaidAt: now.Add(-time.Hour)},
		staff, now); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ReverseDeductions(payments.ChargeID(id("c1")), "thu sai", approver, now); err != nil {
		t.Fatal(err)
	}
	if _, err := l.StageRepricing(id("m1"), pgtest.VND(60_000), pgtest.VND(300_000), staff, now); err != nil {
		t.Fatal(err)
	}

	nv, err := paymentspg.SaveLedger(ctx, f.Pool, l, v, now)
	if err != nil {
		t.Fatal(err)
	}
	got, gv, err := paymentspg.LoadLedger(ctx, f.Pool, ref, false)
	if err != nil || gv != nv {
		t.Fatalf("load = %d (%v), want version %d", gv, err, nv)
	}
	if !reflect.DeepEqual(got, l) {
		t.Fatalf("round trip differs:\n got %+v\nwant %+v", got, l)
	}
	if _, err := paymentspg.SaveLedger(ctx, f.Pool, got, v, now); !errors.Is(err, postgres.ErrStale) {
		t.Fatalf("writing with an old version: err = %v, want ErrStale", err)
	}
}

func TestCancelledLedgerRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	ref := bookingRef(t, f)
	l, v, err := paymentspg.LoadLedger(ctx, f.Pool, ref, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.SettleCancellation(payments.CancellationRequest{
		Fee: pgtest.VND(15_000), Basis: "tier", FeeChargeID: payments.ChargeID(f.Prefix + "-fee"),
		ResolutionID: payments.ResolutionID(f.Prefix + "-res"), Actor: identity.SystemActor("job"), Now: pgtest.Now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := paymentspg.SaveLedger(ctx, f.Pool, l, v, pgtest.Now); err != nil {
		t.Fatal(err)
	}
	got, _, err := paymentspg.LoadLedger(ctx, f.Pool, ref, false)
	if err != nil || !reflect.DeepEqual(got, l) {
		t.Fatalf("round trip differs (%v):\n got %+v\nwant %+v", err, got, l)
	}
}

// applyEvent runs one provider notification the way a webhook will: the
// ledger locked, the event applied and saved in one transaction.
func applyEvent(ctx context.Context, f *pgtest.Fixture, ref string, e payments.PaymentEvent) (payments.Outcome, error) {
	var out payments.Outcome
	err := postgres.InTx(ctx, f.Pool, func(tx pgx.Tx) error {
		l, v, err := paymentspg.LoadLedger(ctx, tx, ref, true)
		if err != nil {
			return err
		}
		if out, err = l.ApplyPaymentEvent(e); err != nil {
			return err
		}
		_, err = paymentspg.SaveLedger(ctx, tx, l, v, pgtest.Now)
		return err
	})
	return out, err
}

// The provider has no webhook adapter yet, so its signature check is not
// tested here. What the ledger stores must already survive the replays.
func TestAProviderEventCountsOnceAcrossReplaysAndReloads(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	ref := bookingRef(t, f)
	l, v, err := paymentspg.LoadLedger(ctx, f.Pool, ref, false)
	if err != nil {
		t.Fatal(err)
	}
	gateway, _, err := l.StartGatewayPayment(payments.NewPaymentRequest{
		ID: payments.PaymentID(f.Prefix + "-p"), IdempotencyKey: f.Prefix + "-k", Method: payments.MethodGateway, Provider: "fakepay",
		Portions: []payments.Portion{{Bucket: payments.BucketSecurity, Amount: pgtest.VND(300_000)}},
		Actor:    identity.SystemActor("checkout"), Now: pgtest.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := paymentspg.SaveLedger(ctx, f.Pool, l, v, pgtest.Now); err != nil {
		t.Fatal(err)
	}
	event := payments.PaymentEvent{Provider: "fakepay", EventID: f.Prefix + "-e1", PaymentID: gateway.ID, ProviderTxnID: f.Prefix + "-txn",
		Amount: gateway.Amount, Status: payments.EventSucceeded, OccurredAt: pgtest.Now}

	// The provider sends the same event several times at once.
	outcomes := make([]payments.Outcome, 5)
	var wg sync.WaitGroup
	for i := range outcomes {
		wg.Go(func() {
			outcome, err := applyEvent(ctx, f, ref, event)
			if err != nil {
				t.Error(err)
			}
			outcomes[i] = outcome
		})
	}
	wg.Wait()
	applied := 0
	for _, o := range outcomes {
		switch o {
		case payments.OutcomeApplied:
			applied++
		case payments.OutcomeDuplicate:
		default:
			t.Errorf("outcome = %s", o)
		}
	}
	if applied != 1 {
		t.Fatalf("outcomes = %v, want one applied", outcomes)
	}

	// A later replay is recognised after a reload, and a forged amount or a
	// different transaction changes nothing.
	if got, err := applyEvent(ctx, f, ref, event); err != nil || got != payments.OutcomeDuplicate {
		t.Fatalf("replay = %s, %v", got, err)
	}
	forged := event
	forged.EventID, forged.Amount = f.Prefix+"-e2", pgtest.VND(1_000)
	if _, err := applyEvent(ctx, f, ref, forged); !errors.Is(err, payments.ErrProviderMismatch) {
		t.Fatalf("forged amount: err = %v", err)
	}
	forged = event
	forged.EventID, forged.ProviderTxnID = f.Prefix+"-e3", f.Prefix+"-other"
	if _, err := applyEvent(ctx, f, ref, forged); !errors.Is(err, payments.ErrProviderMismatch) {
		t.Fatalf("another transaction: err = %v", err)
	}
	forged = event
	forged.EventID, forged.Status = f.Prefix+"-e4", payments.EventFailed
	if _, err := applyEvent(ctx, f, ref, forged); !errors.Is(err, payments.ErrInvalidTransition) {
		t.Fatalf("failure after success: err = %v", err)
	}

	got, _, err := paymentspg.LoadLedger(ctx, f.Pool, ref, false)
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Summary(); s.SecurityReceived.Amount() != 300_000 || s.TotalReceived.Amount() != 300_000 {
		t.Fatalf("received = %+v", s)
	}
	var stored int
	if err := f.Pool.QueryRow(ctx, `SELECT count(*) FROM provider_events WHERE booking_id = $1`, ref).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatalf("%d events stored", stored)
	}
}
