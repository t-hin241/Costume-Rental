package tax

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/payments"
)

type EntryID string

// Source says where an entry comes from.
type Source string

const (
	// SourceBooking entries follow a booking ledger charge.
	SourceBooking Source = "BOOKING"
	// SourceManual entries are revenue earned outside the website, entered by
	// finance (tax-policy §3.2): the threshold counts all of the business.
	SourceManual Source = "MANUAL"
)

const (
	maxChannel  = 80
	maxDocument = 80
	maxNote     = 500
)

var (
	ErrEntryInvalid      = domainerr.New(domainerr.KindInvalid, "tax.entry_invalid", "revenue entry is invalid")
	ErrNotManual         = domainerr.New(domainerr.KindInvalidState, "tax.entry_not_manual", "booking revenue follows its ledger; correct the ledger instead")
	ErrAlreadyReversed   = domainerr.New(domainerr.KindConflict, "tax.entry_reversed", "the entry is already reversed")
	ErrReversalReversed  = domainerr.New(domainerr.KindInvalidState, "tax.entry_is_reversal", "a reversal cannot be reversed")
	ErrRecognitionFuture = domainerr.New(domainerr.KindInvalid, "tax.entry_future", "revenue cannot be recorded for a future date")
)

// RevenueEntry is one append-only line of the revenue ledger. Corrections are
// reversing entries, never edits.
type RevenueEntry struct {
	ID     EntryID
	Source Source
	// BookingRef and SourceRef (the ledger charge) are set on booking entries.
	BookingRef string
	SourceRef  string
	Category   Category
	// List, Discount and Net keep the commercial figures apart: Net = List -
	// Discount. Net is the revenue.
	List           money.Money
	Discount       money.Money
	Net            money.Money
	RecognizedAt   time.Time
	ProfileVersion string
	Reverses       EntryID
	Reason         string
	// Manual entries say where the revenue was earned, what it was and the
	// document behind it, and who entered it.
	Channel     string
	Note        string
	DocumentRef string
	RecordedBy  *identity.Actor
}

// NewEntry checks an entry. Deposits are rejected: they are held money.
func NewEntry(e RevenueEntry) (RevenueEntry, error) {
	if !e.Category.revenue() {
		return RevenueEntry{}, fmt.Errorf("category %s: %w", e.Category, ErrNotRevenue)
	}
	bad := func(why string) error { return fmt.Errorf("revenue entry %s: %s: %w", e.ID, why, ErrEntryInvalid) }
	switch e.Source {
	case SourceBooking:
		if e.BookingRef == "" || e.SourceRef == "" || e.Channel != "" || e.RecordedBy != nil {
			return RevenueEntry{}, bad("a booking entry has its booking and charge and nothing else")
		}
	case SourceManual:
		if e.BookingRef != "" || e.SourceRef != "" || strings.TrimSpace(e.Channel) == "" || e.RecordedBy == nil || !e.RecordedBy.IsUser() {
			return RevenueEntry{}, bad("a manual entry has a channel and the user who entered it, and no booking")
		}
	default:
		return RevenueEntry{}, bad("unknown source")
	}
	switch {
	case e.ID == "":
		return RevenueEntry{}, bad("id is required")
	case e.RecognizedAt.IsZero() || strings.TrimSpace(e.ProfileVersion) == "":
		return RevenueEntry{}, bad("recognition time and profile version are required")
	case utf8.RuneCountInString(e.Channel) > maxChannel || utf8.RuneCountInString(e.DocumentRef) > maxDocument ||
		utf8.RuneCountInString(e.Note) > maxNote || utf8.RuneCountInString(e.Reason) > maxNote:
		return RevenueEntry{}, bad("text is too long")
	case (e.Reverses != "") == (strings.TrimSpace(e.Reason) == ""):
		return RevenueEntry{}, bad("a reversal, and only a reversal, has a reason")
	}
	c := e.List.Currency()
	if c != money.VND || e.Discount.Currency() != c || e.Net.Currency() != c || e.List.Amount()-e.Discount.Amount() != e.Net.Amount() {
		return RevenueEntry{}, bad("net must equal list minus discount, in VND")
	}
	// An entry adds revenue; its reversal takes exactly that away.
	sign := int64(1)
	if e.Reverses != "" {
		sign = -1
	}
	if sign*e.List.Amount() < 0 || sign*e.Discount.Amount() < 0 || sign*e.Net.Amount() < 0 {
		return RevenueEntry{}, bad("amounts have the wrong sign")
	}
	e.RecognizedAt = e.RecognizedAt.UTC()
	return e, nil
}

