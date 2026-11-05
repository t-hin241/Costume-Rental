package taxapp_test

import (
	"context"
	"errors"
	"testing"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/tax"
	"webee_rentor/internal/tax/taxapp"
	"webee_rentor/internal/tax/taxpg"
)

func TestMain(m *testing.M) { pgtest.Main(m, nil) }

// The whole flow runs in one test: the profile history is global to the
// database, and the first version is the only one that may start in the past.
func TestProfilesThresholdAndManualRevenue(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	loc, err := businesstime.LoadLocation()
	if err != nil {
		t.Fatal(err)
	}
	clock := pgtest.NewClock(pgtest.Local(t, "2026-06-15 10:00"))
	svc := taxapp.New(f.Pool, loc, taxapp.WithClock(clock.Now))
	admin, finance := f.User("owner", identity.RoleAdmin), f.User("finance", identity.RoleFinance)
	staff := f.User("staff", identity.RoleRentalStaff)

	// Without a profile the watch says so instead of guessing a threshold.
	if v, err := svc.Year(ctx, finance, 0); err != nil || v.Year != 2026 || v.Summary != nil {
		t.Fatalf("year without a profile = %+v, %v", v, err)
	}
	in := tax.ManualInput{Category: tax.CatRentalRevenue, Amount: pgtest.VND(700_000_000), Date: pgtest.Date(t, "2026-03-02"),
		Channel: "Hội chợ cosplay", DocumentRef: "PT-0001"}
	if _, err := svc.AddManualEntry(ctx, finance, in); !errors.Is(err, tax.ErrNoProfile) {
		t.Fatalf("manual revenue without a profile: err = %v", err)
	}

	profile := taxapp.ProfileInput{
		Version: "tax-2026", AnnualRevenueThreshold: pgtest.VND(1_000_000_000), ThresholdEffectiveFrom: pgtest.Date(t, "2026-01-01"),
		WarningPercents: []int{70, 85, 95}, EffectiveFrom: pgtest.Date(t, "2026-01-01"),
	}
	if _, err := svc.AddProfile(ctx, finance, profile); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("finance adding a profile: err = %v", err)
	}
	if _, err := svc.AddProfile(ctx, admin, profile); err != nil {
		t.Fatal(err)
	}
	retro := profile
	retro.Version, retro.EffectiveFrom = "tax-2026-b", pgtest.Date(t, "2026-06-01")
	if _, err := svc.AddProfile(ctx, admin, retro); !errors.Is(err, tax.ErrRetroactive) {
		t.Fatalf("retroactive version: err = %v", err)
	}

	if _, err := svc.AddManualEntry(ctx, staff, in); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff recording revenue: err = %v", err)
	}
	first, err := svc.AddManualEntry(ctx, finance, in)
	if err != nil {
		t.Fatal(err)
	}
	v, err := svc.Year(ctx, finance, 2026)
	if err != nil || v.Summary == nil || v.Summary.Level != tax.LevelWarning || v.Summary.Mark != 70 || v.Summary.Percent != 70 {
		t.Fatalf("at 70%% = %+v, %v", v.Summary, err)
	}
	more := in
	more.Amount, more.Date, more.Channel = pgtest.VND(310_000_000), pgtest.Date(t, "2026-06-15"), "Facebook"
	second, err := svc.AddManualEntry(ctx, finance, more)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := svc.Year(ctx, finance, 2026); v.Summary.Level != tax.LevelExceeded || v.Summary.Revenue.Amount() != 1_010_000_000 {
		t.Fatalf("above the threshold = %+v", v.Summary)
	}
	future := more
	future.Date = pgtest.Date(t, "2026-06-16")
	if _, err := svc.AddManualEntry(ctx, finance, future); !errors.Is(err, tax.ErrRecognitionFuture) {
		t.Fatalf("future revenue: err = %v", err)
	}

	if _, err := svc.ReverseEntry(ctx, finance, second.ID, "nhập trùng"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReverseEntry(ctx, finance, second.ID, "lần hai"); !errors.Is(err, tax.ErrAlreadyReversed) {
		t.Fatalf("reversing twice: err = %v", err)
	}
	if v, _ := svc.Year(ctx, finance, 2026); v.Summary.Level != tax.LevelWarning || v.Summary.Revenue.Amount() != 700_000_000 {
		t.Fatalf("after the reversal = %+v", v.Summary)
	}

	year := taxapp.Period{From: pgtest.Date(t, "2026-01-01"), To: pgtest.Date(t, "2026-12-31")}
	rows, err := svc.Report(ctx, finance, year, taxpg.GroupMonth)
	if err != nil || len(rows) != 2 || rows[0].Period != pgtest.Date(t, "2026-03-01") || rows[0].Net.Amount() != 700_000_000 ||
		rows[1].Period != pgtest.Date(t, "2026-06-01") || rows[1].Net.Amount() != 0 || rows[1].Entries != 2 {
		t.Fatalf("monthly report = %+v, %v", rows, err)
	}
	if _, err := svc.Report(ctx, finance, year, taxpg.GroupDay); err != nil {
		t.Fatalf("a daily report of one year: %v", err)
	}
	if _, err := svc.Report(ctx, finance, taxapp.Period{From: pgtest.Date(t, "2025-01-01"), To: pgtest.Date(t, "2026-12-31")}, taxpg.GroupDay); err == nil {
		t.Fatal("a daily report spans at most a year")
	}
	if _, err := svc.Report(ctx, staff, year, taxpg.GroupMonth); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff reading revenue: err = %v", err)
	}
	page, total, err := svc.Entries(ctx, finance, taxapp.EntryQuery{Period: year, Source: tax.SourceManual}, 1, 10)
	if err != nil || total != 3 || page[0].RecordedBy == nil {
		t.Fatalf("manual entries = %+v (%d), %v", page, total, err)
	}
	var exported []tax.EntryID
	if err := svc.ExportEntries(ctx, finance, taxapp.EntryQuery{Period: year}, func(r taxpg.EntryRow) error {
		exported = append(exported, r.ID)
		return nil
	}); err != nil || len(exported) != 3 || exported[0] != first.ID {
		t.Fatalf("export = %v, %v", exported, err)
	}

	next := profile
	next.Version, next.EffectiveFrom, next.WarningPercents = "tax-2026-07", pgtest.Date(t, "2026-07-01"), []int{80, 90}
	if _, err := svc.AddProfile(ctx, admin, next); err != nil {
		t.Fatal(err)
	}
	h, err := svc.Profiles(ctx, finance)
	if err != nil || len(h.Profiles) != 2 || h.Profiles[0].EffectiveUntil == nil || h.Profiles[1].EffectiveUntil != nil {
		t.Fatalf("history = %+v, %v", h, err)
	}
	clock.Set(pgtest.Local(t, "2026-07-02 10:00"))
	if v, _ := svc.Year(ctx, finance, 2026); v.Summary.ProfileVersion != "tax-2026-07" || v.Summary.Mark != 0 {
		t.Fatalf("under the new marks = %+v", v.Summary)
	}
}
