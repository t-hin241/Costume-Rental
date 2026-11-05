package rentalsapp

import (
	"context"

	"webee_rentor/internal/delivery"
	"webee_rentor/internal/delivery/deliverypg"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
)

// DeliveryView is a shipment with the booking it serves. Addresses follow the
// contact rule of the booking (decision D43): staff see them in full only
// while they may still need to reach the customer.
type DeliveryView struct {
	Task    *delivery.Task
	Booking Summary
}

// masked hides the street and most of the phone once the contact period is
// over. The task is copied, never changed in place.
func masked(t *delivery.Task, visible bool) *delivery.Task {
	if visible {
		return t
	}
	c := *t
	if c.Direction == delivery.Outbound {
		c.To = c.To.ForStaffAfterContact()
	} else {
		c.From = c.From.ForStaffAfterContact()
	}
	return &c
}

func (s *Service) deliveryViews(ctx context.Context, p identity.Principal, tasks []*delivery.Task) ([]DeliveryView, error) {
	var ids []rentals.BookingID
	seen := map[string]bool{}
	for _, t := range tasks {
		if !seen[t.BookingRef] {
			seen[t.BookingRef] = true
			ids = append(ids, rentals.BookingID(t.BookingRef))
		}
	}
	sums, err := s.summaries(ctx, s.pool, p, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[rentals.BookingID]Summary, len(sums))
	for _, sm := range sums {
		byID[sm.Booking.ID] = sm
	}
	out := make([]DeliveryView, len(tasks))
	for i, t := range tasks {
		sm := byID[rentals.BookingID(t.BookingRef)]
		out[i] = DeliveryView{Task: masked(t, sm.ContactVisible), Booking: sm}
	}
	return out, nil
}

// DeliveryBoard lists shipments for staff (delivery.read_all) by window,
// only those still needing work when open is set.
func (s *Service) DeliveryBoard(ctx context.Context, p identity.Principal, open bool, page, size int) ([]DeliveryView, int, error) {
	if err := p.Require(identity.PermDeliveryReadAll); err != nil {
		return nil, 0, err
	}
	tasks, total, err := deliverypg.ListTasks(ctx, s.pool, deliverypg.Filter{Open: open, Limit: size, Offset: (page - 1) * size})
	if err != nil {
		return nil, 0, err
	}
	views, err := s.deliveryViews(ctx, p, tasks)
	return views, total, err
}

// BookingDeliveries lists the shipments of a booking the principal may read:
// customers their own, staff any.
func (s *Service) BookingDeliveries(ctx context.Context, p identity.Principal, id rentals.BookingID) ([]DeliveryView, error) {
	b, _, err := rentalspg.LoadBooking(ctx, s.pool, id, false)
	if err != nil {
		return nil, err
	}
	if _, err := p.RequireOwnOrAny(identity.PermDeliveryReadOwn, identity.PermDeliveryReadAll, b.CustomerID); err != nil {
		return nil, err
	}
	stored, err := deliverypg.TasksOfBooking(ctx, s.pool, string(id), false)
	if err != nil {
		return nil, err
	}
	tasks := make([]*delivery.Task, len(stored))
	for i, st := range stored {
		tasks[i] = st.Task
	}
	return s.deliveryViews(ctx, p, tasks)
}
