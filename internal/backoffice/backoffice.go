// Package backoffice answers the questions that span modules: the daily
// operations board, late-return alerts, the owner's dashboard, the audit log
// and discount campaigns. It reads through the module use cases and queries;
// the decisions stay in those modules.
package backoffice

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/catalog"
	"webee_rentor/internal/catalog/catalogpg"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identitypg"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/inventory/inventoryapp"
	"webee_rentor/internal/inventory/inventorypg"
	"webee_rentor/internal/money"
	"webee_rentor/internal/platform/ids"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/promotions"
	"webee_rentor/internal/promotions/promotionspg"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalsapp"
	"webee_rentor/internal/rentals/rentalspg"
)

const (
	queueSize    = 50
	scheduleSize = 200
	// conflictHorizon is how far ahead an overdue item is checked against the
	// bookings waiting for it. It only sets how early staff hear about one.
	conflictHorizon = 7 * 24 * time.Hour
)

// Service holds the dependencies of the back-office reads.
type Service struct {
	pool      *pgxpool.Pool
	loc       *time.Location
	now       func() time.Time
	newID     func() string
	rentals   *rentalsapp.Service
	inventory *inventoryapp.Service
}

// New builds the service. loc is the business timezone.
func New(pool *pgxpool.Pool, loc *time.Location, r *rentalsapp.Service, i *inventoryapp.Service, opts ...Option) *Service {
	s := &Service{pool: pool, loc: loc, now: time.Now, newID: ids.New, rentals: r, inventory: i}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Option configures the service.
type Option func(*Service)

// WithClock replaces time.Now, for tests.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// Overview is the daily operations board.
type Overview struct {
	Date      businesstime.Date
	Counts    rentalspg.OpsCounts
	Overdue   int
	Conflicts int
	Queue     []rentalsapp.Summary
}

// Overview counts today's work and lists what waits for staff.
func (s *Service) Overview(ctx context.Context, p identity.Principal) (Overview, error) {
	if err := p.Require(identity.PermBookingReadAll); err != nil {
		return Overview{}, err
	}
	now := s.now().UTC()
	today := businesstime.DateOf(now, s.loc)
	start := businesstime.At(today, businesstime.TimeOfDay{}, s.loc)
	counts, err := rentalspg.CountOps(ctx, s.pool, start, start.AddDate(0, 0, 1))
	if err != nil {
		return Overview{}, err
	}
	overdue, err := s.rentals.OverdueBookings(ctx, p)
	if err != nil {
		return Overview{}, err
	}
	conflicts, err := s.inventory.OverdueConflicts(ctx, p, conflictHorizon)
	if err != nil {
		return Overview{}, err
	}
	ids, err := rentalspg.WorkQueue(ctx, s.pool, queueSize)
	if err != nil {
		return Overview{}, err
	}
	for _, b := range overdue {
		if !slices.Contains(ids, b.ID) {
			ids = append(ids, b.ID)
		}
	}
	queue, err := s.rentals.Summaries(ctx, ids)
	if err != nil {
		return Overview{}, err
	}
	return Overview{Date: today, Counts: counts, Overdue: len(overdue), Conflicts: len(conflicts), Queue: queue}, nil
}

// ScheduleEntry is a handover or a return on the calendar.
type ScheduleEntry struct {
	Handover bool
	At       time.Time
	Booking  rentalsapp.Summary
}

// Schedule lists handovers and returns between two business dates, inclusive.
func (s *Service) Schedule(ctx context.Context, p identity.Principal, from, to businesstime.Date) ([]ScheduleEntry, error) {
	if err := p.Require(identity.PermBookingReadAll); err != nil {
		return nil, err
	}
	start := businesstime.At(from, businesstime.TimeOfDay{}, s.loc)
	end := businesstime.At(to, businesstime.TimeOfDay{}, s.loc).AddDate(0, 0, 1)
	rows, err := rentalspg.Schedule(ctx, s.pool, start, end, scheduleSize)
	if err != nil {
		return nil, err
	}
	ids := make([]rentals.BookingID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	summaries, err := s.rentals.Summaries(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]ScheduleEntry, len(rows))
	for i, r := range rows {
		out[i] = ScheduleEntry{Handover: r.Handover, At: r.At, Booking: summaries[i]}
	}
	return out, nil
}

// OverdueAlert is a booking past its return time.
type OverdueAlert struct {
	Booking  rentalsapp.Summary
	Hours    int
	ItemsOut int
}

// ConflictAlert is an overdue item another booking is waiting for.
type ConflictAlert struct {
	Overdue    rentalsapp.Summary
	AssetCode  string
	ItemName   string
	Endangered []rentalsapp.Summary
}

// Alerts is what needs attention.
type Alerts struct {
	Overdue   []OverdueAlert
	Conflicts []ConflictAlert
}

// Alerts lists late returns and the bookings they affect. Inventory staff see
// the items and schedule but not who the customer is: they do not need it.
func (s *Service) Alerts(ctx context.Context, p identity.Principal) (Alerts, error) {
	canBookings := p.Can(identity.PermBookingReadAll)
	if !canBookings && !p.Can(identity.PermInventoryRead) {
		return Alerts{}, p.Require(identity.PermInventoryRead)
	}
	var out Alerts
	now := s.now().UTC()
	if canBookings {
		overdue, err := s.rentals.OverdueBookings(ctx, p)
		if err != nil {
			return Alerts{}, err
		}
		ids := make([]rentals.BookingID, len(overdue))
		for i, b := range overdue {
			ids[i] = b.ID
		}
		rows, err := s.rentals.Summaries(ctx, ids)
		if err != nil {
			return Alerts{}, err
		}
		for i, b := range overdue {
			out.Overdue = append(out.Overdue, OverdueAlert{Booking: rows[i],
				Hours: int(math.Ceil(now.Sub(b.Schedule.DueAt).Hours())), ItemsOut: itemsOut(b)})
		}
	}
	conflicts, err := s.inventory.OverdueConflicts(ctx, p, conflictHorizon)
	if err != nil {
		return Alerts{}, err
	}
	for _, c := range conflicts {
		a, err := s.conflict(ctx, c)
		if err != nil {
			return Alerts{}, err
		}
		if !canBookings {
			a.Overdue.Customer = identity.Contact{}
			for i := range a.Endangered {
				a.Endangered[i].Customer = identity.Contact{}
			}
		}
		out.Conflicts = append(out.Conflicts, a)
	}
	return out, nil
}

func itemsOut(b *rentals.Booking) int {
	n := 0
	for _, i := range b.Items {
		if i.ReturnedAt == nil && i.LostConfirmedAt == nil {
			n++
		}
	}
	return n
}

func (s *Service) conflict(ctx context.Context, c inventoryapp.Conflict) (ConflictAlert, error) {
	ids := []rentals.BookingID{rentals.BookingID(c.Overdue.BookingRef)}
	for _, e := range c.Endangered {
		ids = append(ids, rentals.BookingID(e.BookingRef))
	}
	rows, err := s.rentals.Summaries(ctx, ids)
	if err != nil {
		return ConflictAlert{}, err
	}
	assets, err := inventorypg.AssetsByID(ctx, s.pool, []inventory.AssetID{c.Overdue.AssetID})
	if err != nil {
		return ConflictAlert{}, err
	}
	variants, err := catalogpg.Variants(ctx, s.pool, []catalog.VariantID{c.Overdue.VariantID})
	if err != nil {
		return ConflictAlert{}, err
	}
	return ConflictAlert{Overdue: rows[0], AssetCode: assets[c.Overdue.AssetID].Code, ItemName: variants[c.Overdue.VariantID].ItemName, Endangered: rows[1:]}, nil
}

// AuditPage is one page of the audit log with who acted.
type AuditPage struct {
	Events []audit.Event
	Actors map[string]string
	Total  int
}

// Audit reads the audit log.
func (s *Service) Audit(ctx context.Context, p identity.Principal, f auditpg.Filter) (AuditPage, error) {
	if err := p.Require(identity.PermAuditRead); err != nil {
		return AuditPage{}, err
	}
	events, total, err := auditpg.Page(ctx, s.pool, f)
	if err != nil {
		return AuditPage{}, err
	}
	people, err := identitypg.Contacts(ctx, s.pool, auditpg.ActorIDs(events))
	if err != nil {
		return AuditPage{}, err
	}
	actors := make(map[string]string, len(people))
	for id, c := range people {
		actors[string(id)] = c.Label()
	}
	return AuditPage{Events: events, Actors: actors, Total: total}, nil
}

// CampaignView is a campaign with its active redemptions.
type CampaignView struct {
	promotions.Campaign
	Used int
}

// Campaigns lists discount campaigns.
func (s *Service) Campaigns(ctx context.Context, p identity.Principal) ([]CampaignView, error) {
	if err := p.Require(identity.PermPromotionManage); err != nil {
		return nil, err
	}
	all, err := promotionspg.All(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	usage, err := promotionspg.Usage(ctx, s.pool, "")
	if err != nil {
		return nil, err
	}
	out := make([]CampaignView, len(all))
	for i, c := range all {
		out[i] = CampaignView{Campaign: c, Used: usage[c.ID].Total}
	}
	return out, nil
}

// SaveCampaign creates a campaign (id empty) or stores a new version of it.
// Bookings keep the snapshot of what they were given.
func (s *Service) SaveCampaign(ctx context.Context, p identity.Principal, id promotions.CampaignID, in promotions.Campaign) (CampaignView, error) {
	if err := p.Require(identity.PermPromotionManage); err != nil {
		return CampaignView{}, err
	}
	var out CampaignView
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		now := s.now().UTC()
		in.Version, in.Funding, in.Timezone = 1, promotions.FundingMerchant, s.loc.String()
		in.Code = promotions.NormalizeCode(in.Code)
		in.ID = id
		if id == "" {
			in.ID = promotions.CampaignID(s.newID())
		} else {
			current, err := promotionspg.Load(ctx, tx, id)
			if err != nil {
				return err
			}
			in.Version = current.Version + 1
			in.MaxDiscount, in.MinimumEligibleAmount = current.MaxDiscount, current.MinimumEligibleAmount
			in.EligibleItemIDs, in.EligibleBundleIDs = current.EligibleItemIDs, current.EligibleBundleIDs
			in.EligibleSegment, in.UsageLimitPerCustomer = current.EligibleSegment, current.UsageLimitPerCustomer
		}
		if in.MinimumEligibleAmount == (money.Money{}) {
			in.MinimumEligibleAmount = money.Zero(money.VND)
		}
		in.PolicyVersion = fmt.Sprintf("campaign-v%d", in.Version)
		if err := promotionspg.Save(ctx, tx, in, now); err != nil {
			return err
		}
		out = CampaignView{Campaign: in}
		return auditpg.Append(ctx, tx, audit.Event{
			ID: s.newID(), OccurredAt: now, Actor: p.Actor(), Action: "promotion.saved", ResourceType: "campaign",
			ResourceID: string(in.ID), Result: audit.ResultSuccess,
		})
	})
	if err != nil {
		return CampaignView{}, fmt.Errorf("save campaign: %w", err)
	}
	return out, nil
}

