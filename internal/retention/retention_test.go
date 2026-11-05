package retention_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"webee_rentor/internal/retention"
)

var now = time.Date(2028, 11, 1, 2, 0, 0, 0, time.UTC)

func TestAProfileIsKeptWhileTheStoreMayNeedIt(t *testing.T) {
	unused := now.AddDate(0, -24, 0).Add(-time.Minute)
	cases := map[string]struct {
		facts retention.ProfileFacts
		want  []retention.Blocker
	}{
		"unused 24 months, nothing open": {retention.ProfileFacts{LastActivity: unused}, nil},
		"used exactly 24 months ago":     {retention.ProfileFacts{LastActivity: now.AddDate(0, -24, 0)}, []retention.Blocker{retention.BlockRecentlyInUse}},
		"open booking":                   {retention.ProfileFacts{LastActivity: unused, OpenBookings: 1}, []retention.Blocker{retention.BlockOpenBooking}},
		"money not settled":              {retention.ProfileFacts{LastActivity: unused, UnsettledBookings: 1}, []retention.Blocker{retention.BlockMoneyOpen}},
		"fee disputed":                   {retention.ProfileFacts{LastActivity: unused, OpenFees: 1}, []retention.Blocker{retention.BlockOpenFee}},
		"support open":                   {retention.ProfileFacts{LastActivity: unused, OpenSupport: 1}, []retention.Blocker{retention.BlockOpenSupport}},
	}
	for name, c := range cases {
		got := c.facts.Blockers(now)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: blockers %v, want %v", name, got, c.want)
		}
		if err := c.facts.CheckErasable(now); (err == nil) != (len(c.want) == 0) || (err != nil && !errors.Is(err, retention.ErrProfileKept)) {
			t.Errorf("%s: CheckErasable = %v", name, err)
		}
	}
}

func TestRetentionCountsCalendarMonths(t *testing.T) {
	if got := retention.ProfileReviewBefore(now); !got.Equal(time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("ProfileReviewBefore = %s", got)
	}
	if got := retention.SupportPurgeBefore(now); !got.Equal(time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("SupportPurgeBefore = %s", got)
	}
}
