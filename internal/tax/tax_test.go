package tax_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/tax"
)

func vnd(v int64) money.Money {
	m, err := money.New(v, money.VND)
	if err != nil {
		panic(err)
	}
	return m
}

var (
	loc, _ = businesstime.LoadLocation()
	jan1   = time.Date(2025, 12, 31, 17, 0, 0, 0, time.UTC) // 2026-01-01 00:00 +07
	mid    = time.Date(2026, 6, 1, 3, 0, 0, 0, time.UTC)
)

func principal(t *testing.T, role identity.Role) identity.Principal {
	t.Helper()
	p, err := identity.NewPrincipal(identity.UserID(role+"1"), []identity.Role{role}, identity.ReferencePermissionMatrix())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// profile is the configuration of decision D41.
func profile() tax.Profile {
	return tax.Profile{
		Version: "tax-2026", TaxpayerType: tax.IndividualBusiness,
		AnnualRevenueThreshold: vnd(1_000_000_000), ThresholdEffectiveFrom: businesstime.Date{Year: 2026, Month: 1, Day: 1},
		WarningPercents: []int{70, 85, 95}, EffectiveFrom: jan1,
	}
}

func history(t *testing.T) tax.History {
	t.Helper()
	var h tax.History
	if _, err := h.Add(principal(t, identity.RoleAdmin), profile(), mid); err != nil {
		t.Fatal(err)
	}
	return h
}

func entry(t *testing.T, id string, cat tax.Category, list, discount int64, at time.Time) tax.RevenueEntry {
	t.Helper()
	e, err := tax.NewEntry(tax.RevenueEntry{
		ID: tax.EntryID(id), Source: tax.SourceBooking, BookingRef: "bk-" + id, SourceRef: "ch-" + id, Category: cat,
		List: vnd(list), Discount: vnd(discount), Net: vnd(list - discount), RecognizedAt: at, ProfileVersion: "tax-2026",
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestProfileHistory(t *testing.T) {
	admin, finance := principal(t, identity.RoleAdmin), principal(t, identity.RoleFinance)
	var h tax.History
	if _, err := h.Add(finance, profile(), mid); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("finance changing the tax profile err = %v", err)
	}
	// The first version may start in the past, so bookings that already
	// ended get recognised.
	if closed, err := h.Add(admin, profile(), mid); err != nil || closed != nil {
		t.Fatalf("first version: %v, %v", closed, err)
	}
	next := profile()
	next.Version, next.EffectiveFrom = "tax-2026-07", mid.Add(-time.Hour)
	if _, err := h.Add(admin, next, mid); !errors.Is(err, tax.ErrRetroactive) {
		t.Fatalf("retroactive version err = %v", err)
	}
	next.EffectiveFrom = mid.Add(24 * time.Hour)
	next.WarningPercents = []int{80, 90}
	closed, err := h.Add(admin, next, mid)
	if err != nil || closed == nil || closed.Version != "tax-2026" || !closed.EffectiveUntil.Equal(next.EffectiveFrom) {
		t.Fatalf("second version closes the first: %+v, %v", closed, err)
	}
	if got, _ := h.At(mid); got.Version != "tax-2026" {
		t.Fatalf("profile now = %+v", got)
	}
	if got, _ := h.At(mid.Add(48 * time.Hour)); got.Version != "tax-2026-07" {
		t.Fatalf("profile later = %+v", got)
	}
	if _, err := h.At(jan1.Add(-time.Second)); !errors.Is(err, tax.ErrNoProfile) {
		t.Fatalf("before any profile err = %v", err)
	}
	dup := next
	dup.EffectiveFrom = mid.Add(72 * time.Hour)
	if _, err := h.Add(admin, dup, mid); !errors.Is(err, tax.ErrProfileInvalid) {
		t.Fatalf("duplicate version err = %v", err)
	}
}

func TestProfileValidation(t *testing.T) {
	for name, change := range map[string]func(*tax.Profile){
		"no version":          func(p *tax.Profile) { p.Version = " " },
		"other taxpayer":      func(p *tax.Profile) { p.TaxpayerType = "ENTERPRISE" },
		"zero threshold":      func(p *tax.Profile) { p.AnnualRevenueThreshold = vnd(0) },
		"no marks":            func(p *tax.Profile) { p.WarningPercents = nil },
		"descending marks":    func(p *tax.Profile) { p.WarningPercents = []int{85, 70} },
		"mark at 100":         func(p *tax.Profile) { p.WarningPercents = []int{70, 100} },
		"no threshold date":   func(p *tax.Profile) { p.ThresholdEffectiveFrom = businesstime.Date{} },
		"no effective period": func(p *tax.Profile) { p.EffectiveFrom = time.Time{} },
	} {
		p := profile()
		change(&p)
		if err := p.Validate(); !errors.Is(err, tax.ErrProfileInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if err := profile().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDepositsAreNeverRevenue(t *testing.T) {
	for _, c := range []tax.Category{tax.CatRentalSecurityDeposit, tax.CatBookingDeposit} {
		_, err := tax.NewEntry(tax.RevenueEntry{ID: "x", Source: tax.SourceBooking, BookingRef: "b", SourceRef: "s", Category: c,
			List: vnd(1), Discount: vnd(0), Net: vnd(1), RecognizedAt: jan1, ProfileVersion: "v"})
		if !errors.Is(err, tax.ErrNotRevenue) {
			t.Errorf("%s err = %v", c, err)
		}
	}
}

// ledger is a booking ledger with 300,000đ list rent less a 30,000đ
// discount, a 500,000đ security deposit and a 200,000đ reservation.
func ledger(t *testing.T) *payments.Ledger {
	t.Helper()
	l, err := payments.OpenLedger(payments.OpenRequest{
		BookingRef: "bk-1", Rent: vnd(270_000), Security: vnd(500_000), Reservation: vnd(200_000),
		RentChargeID: "ch-rent", SecurityChargeID: "ch-dep", ReservationChargeID: "ch-res",
		Actor: identity.SystemActor("booking"), Now: jan1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestSyncRecognisesAnEndedBooking(t *testing.T) {
	h := history(t)
	l := ledger(t)
	staff := identity.UserActor("staff1")
	if _, err := l.AddFee("ch-late", payments.ChargeLateFee, vnd(25_000), "trễ 5 giờ", nil, staff, mid.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	in := tax.BookingRevenue{Ledger: l, EndedAt: mid, RentList: vnd(300_000)}
	entries, err := tax.SyncBooking(in, h)
	if err != nil {
		t.Fatal(err)
	}
	// Rent and the late fee only: the security and reservation deposits are
	// held money, so the reservation is not counted a second time.
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	rent, late := entries[0], entries[1]
	if rent.ID != tax.EntryOf("ch-rent") || rent.Category != tax.CatRentalRevenue || rent.List.Amount() != 300_000 ||
		rent.Discount.Amount() != 30_000 || rent.Net.Amount() != 270_000 || !rent.RecognizedAt.Equal(mid) || rent.ProfileVersion != "tax-2026" {
		t.Fatalf("rent entry = %+v", rent)
	}
	if late.Category != tax.CatLateReturnCharge || late.Net.Amount() != 25_000 || late.Discount.Amount() != 0 {
		t.Fatalf("late fee entry = %+v", late)
	}
	in.Existing = entries
	if again, err := tax.SyncBooking(in, h); err != nil || len(again) != 0 {
		t.Fatalf("second sync = %+v, %v", again, err)
	}
}

func TestSyncFollowsLaterLedgerChanges(t *testing.T) {
	h := history(t)
	l := ledger(t)
	staff := identity.UserActor("fin1")
	if _, err := l.AddFee("ch-dmg", payments.ChargeDamageFee, vnd(100_000), "rách tay áo", nil, staff, mid.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	first, err := tax.SyncBooking(tax.BookingRevenue{Ledger: l, EndedAt: mid, RentList: vnd(300_000)}, h)
	if err != nil || len(first) != 2 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	// The fee is waived three days after the booking ended: the reversal
	// lands on that day, the original stays in its own.
	waived := mid.Add(72 * time.Hour)
	if err := l.ReverseCharge("ch-dmg", "khách chứng minh vết rách có sẵn", staff, waived); err != nil {
		t.Fatal(err)
	}
	more, err := tax.SyncBooking(tax.BookingRevenue{Ledger: l, EndedAt: mid, RentList: vnd(300_000), Existing: first}, h)
	if err != nil || len(more) != 1 {
		t.Fatalf("after waiver = %+v, %v", more, err)
	}
	r := more[0]
	if r.ID != tax.ReversalOf("ch-dmg") || r.Reverses != tax.EntryOf("ch-dmg") || r.Net.Amount() != -100_000 || !r.RecognizedAt.Equal(waived) {
		t.Fatalf("reversal = %+v", r)
	}
	s := tax.Summarize(append(first, more...), profile(), 2026, loc)
	if s.Revenue.Amount() != 270_000 || s.ByCategory[tax.CatDamageCharge].Amount() != 0 {
		t.Fatalf("summary = %+v", s)
	}
}

func TestSyncSkipsWhatWasReversedBeforeTheEnd(t *testing.T) {
	h := history(t)
	l := ledger(t)
	// A cancelled booking: the rent is reversed when it is settled, so only
	// the cancellation fee is revenue.
	if _, _, err := l.SettleCancellation(payments.CancellationRequest{
		Fee: vnd(81_000), Basis: "hủy trước 24 giờ", FeeChargeID: "ch-cxl", ResolutionID: "res-1",
		Actor: identity.SystemActor("booking"), Now: mid,
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := tax.SyncBooking(tax.BookingRevenue{Ledger: l, EndedAt: mid, RentList: vnd(300_000)}, h)
	if err != nil || len(entries) != 1 || entries[0].Category != tax.CatCancellationCharge || entries[0].Net.Amount() != 81_000 {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
}

func TestSyncWaitsForAProfile(t *testing.T) {
	entries, err := tax.SyncBooking(tax.BookingRevenue{Ledger: ledger(t), EndedAt: mid, RentList: vnd(300_000)}, tax.History{})
	if err != nil || len(entries) != 0 {
		t.Fatalf("without a profile = %+v, %v", entries, err)
	}
}

func TestThresholdMarksAndBusinessYear(t *testing.T) {
	p := profile()
	for _, c := range []struct {
		revenue int64
		level   tax.Level
		mark    int
		percent int
	}{
		{0, tax.LevelBelow, 0, 0},
		{699_999_999, tax.LevelBelow, 0, 69},
		{700_000_000, tax.LevelWarning, 70, 70},
		{850_000_000, tax.LevelWarning, 85, 85},
		{999_999_999, tax.LevelWarning, 95, 99},
		{1_000_000_000, tax.LevelWarning, 95, 100},
		{1_000_000_001, tax.LevelExceeded, 95, 100},
	} {
		var es []tax.RevenueEntry
		if c.revenue > 0 {
			es = append(es, entry(t, "1", tax.CatRentalRevenue, c.revenue, 0, mid))
		}
		s := tax.Summarize(es, p, 2026, loc)
		if s.Level != c.level || s.Mark != c.mark || s.Percent != c.percent {
			t.Errorf("%d: level %s mark %d percent %d", c.revenue, s.Level, s.Mark, s.Percent)
		}
	}
	// 2026-12-31 18:00 UTC is 2027-01-01 01:00 in Vietnam.
	newYear := entry(t, "2", tax.CatRentalRevenue, 200_000_000, 0, time.Date(2026, 12, 31, 18, 0, 0, 0, time.UTC))
	if s := tax.Summarize([]tax.RevenueEntry{newYear}, p, 2026, loc); !s.Revenue.IsZero() {
		t.Fatalf("2026 = %+v; the late entry belongs to 2027", s)
	}
}

func TestManualEntries(t *testing.T) {
	h := history(t)
	finance, staff := principal(t, identity.RoleFinance), principal(t, identity.RoleRentalStaff)
	in := tax.ManualInput{ID: "m1", Category: tax.CatRentalRevenue, Amount: vnd(1_500_000),
		Date: businesstime.Date{Year: 2026, Month: 5, Day: 20}, Channel: "Hội chợ cosplay", DocumentRef: "PT-0012"}
	if _, err := tax.NewManualEntry(staff, in, h, loc, mid); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff err = %v", err)
	}
	e, err := tax.NewManualEntry(finance, in, h, loc, mid)
	if err != nil {
		t.Fatal(err)
	}
	if e.Source != tax.SourceManual || e.BookingRef != "" || e.RecordedBy == nil || e.ProfileVersion != "tax-2026" ||
		businesstime.DateOf(e.RecognizedAt, loc) != in.Date {
		t.Fatalf("entry = %+v", e)
	}
	future := in
	future.Date = businesstime.Date{Year: 2026, Month: 6, Day: 2}
	if _, err := tax.NewManualEntry(finance, future, h, loc, mid); !errors.Is(err, tax.ErrRecognitionFuture) {
		t.Fatalf("future err = %v", err)
	}
	early := in
	early.Date = businesstime.Date{Year: 2025, Month: 12, Day: 1}
	if _, err := tax.NewManualEntry(finance, early, h, loc, mid); !errors.Is(err, tax.ErrNoProfile) {
		t.Fatalf("before the profile err = %v", err)
	}
	deposit := in
	deposit.Category = tax.CatRentalSecurityDeposit
	if _, err := tax.NewManualEntry(finance, deposit, h, loc, mid); !errors.Is(err, tax.ErrNotRevenue) {
		t.Fatalf("deposit err = %v", err)
	}
	noChannel := in
	noChannel.Channel = " "
	if _, err := tax.NewManualEntry(finance, noChannel, h, loc, mid); !errors.Is(err, tax.ErrEntryInvalid) {
		t.Fatalf("no channel err = %v", err)
	}

	r, err := tax.ReverseManual(finance, e, false, "m1-rev", "nhập trùng", mid)
	if err != nil || r.Net.Amount() != -1_500_000 || r.Reverses != "m1" || r.RecordedBy == nil {
		t.Fatalf("reversal = %+v, %v", r, err)
	}
	if _, err := tax.ReverseManual(finance, e, true, "m1-rev2", "lần nữa", mid); !errors.Is(err, tax.ErrAlreadyReversed) {
		t.Fatalf("second reversal err = %v", err)
	}
	if _, err := tax.ReverseManual(finance, r, false, "x", "đảo bút toán đảo", mid); !errors.Is(err, tax.ErrReversalReversed) {
		t.Fatalf("reversing a reversal err = %v", err)
	}
	booking := entry(t, "b1", tax.CatRentalRevenue, 300_000, 0, mid)
	if _, err := tax.ReverseManual(finance, booking, false, "x", "sai", mid); !errors.Is(err, tax.ErrNotManual) {
		t.Fatalf("reversing a booking entry err = %v", err)
	}
}
