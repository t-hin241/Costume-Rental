package tax

import (
	"fmt"
	"strings"
	"time"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/money"
	"webee_rentor/internal/payments"
)

type EntryID string

// RevenueEntry is one append-only line of the revenue ledger. Corrections are
// reversing entries, never edits.
type RevenueEntry struct {
	ID         EntryID
	BookingRef string
	// SourceRef is the ledger charge the amount comes from.
	SourceRef string
	Category  Category
	// List, Discount and Net keep the commercial figures apart from any tax
	// base (policy §5): Net = List - Discount.
	List           money.Money
	Discount       money.Money
	Net            money.Money
	RecognizedAt   time.Time
	ProfileVersion string
	Reverses       EntryID
	Reason         string
}

// NewEntry records revenue. Deposits are rejected: they are held money.
func NewEntry(e RevenueEntry) (RevenueEntry, error) {
	if !e.Category.revenue() {
		return RevenueEntry{}, fmt.Errorf("category %s: %w", e.Category, ErrNotRevenue)
	}
	switch {
	case e.ID == "" || e.BookingRef == "" || e.SourceRef == "":
		return RevenueEntry{}, domainerr.Invalid("entry", "id, booking and source are required")
	case e.RecognizedAt.IsZero() || strings.TrimSpace(e.ProfileVersion) == "":
		return RevenueEntry{}, domainerr.Invalid("entry", "recognition time and profile version are required")
	}
	c := e.List.Currency()
	if !c.Valid() || e.Discount.Currency() != c || e.Net.Currency() != c || e.List.Amount()-e.Discount.Amount() != e.Net.Amount() {
		return RevenueEntry{}, domainerr.Invalid("entry", "net must equal list minus discount in one currency")
	}
	e.RecognizedAt = e.RecognizedAt.UTC()
	return e, nil
}