// KPI is one headline number. Change compares with the period before; nil
// when there is nothing to compare with.
type KPI struct {
	Key    string
	Value  int64
	Change *int64
}

// DayRevenue is the revenue of one business day.
type DayRevenue struct {
	Date    businesstime.Date
	Revenue money.Money
}

// StatusCount is how many records are in a status.
type StatusCount struct {
	Status string
	Count  int
}

// TopOffering is an offering by rentals.
type TopOffering struct {
	Name     string
	Bookings int
	Revenue  money.Money
}

// Dashboard is the owner's overview of a period.
type Dashboard struct {
	Days      int
	KPIs      []KPI
	Revenue   []DayRevenue
	Bookings  []StatusCount
	Inventory []StatusCount
	Top       []TopOffering
}

func change(now, before int64) *int64 {
	if before == 0 {
		return nil
	}
	v := (now - before) * 100 / before
	return &v
}

// Dashboard summarises the last days. Revenue counts what is earned for good:
// rent and fees of bookings that finished, on the day they finished (decision
// D15), never the deposit or the reservation.
func (s *Service) Dashboard(ctx context.Context, p identity.Principal, days int) (Dashboard, error) {
	if !p.Can(identity.PermRevenueRead) && !p.Can(identity.PermPolicyManage) {
		return Dashboard{}, p.Require(identity.PermRevenueRead)
	}
	if days < 1 || days > 366 {
		return Dashboard{}, domainerr.Invalid("range_days", "must be between 1 and 366")
	}
	today := businesstime.DateOf(s.now().UTC(), s.loc)
	end := businesstime.At(today, businesstime.TimeOfDay{}, s.loc).AddDate(0, 0, 1)
	start := end.AddDate(0, 0, -days)
	before := start.AddDate(0, 0, -days)

	revenueNow, series, err := s.revenue(ctx, start, end)
	if err != nil {
		return Dashboard{}, err
	}
	revenueBefore, _, err := s.revenue(ctx, before, start)
	if err != nil {
		return Dashboard{}, err
	}
	var bookings, bookingsBefore, customers, customersBefore, active int64
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE created_at >= $2),
			count(*) FILTER (WHERE created_at < $2),
			count(DISTINCT customer_id) FILTER (WHERE created_at >= $2),
			count(DISTINCT customer_id) FILTER (WHERE created_at < $2),
			(SELECT count(*) FROM bookings WHERE status = 'RENTED')
		FROM bookings WHERE created_at >= $1 AND created_at < $3`, before, start, end).
		Scan(&bookings, &bookingsBefore, &customers, &customersBefore, &active); err != nil {
		return Dashboard{}, fmt.Errorf("dashboard counts: %w", err)
	}
	overdue, err := s.rentals.CountOverdue(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	inv, err := s.counts(ctx, `SELECT status, count(*) FROM assets GROUP BY status ORDER BY 2 DESC, 1`)
	if err != nil {
		return Dashboard{}, err
	}
	byStatus, err := s.counts(ctx, `SELECT status, count(*) FROM bookings WHERE created_at >= $1 AND created_at < $2 GROUP BY status ORDER BY 2 DESC, 1`, start, end)
	if err != nil {
		return Dashboard{}, err
	}
	top, err := s.top(ctx, start, end)
	if err != nil {
		return Dashboard{}, err
	}
	var rented, inService int64
	for _, c := range inv {
		if c.Status != string(inventory.AssetRetired) {
			inService += int64(c.Count)
		}
		if c.Status == string(inventory.AssetRented) {
			rented = int64(c.Count)
		}
	}
	var utilization int64
	if inService > 0 {
		utilization = rented * 100 / inService
	}
	return Dashboard{
		Days: days, Revenue: series, Bookings: byStatus, Inventory: inv, Top: top,
		KPIs: []KPI{
			{Key: "revenue", Value: revenueNow, Change: change(revenueNow, revenueBefore)},
			{Key: "bookings", Value: bookings, Change: change(bookings, bookingsBefore)},
			{Key: "customers", Value: customers, Change: change(customers, customersBefore)},
			{Key: "active_rentals", Value: active},
			{Key: "utilization", Value: utilization},
			{Key: "overdue", Value: int64(overdue)},
		},
	}, nil
}

// revenue sums earned charges per business day and returns every day of
// [start, end), including days with none.
func (s *Service) revenue(ctx context.Context, start, end time.Time) (int64, []DayRevenue, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT to_char(coalesce(b.completed_at, b.cancelled_at, b.expired_at) AT TIME ZONE $3, 'YYYY-MM-DD'), sum(c.amount)
		FROM ledger_charges c JOIN bookings b ON b.id = c.booking_id
		WHERE b.status IN ('COMPLETED', 'CANCELLED', 'EXPIRED')
			AND c.kind NOT IN ('SECURITY_DEPOSIT', 'RESERVATION_DEPOSIT') AND c.reversal_reason IS NULL
			AND coalesce(b.completed_at, b.cancelled_at, b.expired_at) >= $1
			AND coalesce(b.completed_at, b.cancelled_at, b.expired_at) < $2
		GROUP BY 1`, start, end, s.loc.String())
	if err != nil {
		return 0, nil, fmt.Errorf("dashboard revenue: %w", err)
	}
	defer rows.Close()
	byDay := map[string]int64{}
	var total int64
	for rows.Next() {
		var day string
		var amount int64
		if err := rows.Scan(&day, &amount); err != nil {
			return 0, nil, fmt.Errorf("dashboard revenue: %w", err)
		}
		byDay[day] = amount
		total += amount
	}
	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("dashboard revenue: %w", err)
	}
	var series []DayRevenue
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		date := businesstime.DateOf(d, s.loc)
		m, err := money.New(byDay[date.String()], money.VND)
		if err != nil {
			return 0, nil, err
		}
		series = append(series, DayRevenue{Date: date, Revenue: m})
	}
	return total, series, nil
}

