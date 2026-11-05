package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/delivery"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/ids"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
)

// deliveryFile mirrors config/delivery.yaml (decision D43). Loading it opens
// courier legs at checkout; without it only the counter is offered.
type deliveryFile struct {
	Version  string `yaml:"version"`
	Provider string `yaml:"provider"`
	Windows  []struct {
		ID    string   `yaml:"id"`
		Days  []string `yaml:"days"`
		Start string   `yaml:"start"`
		End   string   `yaml:"end"`
	} `yaml:"windows"`
	Carrier struct {
		FeeTolerance       int64         `yaml:"fee_tolerance"`
		ReturnShipmentLead time.Duration `yaml:"return_shipment_lead"`
		ParcelWeightGram   int           `yaml:"parcel_weight_gram"`
	} `yaml:"carrier"`
	Origin struct {
		Name         string `yaml:"name"`
		Phone        string `yaml:"phone"`
		Line         string `yaml:"line"`
		WardCode     string `yaml:"ward_code"`
		ProvinceCode string `yaml:"province_code"`
	} `yaml:"origin"`
	Zones []struct {
		ID           string `yaml:"id"`
		Name         string `yaml:"name"`
		ProvinceCode string `yaml:"province_code"`
		Wards        []struct {
			Code string `yaml:"code"`
			Name string `yaml:"name"`
		} `yaml:"wards"`
		OutboundFee   int64 `yaml:"outbound_fee"`
		ReturnFee     int64 `yaml:"return_fee"`
		RedeliveryFee int64 `yaml:"redelivery_fee"`
		Active        bool  `yaml:"active"`
	} `yaml:"zones"`
}

// deliveryConfig is the file turned into what the system stores.
type deliveryConfig struct {
	Area    delivery.ServiceArea
	Windows map[time.Weekday][]rentals.Slot
}

// hasPlaceholder reports a "???" left in a value; comments may mention it.
func hasPlaceholder(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if strings.Contains(line, "???") {
			return true
		}
	}
	return false
}

// parseDelivery reads the delivery file and refuses anything the system
// could not use, including placeholders not filled in yet.
func parseDelivery(data []byte) (deliveryConfig, error) {
	if hasPlaceholder(data) {
		return deliveryConfig{}, errors.New("delivery file: fill in every ??? placeholder before loading")
	}
	var f deliveryFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return deliveryConfig{}, fmt.Errorf("read delivery file: %w", err)
	}
	tolerance, err := vnd(f.Carrier.FeeTolerance, "carrier.fee_tolerance")
	if err != nil {
		return deliveryConfig{}, err
	}
	c := deliveryConfig{
		Area: delivery.ServiceArea{ProviderCode: f.Provider, Version: f.Version, FeeTolerance: tolerance,
			ReturnShipmentLead: f.Carrier.ReturnShipmentLead, ParcelWeightGram: f.Carrier.ParcelWeightGram,
			Origin: delivery.Address{RecipientName: f.Origin.Name, Phone: f.Origin.Phone, Line: f.Origin.Line,
				WardCode: f.Origin.WardCode, ProvinceCode: f.Origin.ProvinceCode}},
		Windows: map[time.Weekday][]rentals.Slot{},
	}
	if len(f.Zones) == 0 || len(f.Windows) == 0 {
		return deliveryConfig{}, errors.New("delivery file: at least one zone and one window are required")
	}
	for _, z := range f.Zones {
		zone := delivery.Zone{ID: z.ID, Name: z.Name, ProvinceCode: z.ProvinceCode, Active: z.Active}
		if len(z.Wards) == 0 {
			return deliveryConfig{}, fmt.Errorf("zone %s: list the wards it serves", z.ID)
		}
		for _, w := range z.Wards {
			zone.Wards = append(zone.Wards, delivery.Ward{Code: w.Code, Name: w.Name})
		}
		if zone.OutboundFee, err = vnd(z.OutboundFee, "zone "+z.ID+" outbound_fee"); err != nil {
			return deliveryConfig{}, err
		}
		if zone.ReturnFee, err = vnd(z.ReturnFee, "zone "+z.ID+" return_fee"); err != nil {
			return deliveryConfig{}, err
		}
		if zone.RedeliveryFee, err = vnd(z.RedeliveryFee, "zone "+z.ID+" redelivery_fee"); err != nil {
			return deliveryConfig{}, err
		}
		c.Area.Zones = append(c.Area.Zones, zone)
	}
	if err := c.Area.Validate(); err != nil {
		return deliveryConfig{}, err
	}
	for _, w := range f.Windows {
		start, err := businesstime.ParseTimeOfDay(w.Start)
		if err != nil {
			return deliveryConfig{}, fmt.Errorf("window %s: %w", w.ID, err)
		}
		end, err := businesstime.ParseTimeOfDay(w.End)
		if err != nil {
			return deliveryConfig{}, fmt.Errorf("window %s: %w", w.ID, err)
		}
		for _, name := range w.Days {
			day, ok := weekdays[name]
			if !ok {
				return deliveryConfig{}, fmt.Errorf("window %s: unknown day %q", w.ID, name)
			}
			c.Windows[day] = append(c.Windows[day], rentals.Slot{ID: w.ID, Start: start, End: end})
		}
	}
	cal, err := rentals.NewStoreCalendar(time.UTC, map[time.Weekday][]rentals.Slot{}, nil)
	if err != nil {
		return deliveryConfig{}, err
	}
	if err := cal.SetDeliveryWindows(c.Windows); err != nil {
		return deliveryConfig{}, err
	}
	return c, nil
}

// saveDelivery stores the area as a new active version and replaces the
// courier windows. Bookings already made keep their fee snapshot.
func saveDelivery(ctx context.Context, tx pgx.Tx, c deliveryConfig, now time.Time) (bool, error) {
	actor := identity.SystemActor("cmd/policy")
	var known bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM policy_documents WHERE kind = $1 AND version = $2 AND active)`,
		postgres.DocDeliveryArea, c.Area.Version).Scan(&known); err != nil {
		return false, fmt.Errorf("check active delivery area: %w", err)
	}
	if err := postgres.SaveDocument(ctx, tx, postgres.DocDeliveryArea, c.Area.Version, c.Area, actor, now, true); err != nil {
		return false, err
	}
	if err := rentalspg.ReplaceSlots(ctx, tx, rentalspg.SlotDelivery, c.Windows); err != nil {
		return false, err
	}
	if known {
		return false, nil
	}
	return true, auditpg.Append(ctx, tx, audit.Event{
		ID: ids.New(), OccurredAt: now, Actor: actor, Action: "delivery_area.published", ResourceType: "delivery_area",
		ResourceID: c.Area.Version, Result: audit.ResultSuccess, Reason: "loaded from the configuration file",
	})
}
