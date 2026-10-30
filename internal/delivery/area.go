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
	DistrictCode  string
	ProvinceCode  string
}

const maxAddressField = 200

// Validate checks the address is complete.
func (a Address) Validate() error {
	for _, f := range []string{a.RecipientName, a.Phone, a.Line, a.WardCode, a.DistrictCode, a.ProvinceCode} {
		if strings.TrimSpace(f) == "" || utf8.RuneCountInString(f) > maxAddressField {
			return ErrInvalidAddress
		}
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

// Zone is a part of the service area with its fixed customer fees (owner
// decision: fixed fee table by area, not the carrier quote).
type Zone struct {
	ID            string
	Name          string
	ProvinceCode  string
	DistrictCodes []string
	// WardCodes, when set, restricts the zone to those wards.
	WardCodes     []string
	OutboundFee   money.Money
	ReturnFee     money.Money
	RedeliveryFee money.Money
	Active        bool
}

// ServiceArea is the single operating area of the MVP, served by one carrier
// from one store.
type ServiceArea struct {
	ProviderCode string
	Zones        []Zone
	Version      string
}

// Validate checks the fee table.
func (s ServiceArea) Validate() error {
	if strings.TrimSpace(s.ProviderCode) == "" || strings.TrimSpace(s.Version) == "" {
		return domainerr.Invalid("service_area", "provider and version are required")
	}
	ids := map[string]bool{}
	for _, z := range s.Zones {
		if z.ID == "" || ids[z.ID] || z.ProvinceCode == "" || len(z.DistrictCodes) == 0 {
			return domainerr.Invalid("service_area.zones", "zones need a unique id, a province and districts")
		}
		ids[z.ID] = true
		for _, m := range []money.Money{z.OutboundFee, z.ReturnFee, z.RedeliveryFee} {
			if !m.Currency().Valid() || m.IsNegative() {
				return domainerr.Invalid("service_area.zones", fmt.Sprintf("zone %s fees must be zero or positive", z.ID))
			}
		}
	}
	return nil
}

// Resolve finds the active zone serving an address; outside the area the
// customer is told before booking (or staff approve manually).
func (s ServiceArea) Resolve(a Address) (Zone, error) {
	if err := a.Validate(); err != nil {
		return Zone{}, err
	}
	for _, z := range s.Zones {
		if !z.Active || z.ProvinceCode != a.ProvinceCode || !slices.Contains(z.DistrictCodes, a.DistrictCode) {
			continue
		}
		if len(z.WardCodes) > 0 && !slices.Contains(z.WardCodes, a.WardCode) {
			continue
		}
		return z, nil
	}
	return Zone{}, fmt.Errorf("address %s: %w", a.Masked(), ErrOutOfServiceArea)
}
