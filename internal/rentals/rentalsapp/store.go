package rentalsapp

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identitypg"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
)

// CalendarDay is one business day as offered to customers.
type CalendarDay struct {
	Date   businesstime.Date
	Open   bool
	Reason string
	Slots  []rentals.Slot
}

// Calendar is the bookable days of a period.
type Calendar struct {
	MinDate businesstime.Date
	MaxDate businesstime.Date
	Days    []CalendarDay
}

// maxCalendarDays bounds one calendar read.
const maxCalendarDays = 62

// Calendar lists which days and slots can be booked now in [from, to]. Days
// outside the booking horizon are closed, so clients need no policy of their own.
func (s *Service) Calendar(ctx context.Context, from, to businesstime.Date) (Calendar, error) {
	cfg, err := s.config(ctx, s.pool)
	if err != nil {
		return Calendar{}, err
	}
	reasons, err := rentalspg.ClosureReasons(ctx, s.pool)
	if err != nil {
		return Calendar{}, err
	}
	now := s.now().UTC()
	earliest, latest := now.Add(cfg.policy.MinLeadTime), now.Add(cfg.policy.MaxAdvance)
	out := Calendar{MinDate: businesstime.DateOf(earliest, s.loc), MaxDate: businesstime.DateOf(latest, s.loc)}
	// The first day that can really be booked, so clients open on it rather
	// than on a day that has no slot left.
	for d := businesstime.At(out.MinDate, businesstime.TimeOfDay{}, s.loc); !d.After(latest); d = d.AddDate(0, 0, 1) {
		date := businesstime.DateOf(d, s.loc)
		if len(s.bookableSlots(cfg, date, earliest, latest)) > 0 {
			out.MinDate = date
			break
		}
	}
	start := businesstime.At(from, businesstime.TimeOfDay{}, s.loc)
	end := businesstime.At(to, businesstime.TimeOfDay{}, s.loc)
	if end.Before(start) {
		return Calendar{}, domainerr.Invalid("to", "must not be before from")
	}
	for d, n := start, 0; !d.After(end); d, n = d.AddDate(0, 0, 1), n+1 {
		if n >= maxCalendarDays {
			return Calendar{}, domainerr.Invalid("to", fmt.Sprintf("at most %d days at a time", maxCalendarDays))
		}
		date := businesstime.DateOf(d, s.loc)
		day := CalendarDay{Date: date}
		open, _ := cfg.calendar.Day(date)
		switch {
		case !open && reasons[date.String()] != "":
			day.Reason = reasons[date.String()]
		case !open && reasons[date.MonthDay().String()] != "":
			day.Reason = reasons[date.MonthDay().String()]
		case !open:
			day.Reason = "Cửa hàng nghỉ"
		}
		day.Slots = s.bookableSlots(cfg, date, earliest, latest)
		day.Open = len(day.Slots) > 0
		if open && !day.Open {
			day.Reason = "Ngoài thời gian được đặt"
		}
		out.Days = append(out.Days, day)
	}
	return out, nil
}

// bookableSlots keeps the slots of a day that start inside the booking horizon.
func (s *Service) bookableSlots(cfg config, date businesstime.Date, earliest, latest time.Time) []rentals.Slot {
	_, slots := cfg.calendar.Day(date)
	var out []rentals.Slot
	for _, sl := range slots {
		if at := businesstime.At(date, sl.Start, s.loc); !at.Before(earliest) && !at.After(latest) {
			out = append(out, sl)
		}
	}
	return out
}

// ActivePolicy reads the rules in force for new bookings.
func (s *Service) ActivePolicy(ctx context.Context) (rentals.Policy, error) {
	var policy rentals.Policy
	if _, err := postgres.ActiveDocument(ctx, s.pool, postgres.DocRentalPolicy, &policy); err != nil {
		return rentals.Policy{}, err
	}
	return policy, nil
}

// PolicyVersion is the active policy with who published it.
type PolicyVersion struct {
	Policy      rentals.Policy
	PublishedAt time.Time
	PublishedBy string
}

// PolicyForEdit reads the active policy for someone who may change it.
func (s *Service) PolicyForEdit(ctx context.Context, p identity.Principal) (PolicyVersion, error) {
	if err := p.Require(identity.PermPolicyManage); err != nil {
		return PolicyVersion{}, err
	}
	policy, err := s.ActivePolicy(ctx)
	if err != nil {
		return PolicyVersion{}, err
	}
	out := PolicyVersion{Policy: policy}
	var by identity.Actor
	err = s.pool.QueryRow(ctx, `SELECT created_at, created_by_kind, created_by_id FROM policy_documents WHERE kind = $1 AND active`,
		postgres.DocRentalPolicy).Scan(&out.PublishedAt, &by.Kind, &by.ID)
	if err != nil {
		return PolicyVersion{}, postgres.NotFound(err, "active rental policy")
	}
	out.PublishedAt, out.PublishedBy = out.PublishedAt.UTC(), by.ID
	if by.IsUser() {
		people, err := identitypg.Contacts(ctx, s.pool, []identity.UserID{identity.UserID(by.ID)})
		if err != nil {
			return PolicyVersion{}, err
		}
		out.PublishedBy = people[identity.UserID(by.ID)].Label()
	}
	return out, nil
}

// PublishPolicy stores a new immutable version and makes it active. Bookings
// already made keep the snapshot they were sold with.
func (s *Service) PublishPolicy(ctx context.Context, p identity.Principal, policy rentals.Policy, reason string) error {
	if err := p.Require(identity.PermPolicyManage); err != nil {
		return err
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	if reason == "" {
		return domainerr.Invalid("reason", "required")
	}
	return postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		now := s.now().UTC()
		if err := postgres.SaveDocument(ctx, tx, postgres.DocRentalPolicy, policy.Version, policy, p.Actor(), now, true); err != nil {
			return err
		}
		return auditpg.Append(ctx, tx, audit.Event{
			ID: s.newID(), OccurredAt: now, Actor: p.Actor(), Action: "policy.published", ResourceType: "rental_policy",
			ResourceID: policy.Version, Result: audit.ResultSuccess, Reason: reason,
		})
	})
}
