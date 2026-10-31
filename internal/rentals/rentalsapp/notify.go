package rentalsapp

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/rentals"
)

// Notifier tells a customer what happened to their booking. deadline is set
// when the customer has to act before it (a missing item's verification
// window).
type Notifier interface {
	BookingChanged(ctx context.Context, customer identity.UserID, code string, action rentals.Action, deadline time.Time) error
}

// customerActions are the transitions a customer hears about.
var customerActions = []rentals.Action{
	rentals.ActionConfirm, rentals.ActionCancel, rentals.ActionExpire, rentals.ActionReportMissing, rentals.ActionComplete,
}

// tell runs after the transaction committed. A failed notification is logged
// and changes nothing: the booking and its money already stand.
func (s *Service) tell(ctx context.Context, b *rentals.Booking, ts []rentals.Transition) {
	if s.notify == nil {
		return
	}
	for _, t := range ts {
		if !slices.Contains(customerActions, t.Action) {
			continue
		}
		var deadline time.Time
		if t.Action == rentals.ActionReportMissing {
			deadline, _ = b.VerificationDeadline(inventory.AssetID(t.Reason))
		}
		if err := s.notify.BookingChanged(ctx, b.CustomerID, b.Code, t.Action, deadline); err != nil {
			slog.ErrorContext(ctx, "booking notification failed", "booking_id", b.ID, "action", t.Action, "error", err)
		}
	}
}
