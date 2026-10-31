package pgtest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/catalog"
	"webee_rentor/internal/catalog/catalogpg"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identitypg"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/inventory/inventorypg"
	"webee_rentor/internal/money"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
)

const day = 24 * time.Hour

// Now is the default fixture time: Sunday 2026-11-01 09:00 in
// Asia/Ho_Chi_Minh.
var Now = time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)

// VND builds an amount for fixtures.
func VND(v int64) money.Money {
	m, err := money.New(v, money.VND)
	if err != nil {
		panic(err)
	}
	return m
}

// Policy is a fixture rental policy; real values are pending owner
// approval and are never taken from here.
func Policy() rentals.Policy {
	return rentals.Policy{
		Version: "fixture-v1", HoldTTL: 30 * time.Minute, MinLeadTime: 2 * time.Hour, MaxAdvance: 90 * day,
		MaxLinesPerBooking: 10, MaxQuantityPerLine: 5,
		Confirmation: rentals.ConfirmationRule{Mode: rentals.ConfirmByStaff, Funds: rentals.FundsRequirement{Kind: rentals.FundsReservationDeposit}},
		Handover:     rentals.FundsRequirement{Kind: rentals.FundsFullRentAndSecurity}, CustomerRescheduleCutoff: day,
		Buffers: rentals.BufferPolicy{
			Default: rentals.Buffer{Preparation: 2 * time.Hour, Turnaround: day},
			ByClass: map[string]rentals.Buffer{"accessory": {Preparation: time.Hour, Turnaround: 4 * time.Hour}},
		},
		Reservation: rentals.ReservationPolicy{Kind: rentals.ReservationRentPercent, RentPercent: 30, Treatment: rentals.ReservationAppliedToRent},
		Cancellation: rentals.CancellationPolicy{
			Tiers: []rentals.FeeTier{{MinNotice: 7 * day, FeePercent: 0}, {MinNotice: 3 * day, FeePercent: 10},
				{MinNotice: day, FeePercent: 30}, {MinNotice: 0, FeePercent: 50}},
			NoShowAfter: 2 * time.Hour, NoShowFeePercent: 70,
		},
		LateFee: rentals.LateFeePolicy{
			Grace: 30 * time.Minute, GraceRequiresNoConflict: true,
			Tiers: []rentals.LateFeeTier{{UpTo: 4 * time.Hour, Percent: 25}, {UpTo: 8 * time.Hour, Percent: 50},
				{UpTo: 12 * time.Hour, Percent: 75}, {UpTo: day, Percent: 100}},
			ExtraPeriod: day, ExtraPercent: 100,
		},
		Loss:         rentals.LossPolicy{VerificationWindow: 48 * time.Hour},
		FeeApproval:  rentals.FeeApprovalPolicy{ManagerApprovalAbove: VND(500_000)},
		Modification: rentals.ModificationPolicy{SelfServiceCutoff: day, SelfServiceSizeChanges: 1, HoldTTL: 30 * time.Minute},
		Delivery:     rentals.DeliveryPolicy{OutboundLead: time.Hour, ReturnTransit: 3 * time.Hour, ComplaintWindow: 2 * time.Hour},
	}
}

// SeedConfig installs the fixture policies and store hours: Monday to
// Saturday, slots 09:00–11:00 and 15:00–17:00, closed 2026-11-20.
func SeedConfig(ctx context.Context, p *pgxpool.Pool) error {
	return postgres.InTx(ctx, p, func(tx pgx.Tx) error {
		system := identity.SystemActor("pgtest")
		if err := postgres.SaveDocument(ctx, tx, postgres.DocRentalPolicy, Policy().Version, Policy(), system, Now, true); err != nil {
			return err
		}
		if err := postgres.SaveDocument(ctx, tx, postgres.DocAllocationPolicy, "fixture-v1", inventory.AllocationPolicy{}, system, Now, true); err != nil {
			return err
		}
		if err := postgres.SaveDocument(ctx, tx, postgres.DocPaymentApproval, "fixture-v1", payments.ApprovalPolicy{DistinctApproverAbove: VND(500_000)}, system, Now, true); err != nil {
			return err
		}
		morning, _ := businesstime.ParseTimeOfDay("09:00")
		late, _ := businesstime.ParseTimeOfDay("11:00")
		afternoon, _ := businesstime.ParseTimeOfDay("15:00")
		evening, _ := businesstime.ParseTimeOfDay("17:00")
		slots := []rentals.Slot{{ID: "morning", Start: morning, End: late}, {ID: "afternoon", Start: afternoon, End: evening}}
		weekly := map[time.Weekday][]rentals.Slot{}
		for d := time.Monday; d <= time.Saturday; d++ {
			weekly[d] = slots
		}
		if err := rentalspg.ReplaceSlots(ctx, tx, rentalspg.SlotCounter, weekly); err != nil {
			return err
		}
		closed, _ := businesstime.ParseDate("2026-11-20")
		return rentalspg.AddClosure(ctx, tx, closed, "fixture holiday")
	})
}

