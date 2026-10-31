package main

import (
	"strings"
	"testing"
	"time"

	"webee_rentor/internal/rentals"
)

const sample = `
version: 2026-11-01
booking:
  hold_ttl: 15m
  min_lead_time: 48h
  max_advance: 2160h
  max_lines_per_booking: 5
  max_quantity_per_line: 5
  customer_reschedule_cutoff: 72h
confirmation: { mode: AUTO, funds: RESERVATION_DEPOSIT }
handover: { funds: FULL_RENT_AND_SECURITY }
buffers:
  default: { preparation: 24h, turnaround: 24h }
  by_class: {}
reservation: { kind: RENT_PERCENT, amount: 0, rent_percent: 30, treatment: APPLIED_TO_RENT }
cancellation:
  tiers:
    - { min_notice: 72h, fee_percent: 10 }
    - { min_notice: 0s, fee_percent: 50 }
  no_show_after: 2h
  no_show_fee_percent: 70
  store_cancel_fee_percent: 0
late_fee:
  grace: 30m
  grace_requires_no_conflict: true
  tiers:
    - { up_to: 4h, percent: 25 }
  extra_period: 24h
  extra_percent: 100
loss: { verification_window: 48h }
fee_approval: { manager_approval_above: 500000 }
modification: { self_service_cutoff: 24h, self_service_size_changes: 1, hold_ttl: 15m }
delivery: { outbound_lead: 4h, return_transit: 4h, complaint_window: 2h }
allocation: { allow_not_ready_for_future: false, not_ready_min_lead: 0s }
payments: { distinct_approver_above: 500000 }
tax:
  version: ???
store:
  counter_slots:
    - { id: morning, days: [mon, sun], start: "09:00", end: "12:00" }
  recurring_closures:
    - { date: "09-02", reason: Quốc khánh }
  closures:
    - { date: "2027-04-16", reason: Giỗ Tổ }
`

func TestParseTurnsTheFileIntoStoredPolicies(t *testing.T) {
	c, err := parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	p := c.Rental
	if p.Version != "2026-11-01" || p.HoldTTL != 15*time.Minute || p.Confirmation.Mode != rentals.ConfirmAuto {
		t.Fatalf("policy = %+v", p)
	}
	if p.Reservation.RentPercent != 30 || p.FeeApproval.ManagerApprovalAbove.Amount() != 500_000 || len(p.Cancellation.Tiers) != 2 {
		t.Fatalf("policy = %+v", p)
	}
	if c.Allocation.AllowNotReadyForFuture {
		t.Fatal("allocation must keep the file's false")
	}
	if len(c.Slots[time.Monday]) != 1 || len(c.Slots[time.Sunday]) != 1 || len(c.Slots[time.Tuesday]) != 0 {
		t.Fatalf("slots = %+v", c.Slots)
	}
	if len(c.Recurring) != 1 || len(c.Closures) != 1 {
		t.Fatalf("closures = %+v %+v", c.Recurring, c.Closures)
	}
}

func TestParseRefusesWhatCouldNotBeUsed(t *testing.T) {
	cases := map[string]string{
		"misspelled key":   strings.Replace(sample, "hold_ttl: 15m\n  min_lead_time", "hold_tl: 15m\n  min_lead_time", 1),
		"placeholder":      strings.Replace(sample, "version: 2026-11-01", "version: ???", 1),
		"invalid policy":   strings.Replace(sample, "max_advance: 2160h", "max_advance: 1h", 1),
		"unknown weekday":  strings.Replace(sample, "[mon, sun]", "[mon, funday]", 1),
		"bad closure date": strings.Replace(sample, "2027-04-16", "2027-13-16", 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parse([]byte(body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
