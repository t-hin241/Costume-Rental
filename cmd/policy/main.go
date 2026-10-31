// Command policy loads the owner-approved configuration file into the
// database: rental, allocation and payment-approval policies, counter slots
// and store closures. Policies are immutable versions, so running it again
// with the same file changes nothing, and changing a value needs a new
// version. The tax section has its own lifecycle and is not read here.
//
// One-off closures are only ever added: removing a date from the file does
// not reopen the store on that date.
//
//	DATABASE_URL=postgres://... go run ./cmd/policy -file config/policy.yaml
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/money"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/platform/envconfig"
	"webee_rentor/internal/platform/ids"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
)

// file mirrors config/policy.yaml. Unknown keys are errors, so a misspelled
// setting cannot silently fall back to zero.
type file struct {
	Version string `yaml:"version"`
	Booking struct {
		HoldTTL                  time.Duration `yaml:"hold_ttl"`
		MinLeadTime              time.Duration `yaml:"min_lead_time"`
		MaxAdvance               time.Duration `yaml:"max_advance"`
		MaxLinesPerBooking       int           `yaml:"max_lines_per_booking"`
		MaxQuantityPerLine       int           `yaml:"max_quantity_per_line"`
		CustomerRescheduleCutoff time.Duration `yaml:"customer_reschedule_cutoff"`
	} `yaml:"booking"`
	Confirmation struct {
		Mode  rentals.ConfirmMode          `yaml:"mode"`
		Funds rentals.FundsRequirementKind `yaml:"funds"`
	} `yaml:"confirmation"`
	Handover struct {
		Funds rentals.FundsRequirementKind `yaml:"funds"`
	} `yaml:"handover"`
	Buffers struct {
		Default bufferFile            `yaml:"default"`
		ByClass map[string]bufferFile `yaml:"by_class"`
	} `yaml:"buffers"`
	Reservation struct {
		Kind        rentals.ReservationDepositKind `yaml:"kind"`
		Amount      int64                          `yaml:"amount"`
		RentPercent int                            `yaml:"rent_percent"`
		Treatment   rentals.ReservationTreatment   `yaml:"treatment"`
	} `yaml:"reservation"`
	Cancellation struct {
		Tiers []struct {
			MinNotice  time.Duration `yaml:"min_notice"`
			FeePercent int           `yaml:"fee_percent"`
		} `yaml:"tiers"`
		NoShowAfter           time.Duration `yaml:"no_show_after"`
		NoShowFeePercent      int           `yaml:"no_show_fee_percent"`
		StoreCancelFeePercent int           `yaml:"store_cancel_fee_percent"`
	} `yaml:"cancellation"`
	LateFee struct {
		Grace                   time.Duration `yaml:"grace"`
		GraceRequiresNoConflict bool          `yaml:"grace_requires_no_conflict"`
		Tiers                   []struct {
			UpTo    time.Duration `yaml:"up_to"`
			Percent int           `yaml:"percent"`
		} `yaml:"tiers"`
		ExtraPeriod  time.Duration `yaml:"extra_period"`
		ExtraPercent int           `yaml:"extra_percent"`
	} `yaml:"late_fee"`
	Loss struct {
		VerificationWindow time.Duration `yaml:"verification_window"`
	} `yaml:"loss"`
	FeeApproval struct {
		ManagerApprovalAbove int64 `yaml:"manager_approval_above"`
	} `yaml:"fee_approval"`
	Modification struct {
		SelfServiceCutoff      time.Duration `yaml:"self_service_cutoff"`
		SelfServiceSizeChanges int           `yaml:"self_service_size_changes"`
		HoldTTL                time.Duration `yaml:"hold_ttl"`
	} `yaml:"modification"`
	Delivery struct {
		OutboundLead    time.Duration `yaml:"outbound_lead"`
		ReturnTransit   time.Duration `yaml:"return_transit"`
		ComplaintWindow time.Duration `yaml:"complaint_window"`
	} `yaml:"delivery"`
	Allocation struct {
		AllowNotReadyForFuture bool          `yaml:"allow_not_ready_for_future"`
		NotReadyMinLead        time.Duration `yaml:"not_ready_min_lead"`
	} `yaml:"allocation"`
	Payments struct {
		DistinctApproverAbove int64 `yaml:"distinct_approver_above"`
	} `yaml:"payments"`
	Tax   yaml.Node `yaml:"tax"`
	Store struct {
		CounterSlots []struct {
			ID    string   `yaml:"id"`
			Days  []string `yaml:"days"`
			Start string   `yaml:"start"`
			End   string   `yaml:"end"`
		} `yaml:"counter_slots"`
		RecurringClosures []closureFile `yaml:"recurring_closures"`
		Closures          []closureFile `yaml:"closures"`
	} `yaml:"store"`
}