// Fixture creates rows with a prefix unique to one test, so tests sharing
// the package database never collide.
type Fixture struct {
	t      testing.TB
	Pool   *pgxpool.Pool
	Prefix string
}

// New starts a fixture for t, skipping without a database.
func New(t testing.TB) *Fixture {
	t.Helper()
	return &Fixture{t: t, Pool: Pool(t), Prefix: "t" + randomHex(4)}
}

func (f *Fixture) id(s string) string { return f.Prefix + "-" + s }

// User inserts a user with roles and returns its principal.
func (f *Fixture) User(name string, roles ...identity.Role) identity.Principal {
	f.t.Helper()
	id := identity.UserID(f.id(name))
	ctx := context.Background()
	if _, err := f.Pool.Exec(ctx, `INSERT INTO users (id, email, status, created_at, updated_at) VALUES ($1, $2, 'ACTIVE', $3, $3)`,
		id, strings.ToLower(string(id))+"@example.test", Now); err != nil {
		f.t.Fatal(err)
	}
	for _, r := range roles {
		if _, err := f.Pool.Exec(ctx, `INSERT INTO user_roles (user_id, role, granted_at, granted_by_kind, granted_by_id)
			VALUES ($1, $2, $3, 'system', 'pgtest')`, id, r, Now); err != nil {
			f.t.Fatal(err)
		}
	}
	// Resolve through the stored matrix, as requests will.
	m, _, err := identitypg.ActiveMatrix(ctx, f.Pool)
	if err != nil {
		f.t.Fatal(err)
	}
	p, err := identitypg.LoadPrincipal(ctx, f.Pool, id, m)
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

// Catalog is the fixture catalog: jacket (M, L), pants (M, L) and a belt
// only rentable in the bundle "set" (jacket + pants + belt), with 1-day and
// 3-day packages.
type Catalog struct {
	OneDay, ThreeDays                 catalog.RentalPackage
	Jacket, Pants, Belt               catalog.Item
	Set                               catalog.Bundle
	JacketM, JacketL, PantsM, BeltStd catalog.VariantID
}

// Catalog saves the fixture catalog.
func (f *Fixture) Catalog() Catalog {
	f.t.Helper()
	ctx := context.Background()
	c := Catalog{
		OneDay:    catalog.RentalPackage{ID: catalog.PackageID(f.id("p1d")), Code: f.id("1D"), Label: "1 ngày", Duration: day, Status: catalog.StatusActive},
		ThreeDays: catalog.RentalPackage{ID: catalog.PackageID(f.id("p3d")), Code: f.id("3D"), Label: "3 ngày", Duration: 3 * day, Status: catalog.StatusActive},
		JacketM:   catalog.VariantID(f.id("jacket-m")), JacketL: catalog.VariantID(f.id("jacket-l")),
		PantsM: catalog.VariantID(f.id("pants-m")), BeltStd: catalog.VariantID(f.id("belt")),
	}
	c.Jacket = catalog.Item{
		ID: catalog.ItemID(f.id("jacket")), Slug: f.id("ao-khoac"), Name: "Áo khoác", Status: catalog.StatusActive, IndividuallyRentable: true,
		Variants: []catalog.Variant{{ID: c.JacketM, SKU: f.id("JK-M"), Size: "M", Status: catalog.StatusActive},
			{ID: c.JacketL, SKU: f.id("JK-L"), Size: "L", Status: catalog.StatusActive}},
		Prices:          catalog.Prices{c.OneDay.ID: VND(50_000), c.ThreeDays.ID: VND(130_000)},
		SecurityDeposit: VND(300_000), DailyRate: VND(50_000), BufferClass: "garment", Version: 1,
	}
	c.Pants = catalog.Item{
		ID: catalog.ItemID(f.id("pants")), Slug: f.id("quan"), Name: "Quần", Status: catalog.StatusActive, IndividuallyRentable: true,
		Variants:        []catalog.Variant{{ID: c.PantsM, SKU: f.id("PT-M"), Size: "M", Status: catalog.StatusActive}},
		Prices:          catalog.Prices{c.OneDay.ID: VND(30_000)},
		SecurityDeposit: VND(150_000), DailyRate: VND(30_000), BufferClass: "garment", Version: 1,
	}
	c.Belt = catalog.Item{
		ID: catalog.ItemID(f.id("belt")), Slug: f.id("that-lung"), Name: "Thắt lưng", Status: catalog.StatusActive,
		Variants:        []catalog.Variant{{ID: c.BeltStd, SKU: f.id("BL"), Status: catalog.StatusActive}},
		SecurityDeposit: VND(100_000), DailyRate: VND(10_000), BufferClass: "accessory", Version: 1,
	}
	c.Set = catalog.Bundle{
		ID: catalog.BundleID(f.id("set")), Slug: f.id("bo-naruto"), Name: "Bộ Naruto", Status: catalog.StatusActive,
		Components: []catalog.BundleComponent{
			{Key: "jacket", ItemID: c.Jacket.ID, Options: []catalog.VariantID{c.JacketM, c.JacketL}, Quantity: 1},
			{Key: "pants", ItemID: c.Pants.ID, Options: []catalog.VariantID{c.PantsM}, Quantity: 1},
			{Key: "belt", ItemID: c.Belt.ID, Options: []catalog.VariantID{c.BeltStd}, Quantity: 1},
		},
		Prices: catalog.Prices{c.OneDay.ID: VND(80_000), c.ThreeDays.ID: VND(200_000)}, Version: 1,
	}
	err := postgres.InTx(ctx, f.Pool, func(tx pgx.Tx) error {
		for _, p := range []catalog.RentalPackage{c.OneDay, c.ThreeDays} {
			if err := catalogpg.SavePackage(ctx, tx, p, Now); err != nil {
				return err
			}
		}
		items := map[catalog.ItemID]catalog.Item{c.Jacket.ID: c.Jacket, c.Pants.ID: c.Pants, c.Belt.ID: c.Belt}
		for _, it := range items {
			if err := catalogpg.SaveItem(ctx, tx, it, Now); err != nil {
				return err
			}
		}
		return catalogpg.SaveBundle(ctx, tx, c.Set, items, Now)
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

// Assets registers n READY assets of the variant and returns their IDs.
func (f *Fixture) Assets(variant catalog.VariantID, n int) []inventory.AssetID {
	f.t.Helper()
	ctx := context.Background()
	staff := identity.UserActor("pgtest-staff")
	var out []inventory.AssetID
	err := postgres.InTx(ctx, f.Pool, func(tx pgx.Tx) error {
		for i := 0; i < n; i++ {
			id := inventory.AssetID(f.id(string(variant) + "-" + randomHex(3)))
			a, intake, err := inventory.NewAsset(id, string(id), variant, staff, Now.Add(-day))
			if err != nil {
				return err
			}
			if err := inventorypg.InsertAsset(ctx, tx, a, intake); err != nil {
				return err
			}
			c, err := a.PassInspection(staff, Now.Add(-day), inventory.InspectionChecklist{Completed: true})
			if err != nil {
				return err
			}
			if err := inventorypg.UpdateAsset(ctx, tx, a, c); err != nil {
				return err
			}
			out = append(out, id)
		}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// Date parses a fixture date.
func Date(t testing.TB, s string) businesstime.Date {
	t.Helper()
	d, err := businesstime.ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// Local returns the UTC instant of a local Asia/Ho_Chi_Minh wall time.
func Local(t testing.TB, s string) time.Time {
	t.Helper()
	loc, err := businesstime.LoadLocation()
	if err != nil {
		t.Fatal(err)
	}
	v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v.UTC()
}
