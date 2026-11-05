package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"webee_rentor/internal/platform/postgres/pgtest"
)

// holdsPolicy is the active policy with another limit on unpaid holds, ready to
// post to /admin/policies.
func (s *shop) holdsPolicy(n int) string {
	s.t.Helper()
	active := decode[map[string]any](s.t, s.send(s.a, "GET", "/admin/policies/active", "", http.StatusOK))
	active["version"] = fmt.Sprintf("%s-holds-%d", s.f.Prefix, n)
	active["booking"].(map[string]any)["max_unpaid_holds"] = n
	body, err := json.Marshal(map[string]any{"policy": active, "reason": "Giới hạn đơn chưa thanh toán"})
	if err != nil {
		s.t.Fatal(err)
	}
	return string(body)
}

func TestUnpaidHoldsAreLimitedForEachCustomer(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 3)
	s.expect(s.do(call{method: "POST", path: "/api/v1/admin/policies", body: s.holdsPolicy(0), cookie: s.a}), http.StatusBadRequest, "validation_failed")
	s.send(s.a, "POST", "/admin/policies", s.holdsPolicy(1), http.StatusCreated)
	// The next test must find the fixture policy again.
	t.Cleanup(func() {
		if err := pgtest.SeedConfig(context.Background(), s.f.Pool); err != nil {
			t.Fatal(err)
		}
	})
	public := decode[struct {
		MaxUnpaidHolds int `json:"max_unpaid_holds"`
	}](t, s.send(nil, "GET", "/policies/public", "", http.StatusOK))
	if public.MaxUnpaidHolds != 1 {
		t.Fatalf("public policy = %+v", public)
	}

	first := s.place(s.customer, "k1", "")
	second := s.order(s.c.JacketM, "2026-11-11", "k2", "")
	s.expect(s.do(call{method: "POST", path: "/api/v1/bookings", body: second, cookie: s.customer}), http.StatusConflict, "rentals.too_many_unpaid_holds")
	if again := s.place(s.customer, "k1", ""); again.ID != first.ID {
		t.Fatalf("retry made booking %s, want %s", again.ID, first.ID)
	}
	s.place(s.signIn("other"), "k1", "")

	s.act(s.money, "/bookings/"+first.ID+"/payments", pay("p1", "CASH", map[string]int64{"RESERVATION": 15_000}))
	s.place(s.customer, "k2", "")
}

// Rent 50,000đ, so 30% is 15,000đ, covered by the reservation deposit paid.
func TestAMovedBookingIsChargedThePreviewedFee(t *testing.T) {
	cases := []struct {
		name   string
		booked string
		moves  []string
		fee    int64
	}{
		{"two days away, moved far away", "2026-11-03", []string{"2026-11-24"}, 15_000},
		{"far away, moved close, moved far away again", "2026-11-24", []string{"2026-11-03", "2026-11-25"}, 15_000},
		{"far away and never moved", "2026-11-24", nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newShop(t)
			s.f.Assets(s.c.JacketM, 1)
			b := decode[detailBody](t, s.send(s.customer, "POST", "/bookings", s.order(s.c.JacketM, c.booked, "k1", ""), http.StatusCreated))
			path := "/bookings/" + b.ID
			s.act(s.money, path+"/payments", pay("p1", "CASH", map[string]int64{"RESERVATION": 15_000}))
			s.act(s.staff, path+"/confirm", "")
			for _, to := range c.moves {
				s.act(s.customer, path+"/reschedule", fmt.Sprintf(`{"pickup_date":%q,"slot_id":"morning"}`, to))
			}

			preview := decode[struct {
				Fee moneyBody `json:"fee"`
			}](t, s.send(s.customer, "GET", path+"/cancellation-preview", "", http.StatusOK))
			if preview.Fee.Amount != c.fee {
				t.Fatalf("preview fee = %d, want %d", preview.Fee.Amount, c.fee)
			}
			got := s.act(s.customer, path+"/cancel", `{"reason":"CUSTOMER_REQUEST","note":"Đổi kế hoạch"}`)
			if want := 15_000 - c.fee; got.Finance.RefundPending.Amount != want {
				t.Fatalf("refund pending = %d, want %d", got.Finance.RefundPending.Amount, want)
			}
		})
	}
}
