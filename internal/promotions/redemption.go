package promotions

import (
	"fmt"
	"strings"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
)

type RedemptionID string

// RedemptionStatus tracks whether a use counts against limits.
type RedemptionStatus string

const (
	RedemptionActive   RedemptionStatus = "ACTIVE"
	RedemptionReleased RedemptionStatus = "RELEASED"
)

var ErrRedemptionClosed = domainerr.New(domainerr.KindInvalidState, "promotions.redemption_closed", "redemption is not active")

// Redemption is one use of a campaign by a booking. It is written in the
// booking transaction; persistence keeps the usage count consistent under
// concurrency (row lock on the campaign counter or a unique per-use row), so
// two requests cannot exceed UsageLimitTotal.
type Redemption struct {
	ID         RedemptionID
	CampaignID CampaignID
	BookingRef string
	CustomerID identity.UserID
	Status     RedemptionStatus
	CreatedAt  time.Time
	ReleasedAt *time.Time
	Reason     string
}

// Redeem records the use of every applied campaign.
func Redeem(ids func(CampaignID) RedemptionID, applied []Applied, bookingRef string, customer identity.UserID, now time.Time) ([]Redemption, error) {
	if bookingRef == "" || customer == "" {
		return nil, domainerr.Invalid("redemption", "booking and customer are required")
	}
	out := make([]Redemption, 0, len(applied))
	for _, a := range applied {
		id := ids(a.CampaignID)
		if id == "" {
			return nil, domainerr.Invalid("redemption_id", "required")
		}
		out = append(out, Redemption{ID: id, CampaignID: a.CampaignID, BookingRef: bookingRef, CustomerID: customer, Status: RedemptionActive, CreatedAt: now.UTC()})
	}
	return out, nil
}

// Release frees the use when the booking is cancelled or expires (owner
// decision D12); completed bookings keep it. Releasing twice is a no-op.
func (r *Redemption) Release(reason string, now time.Time) (bool, error) {
	if r.Status == RedemptionReleased {
		return false, nil
	}
	if r.Status != RedemptionActive {
		return false, fmt.Errorf("redemption %s: %w", r.ID, ErrRedemptionClosed)
	}
	if strings.TrimSpace(reason) == "" {
		return false, domainerr.Invalid("reason", "required")
	}
	t := now.UTC()
	r.Status, r.ReleasedAt, r.Reason = RedemptionReleased, &t, reason
	return true, nil
}

// CountUsage derives the Usage of a campaign from its redemptions.
func CountUsage(rs []Redemption, campaign CampaignID, customer identity.UserID) Usage {
	var u Usage
	for _, r := range rs {
		if r.CampaignID != campaign || r.Status != RedemptionActive {
			continue
		}
		u.Total++
		if r.CustomerID == customer {
			u.ForCustomer++
		}
	}
	return u
}