// Reverse returns the entry that cancels e, recognised at the given time.
func (e RevenueEntry) Reverse(id EntryID, reason string, by *identity.Actor, at time.Time) (RevenueEntry, error) {
	if e.Reverses != "" {
		return RevenueEntry{}, ErrReversalReversed
	}
	if strings.TrimSpace(reason) == "" {
		return RevenueEntry{}, domainerr.Invalid("reason", "required")
	}
	neg := func(m money.Money) money.Money { n, _ := money.New(-m.Amount(), m.Currency()); return n }
	r := e
	r.ID, r.List, r.Discount, r.Net = id, neg(e.List), neg(e.Discount), neg(e.Net)
	r.RecognizedAt, r.Reverses, r.Reason = at, e.ID, strings.TrimSpace(reason)
	if e.Source == SourceManual {
		r.RecordedBy = by
	}
	return NewEntry(r)
}

// ManualInput is revenue earned outside the website.
type ManualInput struct {
	ID       EntryID
	Category Category
	Amount   money.Money
	// Date is the business day the revenue was earned.
	Date        businesstime.Date
	Channel     string
	Note        string
	DocumentRef string
}

// NewManualEntry records revenue from another channel (revenue.record). It is
// recognised at the start of its business day, under the profile effective
// then; a future day is refused.
func NewManualEntry(p identity.Principal, in ManualInput, h History, loc *time.Location, now time.Time) (RevenueEntry, error) {
	if err := p.Require(identity.PermRevenueRecord); err != nil {
		return RevenueEntry{}, err
	}
	if !in.Date.Valid() {
		return RevenueEntry{}, domainerr.Invalid("date", "required")
	}
	at := businesstime.At(in.Date, businesstime.TimeOfDay{}, loc)
	if at.After(now) {
		return RevenueEntry{}, ErrRecognitionFuture
	}
	if !in.Amount.IsPositive() {
		return RevenueEntry{}, domainerr.Invalid("amount", "must be positive")
	}
	prof, err := h.At(at)
	if err != nil {
		return RevenueEntry{}, err
	}
	actor := p.Actor()
	return NewEntry(RevenueEntry{
		ID: in.ID, Source: SourceManual, Category: in.Category,
		List: in.Amount, Discount: money.Zero(in.Amount.Currency()), Net: in.Amount,
		RecognizedAt: at, ProfileVersion: prof.Version,
		Channel: strings.TrimSpace(in.Channel), Note: strings.TrimSpace(in.Note), DocumentRef: strings.TrimSpace(in.DocumentRef),
		RecordedBy: &actor,
	})
}

// ReverseManual cancels a manual entry (revenue.record). Booking entries are
// corrected through their ledger, which the revenue ledger then follows.
func ReverseManual(p identity.Principal, e RevenueEntry, alreadyReversed bool, id EntryID, reason string, now time.Time) (RevenueEntry, error) {
	if err := p.Require(identity.PermRevenueRecord); err != nil {
		return RevenueEntry{}, err
	}
	switch {
	case e.Source != SourceManual:
		return RevenueEntry{}, ErrNotManual
	case e.Reverses != "":
		return RevenueEntry{}, ErrReversalReversed
	case alreadyReversed:
		return RevenueEntry{}, ErrAlreadyReversed
	}
	actor := p.Actor()
	return e.Reverse(id, reason, &actor, now)
}

// CategoryFor maps a ledger charge kind to its revenue category; deposits
// return false.
func CategoryFor(k payments.ChargeKind) (Category, bool) {
	switch k {
	case payments.ChargeRent:
		return CatRentalRevenue, true
	case payments.ChargeDeliveryFee, payments.ChargeRedeliveryFee:
		return CatDeliveryService, true
	case payments.ChargeCleaningFee:
		return CatCleaningService, true
	case payments.ChargeLateFee:
		return CatLateReturnCharge, true
	case payments.ChargeDamageFee:
		return CatDamageCharge, true
	case payments.ChargeLossFee:
		return CatLostItemCompensation, true
	case payments.ChargeCancellationFee:
		return CatCancellationCharge, true
	case payments.ChargeOtherFee:
		return CatOtherCharge, true
	}
	return "", false
}

// EntryOf and ReversalOf are the fixed ids of the entries of a charge, which
// make recognition safe to repeat.
func EntryOf(c payments.ChargeID) EntryID    { return EntryID("rev:" + string(c)) }
func ReversalOf(c payments.ChargeID) EntryID { return EntryID("rev:" + string(c) + ":reversal") }

// BookingRevenue is an ended booking with its ledger and the entries it
// already has.
type BookingRevenue struct {
	Ledger *payments.Ledger
	// EndedAt is when the booking was completed, cancelled or expired.
	EndedAt time.Time
	// RentList is the booking rent at list prices. What a rent charge falls
	// short of it (discounts, service credits) is recognised as discount.
	RentList money.Money
	Existing []RevenueEntry
}

