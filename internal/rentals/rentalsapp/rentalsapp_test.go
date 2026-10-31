package rentalsapp_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/catalog"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/inventory/inventoryapp"
	"webee_rentor/internal/inventory/inventorypg"
	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalsapp"
)

func TestMain(m *testing.M) { pgtest.Main(m, pgtest.SeedConfig) }

type env struct {
	t     *testing.T
	f     *pgtest.Fixture
	c     pgtest.Catalog
	clock *pgtest.Clock
	svc   *rentalsapp.Service
	inv   *inventoryapp.Service
	staff identity.Principal
	// finance approves refunds and fees; staff never can (decision D22).
	finance identity.Principal
}

func setup(t *testing.T) *env {
	t.Helper()
	f := pgtest.New(t)
	loc, err := businesstime.LoadLocation()
	if err != nil {
		t.Fatal(err)
	}
	clock := pgtest.NewClock(pgtest.Now)
	return &env{
		t: t, f: f, c: f.Catalog(), clock: clock,
		svc:     rentalsapp.New(f.Pool, loc, rentalsapp.WithClock(clock.Now)),
		inv:     inventoryapp.New(f.Pool, inventoryapp.WithClock(clock.Now)),
		staff:   f.User("staff", identity.RoleRentalStaff),
		finance: f.User("finance", identity.RoleFinance),
	}
}

func (e *env) item(v catalog.VariantID) rentalsapp.LineInput {
	return rentalsapp.LineInput{Kind: catalog.KindItem, OfferingID: string(e.c.Jacket.ID), VariantID: v, Quantity: 1}
}

func (e *env) set(jacket catalog.VariantID) rentalsapp.LineInput {
	return rentalsapp.LineInput{Kind: catalog.KindBundle, OfferingID: string(e.c.Set.ID),
		Selections: map[string]catalog.VariantID{"jacket": jacket}, Quantity: 1}
}

func (e *env) at(date, slot string, lines ...rentalsapp.LineInput) rentalsapp.PlaceInput {
	return rentalsapp.PlaceInput{PackageID: e.c.OneDay.ID, PickupDate: pgtest.Date(e.t, date), SlotID: slot, Lines: lines}
}

func (e *env) place(p identity.Principal, in rentalsapp.PlaceInput) *rentals.Booking {
	e.t.Helper()
	b, err := e.svc.PlaceBooking(context.Background(), p, in)
	if err != nil {
		e.t.Fatalf("place booking: %v", err)
	}
	return b
}

