package rentals_test

import (
	"testing"
	"time"

	"webee_rentor/internal/rentals"
)

func TestStaffSeeAPhoneNumberWhileTheyMayNeedToCall(t *testing.T) {
	ended := now
	cases := map[string]struct {
		booking rentals.Booking
		settled bool
		at      time.Time
		want    bool
	}{
		"live, long after it was made": {rentals.Booking{Status: rentals.StatusRented}, true, now.AddDate(1, 0, 0), true},
		"completed, last minute":       {rentals.Booking{Status: rentals.StatusCompleted, CompletedAt: &ended}, true, now.Add(rentals.ContactAfterEnd - time.Minute), true},
		"completed, month is up":       {rentals.Booking{Status: rentals.StatusCompleted, CompletedAt: &ended}, true, now.Add(rentals.ContactAfterEnd), false},
		"cancelled, month is up":       {rentals.Booking{Status: rentals.StatusCancelled, CancelledAt: &ended}, true, now.Add(rentals.ContactAfterEnd), false},
		"expired, month is up":         {rentals.Booking{Status: rentals.StatusExpired, ExpiredAt: &ended}, true, now.Add(rentals.ContactAfterEnd), false},
		"cancelled, money still open":  {rentals.Booking{Status: rentals.StatusCancelled, CancelledAt: &ended}, false, now.AddDate(1, 0, 0), true},
	}
	for name, c := range cases {
		if got := c.booking.StaffSeesFullContact(c.settled, c.at); got != c.want {
			t.Errorf("%s: StaffSeesFullContact = %v, want %v", name, got, c.want)
		}
	}
	if since := rentals.ContactSearchSince(now); !since.Equal(now.Add(-rentals.ContactAfterEnd)) {
		t.Fatalf("ContactSearchSince = %s", since)
	}
}