func (s *Service) counts(ctx context.Context, query string, args ...any) ([]StatusCount, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("dashboard status counts: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (StatusCount, error) {
		var c StatusCount
		err := r.Scan(&c.Status, &c.Count)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("dashboard status counts: %w", err)
	}
	return out, nil
}

func (s *Service) top(ctx context.Context, start, end time.Time) ([]TopOffering, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT l.offering_name, count(*), sum(l.rent_amount)
		FROM booking_lines l JOIN bookings b ON b.id = l.booking_id
		WHERE b.created_at >= $1 AND b.created_at < $2 AND b.status NOT IN ('CANCELLED', 'EXPIRED')
		GROUP BY l.offering_id, l.offering_name ORDER BY 2 DESC, 3 DESC, 1 LIMIT 5`, start, end)
	if err != nil {
		return nil, fmt.Errorf("dashboard top offerings: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (TopOffering, error) {
		var t TopOffering
		var amount int64
		if err := r.Scan(&t.Name, &t.Bookings, &amount); err != nil {
			return t, err
		}
		var err error
		t.Revenue, err = money.New(amount, money.VND)
		return t, err
	})
	if err != nil {
		return nil, fmt.Errorf("dashboard top offerings: %w", err)
	}
	slices.SortStableFunc(out, func(a, b TopOffering) int { return cmp.Compare(b.Bookings, a.Bookings) })
	return out, nil
}