// SyncBooking returns the entries that bring the revenue of an ended booking
// in line with its ledger (decision D41). Each active charge other than a
// deposit is revenue once the booking has ended: recognised when it ended, or
// when the charge was added if later. A charge reversed after that is
// reversed on the day it was. What was reversed before the end was never
// revenue. Times come from the records, never from when this runs, so the
// result is the same whether it runs in the booking transaction or later.
// An entry whose time no profile covers waits until one does.
func SyncBooking(in BookingRevenue, h History) ([]RevenueEntry, error) {
	if in.Ledger == nil || in.EndedAt.IsZero() {
		return nil, domainerr.Invalid("recognition", "an ended booking ledger is required")
	}
	have := map[EntryID]RevenueEntry{}
	for _, e := range in.Existing {
		have[e.ID] = e
	}
	later := func(a, b time.Time) time.Time {
		if b.After(a) {
			return b
		}
		return a
	}
	var out []RevenueEntry
	for _, c := range in.Ledger.Charges {
		cat, ok := CategoryFor(c.Kind)
		if !ok || !c.Amount.IsPositive() || (c.Reversal != nil && !c.Reversal.At.After(in.EndedAt)) {
			continue
		}
		e, ok := have[EntryOf(c.ID)]
		if !ok {
			at := later(in.EndedAt, c.CreatedAt)
			prof, err := h.At(at)
			if err != nil {
				continue
			}
			list := c.Amount
			if c.Kind == payments.ChargeRent && in.RentList.Currency() == c.Amount.Currency() && in.RentList.Amount() > c.Amount.Amount() {
				list = in.RentList
			}
			discount, _ := list.Sub(c.Amount)
			e, err = NewEntry(RevenueEntry{
				ID: EntryOf(c.ID), Source: SourceBooking, BookingRef: in.Ledger.BookingRef, SourceRef: string(c.ID),
				Category: cat, List: list, Discount: discount, Net: c.Amount, RecognizedAt: at, ProfileVersion: prof.Version,
			})
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		}
		if _, done := have[ReversalOf(c.ID)]; c.Reversal != nil && !done {
			r, err := e.Reverse(ReversalOf(c.ID), c.Reversal.Reason, nil, later(in.EndedAt, c.Reversal.At))
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// Level is the internal threshold warning.
type Level string

const (
	LevelBelow    Level = "BELOW"
	LevelWarning  Level = "WARNING"
	LevelExceeded Level = "EXCEEDED"
)

// YearSummary is the calendar-year revenue (business timezone) against the
// threshold of the profile given.
type YearSummary struct {
	Year           int
	ProfileVersion string
	List           money.Money
	Discount       money.Money
	Revenue        money.Money
	Threshold      money.Money
	// Percent is the revenue as a share of the threshold, rounded down.
	Percent int
	Level   Level
	// Mark is the highest warning mark reached; zero below the first.
	Mark       int
	Marks      []int
	ByCategory map[Category]money.Money
}

// Summarize computes the year summary of the entries. The warning never
// changes how anything is computed (tax-policy §12).
func Summarize(entries []RevenueEntry, p Profile, year int, loc *time.Location) YearSummary {
	by := map[Category]int64{}
	var list, discount, total int64
	for _, e := range entries {
		if businesstime.DateOf(e.RecognizedAt, loc).Year != year {
			continue
		}
		by[e.Category] += e.Net.Amount()
		list, discount, total = list+e.List.Amount(), discount+e.Discount.Amount(), total+e.Net.Amount()
	}
	th := p.AnnualRevenueThreshold.Amount()
	s := YearSummary{
		Year: year, ProfileVersion: p.Version, List: vnd(list), Discount: vnd(discount), Revenue: vnd(total),
		Threshold: p.AnnualRevenueThreshold, Level: LevelBelow, Marks: slices.Clone(p.WarningPercents),
		ByCategory: map[Category]money.Money{},
	}
	for k, v := range by {
		s.ByCategory[k] = vnd(v)
	}
	if th > 0 && total > 0 {
		s.Percent = int(min(total/th*100+total%th*100/th, 1_000_000))
	}
	for _, m := range p.WarningPercents {
		if total > 0 && total*100 >= th*int64(m) {
			s.Level, s.Mark = LevelWarning, m
		}
	}
	if total > th {
		s.Level = LevelExceeded
	}
	return s
}

func vnd(v int64) money.Money {
	m, _ := money.New(v, money.VND) // sums of bounded ledger amounts
	return m
}