// Reverse returns the entry that cancels e.
func (e RevenueEntry) Reverse(id EntryID, reason string, at time.Time) (RevenueEntry, error) {
	if strings.TrimSpace(reason) == "" {
		return RevenueEntry{}, domainerr.Invalid("reason", "required")
	}
	neg := func(m money.Money) money.Money { n, _ := money.New(-m.Amount(), m.Currency()); return n }
	return NewEntry(RevenueEntry{
		ID: id, BookingRef: e.BookingRef, SourceRef: e.SourceRef, Category: e.Category,
		List: neg(e.List), Discount: neg(e.Discount), Net: neg(e.Net),
		RecognizedAt: at, ProfileVersion: e.ProfileVersion, Reverses: e.ID, Reason: reason,
	})
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

// RecognitionInput is a settled booking ledger plus the booking rent
// breakdown (list and discount behind the RENT charge).
type RecognitionInput struct {
	Ledger       *payments.Ledger
	RentList     money.Money
	RentDiscount money.Money
	IDPrefix     string
	Profile      Profile
	At           time.Time
}

// RecognizeLedger turns the active charges of a settled booking ledger into
// revenue entries (one per charge). Security and reservation deposits never
// become revenue; what was kept from them already appears as fee or rent
// charges. When recognition happens (completion or cancellation settlement)
// is the use case's call.
func RecognizeLedger(in RecognitionInput) ([]RevenueEntry, error) {
	if in.Ledger == nil || in.IDPrefix == "" {
		return nil, domainerr.Invalid("recognition", "ledger and id prefix are required")
	}
	var out []RevenueEntry
	for _, c := range in.Ledger.Charges {
		cat, ok := CategoryFor(c.Kind)
		if !ok || c.Reversal != nil {
			continue
		}
		list, discount := c.Amount, money.Zero(c.Amount.Currency())
		if c.Kind == payments.ChargeRent {
			if in.RentList.Amount()-in.RentDiscount.Amount() != c.Amount.Amount() {
				return nil, domainerr.Invalid("rent", "list minus discount must equal the rent charge")
			}
			list, discount = in.RentList, in.RentDiscount
		}
		e, err := NewEntry(RevenueEntry{
			ID: EntryID(in.IDPrefix + ":" + string(c.ID)), BookingRef: in.Ledger.BookingRef, SourceRef: string(c.ID),
			Category: cat, List: list, Discount: discount, Net: c.Amount,
			RecognizedAt: in.At, ProfileVersion: in.Profile.Version,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// ThresholdLevel is the internal warning level.
type ThresholdLevel string

const (
	ThresholdBelow    ThresholdLevel = "BELOW"
	ThresholdNear     ThresholdLevel = "NEAR"
	ThresholdExceeded ThresholdLevel = "EXCEEDED"
)

// YearSummary is the calendar-year revenue (business timezone) against the
// threshold. Revenue counts net commercial amounts of all revenue categories
// (owner decision D15; the accountant may refine the recognition rule).
type YearSummary struct {
	Year       int
	Revenue    money.Money
	Threshold  money.Money
	Level      ThresholdLevel
	ByCategory map[Category]money.Money
}

// Summarize computes the year summary. The warning never decides tax
// obligations by itself.
func Summarize(entries []RevenueEntry, p Profile, year int, loc *time.Location) YearSummary {
	c := p.AnnualRevenueThreshold.Currency()
	by := map[Category]int64{}
	var total int64
	for _, e := range inYear(entries, year, loc) {
		by[e.Category] += e.Net.Amount()
		total += e.Net.Amount()
	}
	s := YearSummary{Year: year, Revenue: vnd(total, c), Threshold: p.AnnualRevenueThreshold, ByCategory: map[Category]money.Money{}}
	for k, v := range by {
		s.ByCategory[k] = vnd(v, c)
	}
	th := p.AnnualRevenueThreshold.Amount()
	switch {
	case total > th:
		s.Level = ThresholdExceeded
	case total*100 >= th*int64(p.NearThresholdPercent):
		s.Level = ThresholdNear
	default:
		s.Level = ThresholdBelow
	}
	return s
}

// EstimateStatus explains whether an estimate number exists.
type EstimateStatus string

const (
	EstimateBelowThreshold EstimateStatus = "BELOW_THRESHOLD"
	EstimateReady          EstimateStatus = "ESTIMATED"
	EstimateUnconfirmed    EstimateStatus = "UNCONFIRMED"
)

// Estimate is the internal VAT estimate (policy §2.3); not a tax return.
type Estimate struct {
	Status         EstimateStatus
	Summary        YearSummary
	TaxableRevenue *money.Money
	Rate           *Rate
	VAT            *money.Money
	// Missing lists what the accountant still has to confirm.
	Missing []string
}

// EstimateVAT computes taxable_revenue x rate for a year once the threshold
// is exceeded. The base is the whole year taxable revenue, not only the part
// above the threshold. Nothing is estimated while the rate, a category
// treatment or (when discounts exist) the discount basis is unconfirmed.
func EstimateVAT(entries []RevenueEntry, p Profile, year int, loc *time.Location) Estimate {
	s := Summarize(entries, p, year, loc)
	est := Estimate{Summary: s}
	if s.Level != ThresholdExceeded {
		est.Status = EstimateBelowThreshold
		return est
	}
	var base int64
	missing := map[string]bool{}
	for _, e := range inYear(entries, year, loc) {
		switch p.treatment(e.Category) {
		case NotTaxable:
			continue
		case Unconfirmed:
			missing["treatment of "+string(e.Category)] = true
			continue
		}
		switch {
		case e.Discount.IsZero() || p.DiscountBasis == DiscountBasisNet:
			base += e.Net.Amount()
		case p.DiscountBasis == DiscountBasisGross:
			base += e.List.Amount()
		default:
			missing["discount basis"] = true
		}
	}
	if p.VATRevenueRate == nil {
		missing["VAT revenue rate"] = true
	}
	for m := range missing {
		est.Missing = append(est.Missing, m)
	}
	if len(est.Missing) > 0 {
		est.Status = EstimateUnconfirmed
		return est
	}
	c := p.AnnualRevenueThreshold.Currency()
	b := vnd(base, c)
	// Round half up to the đồng for the internal estimate.
	v := vnd((base*int64(p.VATRevenueRate.BasisPoints)+5_000)/10_000, c)
	rate := *p.VATRevenueRate
	est.Status, est.TaxableRevenue, est.Rate, est.VAT = EstimateReady, &b, &rate, &v
	return est
}

func inYear(entries []RevenueEntry, year int, loc *time.Location) []RevenueEntry {
	var out []RevenueEntry
	for _, e := range entries {
		if businesstime.DateOf(e.RecognizedAt, loc).Year == year {
			out = append(out, e)
		}
	}
	return out
}

func vnd(v int64, c money.Currency) money.Money {
	m, _ := money.New(v, c) // sums of bounded ledger amounts
	return m
}