type bufferFile struct {
	Preparation time.Duration `yaml:"preparation"`
	Turnaround  time.Duration `yaml:"turnaround"`
}

func (b bufferFile) buffer() rentals.Buffer {
	return rentals.Buffer{Preparation: b.Preparation, Turnaround: b.Turnaround}
}

type closureFile struct {
	Date   string `yaml:"date"`
	Reason string `yaml:"reason"`
}

// config is the file turned into what the system stores.
type config struct {
	Rental     rentals.Policy
	Allocation inventory.AllocationPolicy
	Approval   payments.ApprovalPolicy
	Slots      map[time.Weekday][]rentals.Slot
	Recurring  []rentalspg.RecurringClosure
	Closures   []closure
}

type closure struct {
	Date   businesstime.Date
	Reason string
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

func vnd(n int64, field string) (money.Money, error) {
	m, err := money.New(n, money.VND)
	if err != nil {
		return money.Money{}, fmt.Errorf("%s: %w", field, err)
	}
	return m, nil
}

// parse reads the file and refuses anything the system could not use.
func parse(data []byte) (config, error) {
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return config{}, fmt.Errorf("read policy file: %w", err)
	}
	if strings.Contains(f.Version, "?") {
		return config{}, errors.New("version: fill in the placeholder before loading")
	}
	reservation, err := vnd(f.Reservation.Amount, "reservation.amount")
	if err != nil {
		return config{}, err
	}
	feeApproval, err := vnd(f.FeeApproval.ManagerApprovalAbove, "fee_approval.manager_approval_above")
	if err != nil {
		return config{}, err
	}
	approver, err := vnd(f.Payments.DistinctApproverAbove, "payments.distinct_approver_above")
	if err != nil {
		return config{}, err
	}
	byClass := make(map[string]rentals.Buffer, len(f.Buffers.ByClass))
	for class, b := range f.Buffers.ByClass {
		byClass[class] = b.buffer()
	}
	c := config{
		Rental: rentals.Policy{
			Version: f.Version, HoldTTL: f.Booking.HoldTTL, MinLeadTime: f.Booking.MinLeadTime, MaxAdvance: f.Booking.MaxAdvance,
			MaxLinesPerBooking: f.Booking.MaxLinesPerBooking, MaxQuantityPerLine: f.Booking.MaxQuantityPerLine,
			Confirmation:             rentals.ConfirmationRule{Mode: f.Confirmation.Mode, Funds: rentals.FundsRequirement{Kind: f.Confirmation.Funds}},
			Handover:                 rentals.FundsRequirement{Kind: f.Handover.Funds},
			CustomerRescheduleCutoff: f.Booking.CustomerRescheduleCutoff,
			Buffers:                  rentals.BufferPolicy{Default: f.Buffers.Default.buffer(), ByClass: byClass},
			Reservation: rentals.ReservationPolicy{
				Kind: f.Reservation.Kind, Amount: reservation, RentPercent: f.Reservation.RentPercent, Treatment: f.Reservation.Treatment,
			},
			Cancellation: rentals.CancellationPolicy{
				NoShowAfter: f.Cancellation.NoShowAfter, NoShowFeePercent: f.Cancellation.NoShowFeePercent, StoreCancelFeePercent: f.Cancellation.StoreCancelFeePercent,
			},
			LateFee: rentals.LateFeePolicy{
				Grace: f.LateFee.Grace, GraceRequiresNoConflict: f.LateFee.GraceRequiresNoConflict, ExtraPeriod: f.LateFee.ExtraPeriod, ExtraPercent: f.LateFee.ExtraPercent,
			},
			Loss:        rentals.LossPolicy{VerificationWindow: f.Loss.VerificationWindow},
			FeeApproval: rentals.FeeApprovalPolicy{ManagerApprovalAbove: feeApproval},
			Modification: rentals.ModificationPolicy{
				SelfServiceCutoff: f.Modification.SelfServiceCutoff, SelfServiceSizeChanges: f.Modification.SelfServiceSizeChanges, HoldTTL: f.Modification.HoldTTL,
			},
			Delivery: rentals.DeliveryPolicy{
				OutboundLead: f.Delivery.OutboundLead, ReturnTransit: f.Delivery.ReturnTransit, ComplaintWindow: f.Delivery.ComplaintWindow,
			},
		},
		Allocation: inventory.AllocationPolicy{AllowNotReadyForFuture: f.Allocation.AllowNotReadyForFuture, NotReadyMinLead: f.Allocation.NotReadyMinLead},
		Approval:   payments.ApprovalPolicy{DistinctApproverAbove: approver},
		Slots:      map[time.Weekday][]rentals.Slot{},
	}
	for _, t := range f.Cancellation.Tiers {
		c.Rental.Cancellation.Tiers = append(c.Rental.Cancellation.Tiers, rentals.FeeTier{MinNotice: t.MinNotice, FeePercent: t.FeePercent})
	}
	for _, t := range f.LateFee.Tiers {
		c.Rental.LateFee.Tiers = append(c.Rental.LateFee.Tiers, rentals.LateFeeTier{UpTo: t.UpTo, Percent: t.Percent})
	}
	if err := c.Rental.Validate(); err != nil {
		return config{}, err
	}
	if err := c.Allocation.Validate(); err != nil {
		return config{}, err
	}
	for _, s := range f.Store.CounterSlots {
		start, err := businesstime.ParseTimeOfDay(s.Start)
		if err != nil {
			return config{}, fmt.Errorf("slot %s: %w", s.ID, err)
		}
		end, err := businesstime.ParseTimeOfDay(s.End)
		if err != nil {
			return config{}, fmt.Errorf("slot %s: %w", s.ID, err)
		}
		for _, name := range s.Days {
			day, ok := weekdays[name]
			if !ok {
				return config{}, fmt.Errorf("slot %s: unknown day %q", s.ID, name)
			}
			c.Slots[day] = append(c.Slots[day], rentals.Slot{ID: s.ID, Start: start, End: end})
		}
	}
	if _, err := rentals.NewStoreCalendar(time.UTC, c.Slots, nil); err != nil {
		return config{}, err
	}
	for _, r := range f.Store.RecurringClosures {
		d, err := businesstime.ParseMonthDay(r.Date)
		if err != nil {
			return config{}, err
		}
		c.Recurring = append(c.Recurring, rentalspg.RecurringClosure{Date: d, Reason: r.Reason})
	}
	for _, r := range f.Store.Closures {
		d, err := businesstime.ParseDate(r.Date)
		if err != nil {
			return config{}, err
		}
		c.Closures = append(c.Closures, closure{Date: d, Reason: r.Reason})
	}
	return c, nil
}

