// Package delivery owns door-to-door fulfillment: the service area and its
// fixed customer fees, address snapshots, and delivery tasks (outbound to the
// customer, return pickup back to the store) with their carrier events
// (docs/note/delivery-and-carrier-integration.md).
//
// Delivery is its own workflow. A carrier status never decides the booking,
// payment or asset state by itself: "delivered" is evidence of delivery, and
// "received at store" only allows inspection to start.
package delivery

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/money"
)

var (
	ErrOutOfServiceArea = domainerr.New(domainerr.KindInvalid, "delivery.out_of_service_area", "the address is outside the delivery area")
	ErrInvalidAddress   = domainerr.New(domainerr.KindInvalid, "delivery.invalid_address", "the address is incomplete or invalid")
)

// Address is a delivery address snapshot. It is personal data: expose it only
// to roles that need it and log Masked() instead.
type Address struct {
	RecipientName string
	Phone         string
	Line          string // street and house number
	WardCode      string
	// DistrictCode is optional: since 2025-07 the carrier addresses by
	// province and ward only.
	DistrictCode string
	ProvinceCode string
}

const maxAddressField = 200

// Validate checks the address is complete.
func (a Address) Validate() error {
	for _, f := range []string{a.RecipientName, a.Phone, a.Line, a.WardCode, a.ProvinceCode} {
		if strings.TrimSpace(f) == "" || utf8.RuneCountInString(f) > maxAddressField {
			return ErrInvalidAddress
		}
	}
	if utf8.RuneCountInString(a.DistrictCode) > maxAddressField {
		return ErrInvalidAddress
	}
	digits := 0
	for _, r := range a.Phone {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '+' || r == ' ' || r == '-' || r == '.':
		default:
			return ErrInvalidAddress
		}
	}
	if digits < 9 || digits > 15 {
		return ErrInvalidAddress
	}
	return nil
}

// Masked returns a log-safe form: only the last 3 phone digits and the area
// codes.
func (a Address) Masked() string {
	tail := a.Phone
	if len(tail) > 3 {
		tail = tail[len(tail)-3:]
	}
	return fmt.Sprintf("***%s / %s-%s-%s", tail, a.WardCode, a.DistrictCode, a.ProvinceCode)
}

// ForStaffAfterContact is what staff see once they no longer need to reach
// the customer (decision D40, applied to addresses by D43): the recipient
// name and the ward stay, the street line goes and the phone keeps its last 3
// digits.
func (a Address) ForStaffAfterContact() Address {
	out := Address{RecipientName: a.RecipientName, WardCode: a.WardCode, DistrictCode: a.DistrictCode, ProvinceCode: a.ProvinceCode}
	var digits []rune
	for _, r := range a.Phone {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
		}
	}
	if len(digits) > 3 {
		digits = digits[len(digits)-3:]
	}
	out.Phone = "***" + string(digits)
	return out
}

// Zone is a part of the service area with its fixed customer fees (owner
// decision: fixed fee table by area, not the carrier quote).
type Zone struct {
	ID           string
	Name         string
	ProvinceCode string
	// DistrictCodes and Wards, when set, restrict the zone; a zone needs at
	// least one of them.
	DistrictCodes []string
	Wards         []Ward
	OutboundFee   money.Money
	ReturnFee     money.Money
	RedeliveryFee money.Money
	Active        bool
}

// Ward is a carrier ward code with the name customers pick from.
type Ward struct {
	Code string
	Name string
}

func (z Zone) hasWard(code string) bool {
	return slices.ContainsFunc(z.Wards, func(w Ward) bool { return w.Code == code })
}

// ServiceArea is the single operating area of the MVP, served by one carrier
// from one store.
type ServiceArea struct {
	ProviderCode string
	Zones        []Zone
	Version      string
	// FeeTolerance is how far the carrier's final cost may differ from the
	// estimate before staff review it; the store absorbs the rest.
	FeeTolerance money.Money
	// ReturnShipmentLead is how long before due_at the return pickup is
	// arranged with the carrier.
	ReturnShipmentLead time.Duration
	// Origin is the store address parcels leave from and come back to.
	Origin Address
	// ParcelWeightGram is the default parcel weight declared to the carrier.
	ParcelWeightGram int
}

// Validate checks the fee table.
func (s ServiceArea) Validate() error {
	if strings.TrimSpace(s.ProviderCode) == "" || strings.TrimSpace(s.Version) == "" {
		return domainerr.Invalid("service_area", "provider and version are required")
	}
	ids := map[string]bool{}
	for _, z := range s.Zones {
		if z.ID == "" || ids[z.ID] || z.ProvinceCode == "" || (len(z.DistrictCodes) == 0 && len(z.Wards) == 0) {
			return domainerr.Invalid("service_area.zones", "zones need a unique id, a province and districts or wards")
		}
		for _, w := range z.Wards {
			if strings.TrimSpace(w.Code) == "" || strings.TrimSpace(w.Name) == "" {
				return domainerr.Invalid("service_area.zones", fmt.Sprintf("zone %s wards need a code and a name", z.ID))
			}
		}
		ids[z.ID] = true
		for _, m := range []money.Money{z.OutboundFee, z.ReturnFee, z.RedeliveryFee} {
			if !m.Currency().Valid() || m.IsNegative() {
				return domainerr.Invalid("service_area.zones", fmt.Sprintf("zone %s fees must be zero or positive", z.ID))
			}
		}
	}
	if s.FeeTolerance.Currency().Valid() && s.FeeTolerance.IsNegative() || s.ReturnShipmentLead < 0 {
		return domainerr.Invalid("service_area", "fee tolerance and return shipment lead must not be negative")
	}
	if len(s.Zones) > 0 {
		if err := s.Origin.Validate(); err != nil {
			return fmt.Errorf("service area origin: %w", err)
		}
		if s.ParcelWeightGram < 1 {
			return domainerr.Invalid("service_area", "a default parcel weight is required")
		}
	}
	return nil
}

// ZoneOfWard finds the active zone listing a ward; customers pick a ward and
// the province comes from its zone.
func (s ServiceArea) ZoneOfWard(code string) (Zone, bool) {
	for _, z := range s.Zones {
		if z.Active && z.hasWard(code) {
			return z, true
		}
	}
	return Zone{}, false
}

// Resolve finds the active zone serving an address. An address outside the
// area is refused before booking, with no manual exception (decision D42).
func (s ServiceArea) Resolve(a Address) (Zone, error) {
	if err := a.Validate(); err != nil {
		return Zone{}, err
	}
	for _, z := range s.Zones {
		if !z.Active || z.ProvinceCode != a.ProvinceCode {
			continue
		}
		if len(z.DistrictCodes) > 0 && !slices.Contains(z.DistrictCodes, a.DistrictCode) {
			continue
		}
		if len(z.Wards) > 0 && !z.hasWard(a.WardCode) {
			continue
		}
		return z, nil
	}
	return Zone{}, fmt.Errorf("address %s: %w", a.Masked(), ErrOutOfServiceArea)
}
