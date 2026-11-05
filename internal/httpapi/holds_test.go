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
