package rentals

import "time"

// ContactAfterEnd is how long after a booking ends staff still see the
// customer's whole phone number (decision D40).
const ContactAfterEnd = 30 * 24 * time.Hour

// EndedAt is when the booking reached a terminal status; nil while it is live.
func (b *Booking) EndedAt() *time.Time {
	switch b.Status {
	case StatusCompleted:
		return b.CompletedAt
	case StatusCancelled:
		return b.CancelledAt
	case StatusExpired:
		return b.ExpiredAt
	}
	return nil
}

// StaffSeesFullContact reports whether staff reading the booking get the
// customer's whole phone number (decision D40): while the booking is live,
// for ContactAfterEnd after it ended, and for as long as its money is not
// settled, since a debt or a refund may still need a call.
func (b *Booking) StaffSeesFullContact(moneySettled bool, now time.Time) bool {
	end := b.EndedAt()
	if end == nil || !moneySettled {
		return true
	}
	return now.Before(end.Add(ContactAfterEnd))
}

// ContactSearchSince is the time after which a finished booking must have
// ended for a staff search by phone number to find it (decision D40).
func ContactSearchSince(now time.Time) time.Time {
	return now.Add(-ContactAfterEnd)
}