func save(ctx context.Context, tx pgx.Tx, c config, now time.Time) (bool, error) {
	actor := identity.SystemActor("cmd/policy")
	var known bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM policy_documents WHERE kind = $1 AND version = $2 AND active)`,
		postgres.DocRentalPolicy, c.Rental.Version).Scan(&known); err != nil {
		return false, fmt.Errorf("check active policy: %w", err)
	}
	version := c.Rental.Version
	if err := postgres.SaveDocument(ctx, tx, postgres.DocRentalPolicy, version, c.Rental, actor, now, true); err != nil {
		return false, err
	}
	if err := postgres.SaveDocument(ctx, tx, postgres.DocAllocationPolicy, version, c.Allocation, actor, now, true); err != nil {
		return false, err
	}
	if err := postgres.SaveDocument(ctx, tx, postgres.DocPaymentApproval, version, c.Approval, actor, now, true); err != nil {
		return false, err
	}
	if err := rentalspg.ReplaceSlots(ctx, tx, rentalspg.SlotCounter, c.Slots); err != nil {
		return false, err
	}
	if err := rentalspg.ReplaceRecurringClosures(ctx, tx, c.Recurring); err != nil {
		return false, err
	}
	for _, d := range c.Closures {
		if err := rentalspg.AddClosure(ctx, tx, d.Date, d.Reason); err != nil {
			return false, err
		}
	}
	if known {
		return false, nil
	}
	return true, auditpg.Append(ctx, tx, audit.Event{
		ID: ids.New(), OccurredAt: now, Actor: actor, Action: "policy.published", ResourceType: "rental_policy",
		ResourceID: version, Result: audit.ResultSuccess, Reason: "loaded from the configuration file",
	})
}

func run(path string) error {
	env := envconfig.New()
	dsn := env.Required("DATABASE_URL")
	if err := env.Err(); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	c, err := parse(data)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := postgres.Open(ctx, postgres.Config{DSN: dsn, ConnectTimeout: 5 * time.Second, StatementTimeout: 10 * time.Second})
	if err != nil {
		return err
	}
	defer pool.Close()
	var published bool
	err = postgres.InTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		published, err = save(ctx, tx, c, time.Now().UTC())
		return err
	})
	if err != nil {
		if errors.Is(err, postgres.ErrDuplicate) {
			return fmt.Errorf("%w: change version in the file when a value changes", err)
		}
		return err
	}
	slog.Info("policy loaded", "version", c.Rental.Version, "published", published)
	return nil
}

func main() {
	path := flag.String("file", "config/policy.yaml", "policy configuration file")
	flag.Parse()
	if err := run(*path); err != nil {
		slog.Error("policy load failed", "error", err)
		os.Exit(1)
	}
}
