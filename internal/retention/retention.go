// Package retention decides how long personal data is kept (decision D39):
// a customer profile while the account is in use, and a support conversation
// for a fixed time after it was resolved. It holds the rules only; the use
// cases read the facts and delete.
package retention

import (
	"time"

	"webee_rentor/internal/domainerr"
)

const (
	// ProfileInactiveMonths is how long an account goes unused before its
	// profile comes up for review. An admin decides; nothing is deleted on
	// its own.
	ProfileInactiveMonths = 24
	// SupportKeptMonths is how long a resolved conversation is kept.
	SupportKeptMonths = 24
)

// ProfileReviewBefore is the activity time before which a profile is due for
// review.
func ProfileReviewBefore(now time.Time) time.Time {
	return now.UTC().AddDate(0, -ProfileInactiveMonths, 0)
}

// SupportPurgeBefore is the resolution time before which a conversation is
// deleted.
func SupportPurgeBefore(now time.Time) time.Time {
	return now.UTC().AddDate(0, -SupportKeptMonths, 0)
}

// Blocker is a reason a profile must stay even though the account is unused:
// the store may still need to reach the customer.
type Blocker string

const (
	BlockOpenBooking   Blocker = "OPEN_BOOKING"
	BlockMoneyOpen     Blocker = "MONEY_OPEN"
	BlockOpenFee       Blocker = "OPEN_FEE"
	BlockOpenSupport   Blocker = "OPEN_SUPPORT"
	BlockRecentlyInUse Blocker = "RECENTLY_ACTIVE"
)

// ProfileFacts is what decides whether a profile may be erased.
type ProfileFacts struct {
	// LastActivity is the latest of: account creation, profile update,
	// session use, booking made or ended, support message written.
	LastActivity time.Time
	// OpenBookings counts bookings not yet completed, cancelled or expired.
	OpenBookings int
	// UnsettledBookings counts finished bookings whose money is not closed:
	// a debt, a refund, a deposit or a payment still pending.
	UnsettledBookings int
	// OpenFees counts fees still proposed or disputed.
	OpenFees int
	// OpenSupport counts support requests not resolved.
	OpenSupport int
}

// Blockers lists why the profile must stay; empty means an admin may erase it.
func (f ProfileFacts) Blockers(now time.Time) []Blocker {
	var out []Blocker
	if !f.LastActivity.Before(ProfileReviewBefore(now)) {
		out = append(out, BlockRecentlyInUse)
	}
	if f.OpenBookings > 0 {
		out = append(out, BlockOpenBooking)
	}
	if f.UnsettledBookings > 0 {
		out = append(out, BlockMoneyOpen)
	}
	if f.OpenFees > 0 {
		out = append(out, BlockOpenFee)
	}
	if f.OpenSupport > 0 {
		out = append(out, BlockOpenSupport)
	}
	return out
}

// ErrProfileKept means the profile may not be erased yet.
var ErrProfileKept = domainerr.New(domainerr.KindInvalidState, "retention.profile_kept",
	"this profile is still in use or the store may still need to reach the customer")

// ErrReasonRequired means an erasure was asked for without a reason.
var ErrReasonRequired = domainerr.New(domainerr.KindInvalid, "retention.reason_required", "a reason is required")

// CheckErasable reports whether the profile may be erased now.
func (f ProfileFacts) CheckErasable(now time.Time) error {
	if len(f.Blockers(now)) > 0 {
		return ErrProfileKept
	}
	return nil
}