func (e *env) allocations(b rentals.BookingID) []inventory.Allocation {
	e.t.Helper()
	got, err := inventorypg.AllocationsOfBooking(context.Background(), e.f.Pool, string(b), false)
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

func (e *env) active(b rentals.BookingID) []inventory.Allocation {
	return slices.DeleteFunc(e.allocations(b), func(a inventory.Allocation) bool { return a.Status != inventory.AllocationActive })
}

func (e *env) bookingsOf(p identity.Principal) int {
	e.t.Helper()
	var n int
	if err := e.f.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bookings WHERE customer_id = $1`, p.UserID).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func wantShort(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Fatalf("err = %v, want ErrInsufficientInventory", err)
	}
}

// §12: a bundle holds every component; individual items use the same stock
// and nothing is counted twice.
func TestBundleAndItemsShareStock(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	e.f.Assets(e.c.PantsM, 1)
	e.f.Assets(e.c.BeltStd, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.f.User("b", identity.RoleCustomer)

	booked := e.place(a, e.at("2026-11-10", "morning", e.set(e.c.JacketM)))
	if got := len(e.active(booked.ID)); got != 3 {
		t.Fatalf("bundle allocations = %d, want jacket + pants + belt", got)
	}
	if booked.RentTotal.Amount() != 80_000 || booked.SecurityDepositTotal.Amount() != 550_000 {
		t.Fatalf("bundle rent %s deposit %s; want the bundle price and component deposits", booked.RentTotal, booked.SecurityDepositTotal)
	}

	_, err := e.svc.PlaceBooking(ctx, b, e.at("2026-11-10", "afternoon", e.item(e.c.JacketM)))
	wantShort(t, err)
	pants := rentalsapp.LineInput{Kind: catalog.KindItem, OfferingID: string(e.c.Pants.ID), VariantID: e.c.PantsM, Quantity: 1}
	_, err = e.svc.PlaceBooking(ctx, b, e.at("2026-11-10", "morning", pants))
	wantShort(t, err)
	if n := e.bookingsOf(b); n != 0 {
		t.Fatalf("failed placements left %d bookings", n)
	}

	avail, err := e.svc.CheckAvailability(ctx, e.at("2026-11-10", "afternoon", e.item(e.c.JacketM)))
	if err != nil || avail.Available || len(avail.Shortages) != 1 {
		t.Fatalf("availability = %+v, %v", avail, err)
	}
	// The jacket is free again once the bundle's block (turnaround included)
	// has ended.
	e.place(b, e.at("2026-11-13", "morning", e.item(e.c.JacketM)))
}

// §12: two customers racing for the last asset; exactly one wins.
func TestConcurrentRequestsForTheLastAssetOnlyOneWins(t *testing.T) {
	e := setup(t)
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	const racers = 8
	customers := make([]identity.Principal, racers)
	for i := range customers {
		customers[i] = e.f.User(string(rune('a'+i)), identity.RoleCustomer)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, racers)
	for i := range customers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = e.svc.PlaceBooking(context.Background(), customers[i], e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
		}(i)
	}
	close(start)
	wg.Wait()
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, inventory.ErrInsufficientInventory):
			t.Errorf("loser err = %v, want ErrInsufficientInventory", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d placements succeeded, want exactly 1", wins)
	}
	active, err := inventorypg.ActiveAllocationsOn(context.Background(), e.f.Pool, []inventory.AssetID{asset})
	if err != nil || len(active) != 1 {
		t.Fatalf("active allocations on the last asset = %d (%v), want 1", len(active), err)
	}
}

func TestRetryWithRequestKeyReturnsTheSameBooking(t *testing.T) {
	e := setup(t)
	e.f.Assets(e.c.JacketM, 2)
	a := e.f.User("a", identity.RoleCustomer)
	in := e.at("2026-11-10", "morning", e.item(e.c.JacketM))
	in.RequestKey = "checkout-1"
	first := e.place(a, in)
	again := e.place(a, in)
	if again.ID != first.ID || e.bookingsOf(a) != 1 || len(e.active(first.ID)) != 1 {
		t.Fatalf("retry created a second booking or allocation")
	}
}

// §12: cancel releases allocations exactly once and frees the asset.
func TestCancelReleasesAllocations(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.f.User("b", identity.RoleCustomer)
	booked := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))

	if _, err := e.svc.Cancel(ctx, b, booked.ID, rentalsapp.CancelInput{Reason: rentals.CancelCustomerRequest}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("another customer cancelling: err = %v", err)
	}
	cancelled, err := e.svc.Cancel(ctx, a, booked.ID, rentalsapp.CancelInput{Reason: rentals.CancelCustomerRequest})
	if err != nil || cancelled.Status != rentals.StatusCancelled {
		t.Fatalf("cancel = %v, %v", cancelled, err)
	}
	allocs := e.allocations(booked.ID)
	if len(allocs) != 1 || allocs[0].Status != inventory.AllocationReleased {
		t.Fatalf("allocations after cancel = %+v", allocs)
	}
	if _, err := e.svc.Cancel(ctx, a, booked.ID, rentalsapp.CancelInput{Reason: rentals.CancelCustomerRequest}); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("second cancel err = %v", err)
	}
	if again := e.allocations(booked.ID); !allocs[0].ReleasedAt.Equal(*again[0].ReleasedAt) {
		t.Fatal("a repeated cancel must not release again")
	}
	e.place(b, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
}

// §12: hold expiry releases once; confirmed bookings are untouched.
func TestExpireHoldsReleasesOnlyUnconfirmed(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 2)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.f.User("b", identity.RoleCustomer)
	pending := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	kept := e.place(b, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.payInFull(kept)
	if _, err := e.svc.Confirm(ctx, e.staff, kept.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(pgtest.Now.Add(31 * time.Minute))
	if n, err := e.svc.ExpireHolds(ctx, 1000); err != nil || n < 1 {
		t.Fatalf("expire = %d, %v", n, err)
	}
	if n, err := e.svc.ExpireHolds(ctx, 1000); err != nil || n != 0 {
		t.Fatalf("second run expired %d (%v); must be a no-op", n, err)
	}
	got, err := e.svc.Booking(ctx, a, pending.ID)
	if err != nil || got.Status != rentals.StatusExpired || len(e.active(pending.ID)) != 0 {
		t.Fatalf("pending booking after expiry: %v %v, active %d", got.Status, err, len(e.active(pending.ID)))
	}
	if got, _ := e.svc.Booking(ctx, b, kept.ID); got.Status != rentals.StatusConfirmed || len(e.active(kept.ID)) != 1 {
		t.Fatalf("confirmed booking was touched: %v", got.Status)
	}
}

// §12: a failed reschedule keeps the old reservation; a successful one
// swaps allocations atomically.
func TestRescheduleKeepsOldAllocationsWhenTheNewTimeIsTaken(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.f.User("b", identity.RoleCustomer)
	mine := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.place(b, e.at("2026-11-13", "morning", e.item(e.c.JacketM)))
	before := e.active(mine.ID)

	_, err := e.svc.Reschedule(ctx, a, mine.ID, rentalsapp.RescheduleInput{PickupDate: pgtest.Date(t, "2026-11-13"), SlotID: "morning"})
	wantShort(t, err)
	got, _ := e.svc.Booking(ctx, a, mine.ID)
	after := e.active(mine.ID)
	if !got.Schedule.PickupAt.Equal(mine.Schedule.PickupAt) || len(after) != 1 || after[0].ID != before[0].ID {
		t.Fatalf("failed reschedule changed the booking or its allocation")
	}

	moved, err := e.svc.Reschedule(ctx, a, mine.ID, rentalsapp.RescheduleInput{PickupDate: pgtest.Date(t, "2026-11-17"), SlotID: "morning"})
	if err != nil {
		t.Fatal(err)
	}
	now := e.active(mine.ID)
	if len(now) != 1 || now[0].ID == before[0].ID || !now[0].Block.Contains(moved.Schedule.PickupAt) {
		t.Fatalf("allocations after reschedule = %+v", now)
	}
	if all := e.allocations(mine.ID); len(all) != 2 {
		t.Fatalf("old allocation must be kept as RELEASED history, got %d rows", len(all))
	}
}

func (e *env) handOver(b *rentals.Booking, at time.Time) {
	e.t.Helper()
	ctx := context.Background()
	e.payInFull(b)
	if _, err := e.svc.Confirm(ctx, e.staff, b.ID); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.StartPreparation(ctx, e.staff, b.ID); err != nil {
		e.t.Fatal(err)
	}
	e.clock.Set(at)
	var delivered []rentals.DeliveredItem
	for _, a := range e.active(b.ID) {
		delivered = append(delivered, rentals.DeliveredItem{AssetID: a.AssetID})
	}
	if _, err := e.svc.RecordHandover(ctx, e.staff, b.ID, rentalsapp.HandoverInput{Delivered: delivered, ChecklistCompleted: true}); err != nil {
		e.t.Fatalf("handover: %v", err)
	}
}

func (e *env) assetStatus(id inventory.AssetID) inventory.AssetStatus {
	e.t.Helper()
	var s inventory.AssetStatus
	if err := e.f.Pool.QueryRow(context.Background(), `SELECT status FROM assets WHERE id = $1`, id).Scan(&s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// §12: a returned asset is not ready until inspected; its turnaround keeps
// blocking until then, and inspection frees it early.
func TestReturnedAssetBlocksUntilInspected(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.f.User("b", identity.RoleCustomer)
	booked := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.handOver(booked, pgtest.Local(t, "2026-11-10 08:30"))
	if e.assetStatus(asset) != inventory.AssetRented || e.active(booked.ID)[0].HandedOverAt == nil {
		t.Fatal("handover must mark the asset RENTED and the allocation handed over")
	}

	e.clock.Set(pgtest.Local(t, "2026-11-11 09:00"))
	if _, err := e.svc.RecordReturn(ctx, e.staff, booked.ID, []inventory.AssetID{asset}); err != nil {
		t.Fatal(err)
	}
	if e.assetStatus(asset) != inventory.AssetNeedsInspection {
		t.Fatal("a returned asset must need inspection, not be READY")
	}
	_, err := e.svc.PlaceBooking(ctx, b, e.at("2026-11-11", "afternoon", e.item(e.c.JacketM)))
	wantShort(t, err)

	e.clock.Set(pgtest.Local(t, "2026-11-11 09:30"))
	if _, err := e.inv.ChangeStatus(ctx, e.staff, asset, inventoryapp.StatusInput{
		Action: inventory.ActionPassInspection, Checklist: inventory.InspectionChecklist{Completed: true},
	}); err != nil {
		t.Fatal(err)
	}
	if allocs := e.allocations(booked.ID); allocs[0].Status != inventory.AllocationClosed {
		t.Fatalf("inspection must close the allocation, got %s", allocs[0].Status)
	}
	e.place(b, e.at("2026-11-11", "afternoon", e.item(e.c.JacketM)))
}

// §12: a late return keeps blocking its asset (no double allocation) and
// the endangered next booking is reported.
func TestLateReturnKeepsBlockingAndIsReported(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.f.User("b", identity.RoleCustomer)
	c := e.f.User("c", identity.RoleCustomer)
	late := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	next := e.place(b, e.at("2026-11-12", "afternoon", e.item(e.c.JacketM)))
	e.handOver(late, pgtest.Local(t, "2026-11-10 08:30"))

	e.clock.Set(pgtest.Local(t, "2026-11-12 10:00")) // the block ended at 09:00
	_, err := e.svc.PlaceBooking(ctx, c, e.at("2026-11-17", "morning", e.item(e.c.JacketM)))
	wantShort(t, err)

	conflicts, err := e.inv.OverdueConflicts(ctx, e.staff, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(conflicts, func(x inventoryapp.Conflict) bool { return x.Overdue.AssetID == asset })
	if i < 0 || len(conflicts[i].Endangered) != 1 || conflicts[i].Endangered[0].BookingRef != string(next.ID) {
		t.Fatalf("conflicts = %+v; want the next booking reported", conflicts)
	}
	customer := e.f.User("d", identity.RoleCustomer)
	if _, err := e.inv.OverdueConflicts(ctx, customer, time.Hour); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("customer reading inventory conflicts: err = %v", err)
	}
}

// waitForLockWait returns once a session of this database waits on a row lock.
func (e *env) waitForLockWait() {
	e.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var n int
		if err := e.f.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n); err != nil {
			e.t.Fatal(err)
		}
		if n > 0 {
			return
		}
	}
	e.t.Fatal("no session is waiting on a lock")
}

// The job listed the booking, then a confirmation took the row first. The job
// must leave the booking and its open change request exactly as they are.
func TestExpireLosingRaceToConfirmLeavesChangeRequestOpen(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	e.f.Assets(e.c.JacketL, 1)
	a := e.f.User("a", identity.RoleCustomer)
	booked := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	m, err := e.svc.RequestModification(ctx, a, booked.ID, rentalsapp.ModificationInput{
		Changes: []rentalsapp.LineChangeInput{{LineID: "L1", Line: e.item(e.c.JacketL)}}, Reason: "size",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.QuoteModification(ctx, a, m.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(pgtest.Now.Add(31 * time.Minute))

	tx, err := e.f.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM bookings WHERE id = $1 FOR UPDATE`, booked.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.ExpireHolds(ctx, 1000)
		done <- err
	}()
	e.waitForLockWait()
	if _, err := tx.Exec(ctx, `UPDATE bookings SET status = 'CONFIRMED', confirmed_at = $2, version = version + 1 WHERE id = $1`,
		booked.ID, pgtest.Now.Add(29*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	var status string
	if err := e.f.Pool.QueryRow(ctx, `SELECT status FROM booking_modifications WHERE id = $1`, m.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Booking(ctx, a, booked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status != string(rentals.ModQuoted) || got.Status != rentals.StatusConfirmed || got.OpenModification != m.ID || len(e.active(booked.ID)) != 2 {
		t.Fatalf("modification %s, booking %s open %q, active %d; the job must change nothing", status, got.Status, got.OpenModification, len(e.active(booked.ID)))
	}
}

// One booking the job cannot process must not keep the others blocking stock.
func TestExpireHoldsContinuesPastAFailure(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 2)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.f.User("b", identity.RoleCustomer)
	broken := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.clock.Set(pgtest.Now.Add(time.Minute))
	healthy := e.place(b, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	setOpenModification := func(v *string) {
		t.Helper()
		if _, err := e.f.Pool.Exec(ctx, `UPDATE bookings SET open_modification_id = $2 WHERE id = $1`, broken.ID, v); err != nil {
			t.Fatal(err)
		}
	}
	missing := "no-such-modification"
	setOpenModification(&missing)
	t.Cleanup(func() { setOpenModification(nil) })

	e.clock.Set(pgtest.Now.Add(time.Hour))
	if _, err := e.svc.ExpireHolds(ctx, 1000); err == nil {
		t.Fatal("the failed booking must be reported")
	}
	if got, _ := e.svc.Booking(ctx, b, healthy.ID); got.Status != rentals.StatusExpired {
		t.Fatalf("healthy booking = %s, want it expired", got.Status)
	}
}

// Reschedule cannot see retired assets among its candidates, yet it must
// still release what the booking holds on them.
func TestRescheduleReleasesAllocationOnRetiredAsset(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 2)
	a := e.f.User("a", identity.RoleCustomer)
	mgr := e.f.User("mgr", identity.RoleInventoryManager)
	booked := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	retired := e.active(booked.ID)[0].AssetID
	if _, err := e.inv.ChangeStatus(ctx, mgr, retired, inventoryapp.StatusInput{Action: inventory.ActionRetire, Reason: "rách không sửa được"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Reschedule(ctx, a, booked.ID, rentalsapp.RescheduleInput{PickupDate: pgtest.Date(t, "2026-11-17"), SlotID: "morning"}); err != nil {
		t.Fatal(err)
	}
	active := e.active(booked.ID)
	if len(active) != 1 || active[0].AssetID == retired {
		t.Fatalf("active allocations = %+v, want one on the other jacket", active)
	}
}

// A rented asset changes only through its booking, so booking, allocation and
// asset cannot drift apart: loss goes missing-pending, then confirmed.
func TestLossGoesThroughTheBooking(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	mgr := e.f.User("mgr", identity.RoleInventoryManager)
	owner := e.f.User("owner", identity.RoleAdmin)
	booked := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.handOver(booked, pgtest.Local(t, "2026-11-10 08:30"))

	if _, err := e.inv.ChangeStatus(ctx, mgr, asset, inventoryapp.StatusInput{Action: inventory.ActionReportMissing, Reason: "không thấy"}); !errors.Is(err, inventory.ErrAssetOnRental) {
		t.Fatalf("marking a rented asset missing directly: err = %v", err)
	}

	e.clock.Set(pgtest.Local(t, "2026-11-11 12:00"))
	if _, err := e.svc.ReportMissing(ctx, a, booked.ID, asset); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("customer reporting missing: err = %v", err)
	}
	if _, err := e.svc.ReportMissing(ctx, e.staff, booked.ID, asset); err != nil {
		t.Fatal(err)
	}
	if e.assetStatus(asset) != inventory.AssetRented {
		t.Fatal("a missing-pending asset stays RENTED until the loss is confirmed")
	}
	if _, err := e.svc.ConfirmLost(ctx, owner, booked.ID, asset, "hết hạn xác minh"); !errors.Is(err, rentals.ErrVerificationWindowOpen) {
		t.Fatalf("confirming inside the window: err = %v", err)
	}

	e.clock.Set(pgtest.Local(t, "2026-11-13 12:00"))
	if _, err := e.svc.ConfirmLost(ctx, e.staff, booked.ID, asset, "hết hạn xác minh"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff without rental.confirm_lost: err = %v", err)
	}
	got, err := e.svc.ConfirmLost(ctx, owner, booked.ID, asset, "hết hạn xác minh")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != rentals.StatusReturnInspection || e.assetStatus(asset) != inventory.AssetMissing || e.allocations(booked.ID)[0].Status != inventory.AllocationClosed {
		t.Fatalf("booking %s, asset %s, allocation %s", got.Status, e.assetStatus(asset), e.allocations(booked.ID)[0].Status)
	}
}

// A quoted size change holds the new size; rejecting it frees the hold and
// leaves the original allocation alone.
func TestModificationHoldBlocksUntilRejected(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	e.f.Assets(e.c.JacketL, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.f.User("b", identity.RoleCustomer)
	booked := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	original := e.active(booked.ID)[0]

	m, err := e.svc.RequestModification(ctx, a, booked.ID, rentalsapp.ModificationInput{
		Changes: []rentalsapp.LineChangeInput{{LineID: "L1", Line: e.item(e.c.JacketL)}}, Reason: "size",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.QuoteModification(ctx, b, m.ID); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("another customer quoting: err = %v", err)
	}
	if _, err := e.svc.QuoteModification(ctx, a, m.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.active(booked.ID); len(got) != 2 {
		t.Fatalf("quote must add a tentative hold next to the original, got %d", len(got))
	}
	_, err = e.svc.PlaceBooking(ctx, b, e.at("2026-11-10", "morning", e.item(e.c.JacketL)))
	wantShort(t, err)

	if _, err := e.svc.EndModification(ctx, a, m.ID, rentalsapp.ModificationRejected, "no"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("customer rejecting: err = %v", err)
	}
	if _, err := e.svc.EndModification(ctx, e.staff, m.ID, rentalsapp.ModificationRejected, "size L reserved for an event"); err != nil {
		t.Fatal(err)
	}
	active := e.active(booked.ID)
	if len(active) != 1 || active[0].ID != original.ID {
		t.Fatalf("after reject active = %+v, want only the original", active)
	}
	got, _ := e.svc.Booking(ctx, a, booked.ID)
	if got.OpenModification != "" {
		t.Fatal("rejected change must close on the booking")
	}
	e.place(b, e.at("2026-11-10", "morning", e.item(e.c.JacketL)))
}

func TestExpiredModificationHoldIsReleased(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	e.f.Assets(e.c.JacketL, 1)
	a := e.f.User("a", identity.RoleCustomer)
	booked := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	m, err := e.svc.RequestModification(ctx, e.staff, booked.ID, rentalsapp.ModificationInput{
		Changes: []rentalsapp.LineChangeInput{{LineID: "L1", Line: e.item(e.c.JacketL)}}, Reason: "customer phoned",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.QuoteModification(ctx, e.staff, m.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(pgtest.Now.Add(31 * time.Minute))
	if n, err := e.svc.ExpireModificationHolds(ctx, 1000); err != nil || n < 1 {
		t.Fatalf("expire = %d, %v", n, err)
	}
	if n, err := e.svc.ExpireModificationHolds(ctx, 1000); err != nil || n != 0 {
		t.Fatalf("second run = %d, %v; must be a no-op", n, err)
	}
	if got := e.active(booked.ID); len(got) != 1 || got[0].VariantID != e.c.JacketM {
		t.Fatalf("after expiry active = %+v", got)
	}
}
