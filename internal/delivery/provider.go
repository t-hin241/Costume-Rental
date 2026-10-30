package delivery

import (
	"context"
	"time"

	"webee_rentor/internal/money"
)

// ShippingProvider is the port a carrier adapter implements (for example
// internal/integration/shipping/ghn). Adapters own HTTP, timeouts, retries,
// webhook authentication and status mapping; they are called outside the
// business transaction through an outbox worker. Only the capabilities the
// MVP uses are listed.
type ShippingProvider interface {
	Code() string
	CalculateFee(ctx context.Context, req FeeQuoteRequest) (FeeQuote, error)
	// CreateShipment must send ClientReference so a retry after a timeout
	// can be detected; callers look the shipment up before retrying.
	CreateShipment(ctx context.Context, req CreateShipmentRequest) (ShipmentResult, error)
	FindShipment(ctx context.Context, clientReference string) (*ShipmentResult, error)
	CancelShipment(ctx context.Context, shipmentCode string) error
	GetShipment(ctx context.Context, shipmentCode string) (CarrierEvent, error)
	// ParseWebhook authenticates the request as the provider documents and
	// maps it to an internal event; unauthenticated payloads return an error.
	ParseWebhook(ctx context.Context, headers map[string][]string, body []byte) (CarrierEvent, error)
}

// FeeQuoteRequest asks for the carrier cost of a leg (for reconciliation;
// the customer fee comes from the zone table).
type FeeQuoteRequest struct {
	Direction Direction
	From      Address
	To        Address
	Parcel    Parcel
}

// FeeQuote is a carrier cost estimate.
type FeeQuote struct {
	Fee        money.Money
	ExpectedAt *time.Time
	ValidUntil *time.Time
}

// CreateShipmentRequest creates a carrier shipment for a task. For a return
// leg From is the customer and To the store.
type CreateShipmentRequest struct {
	ClientReference string
	Direction       Direction
	From            Address
	To              Address
	Parcel          Parcel
	PickupWindow    *time.Time
}
