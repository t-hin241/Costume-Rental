package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"webee_rentor/internal/platform/postgres/pgtest"
)

type reviewBody struct {
	UserID       string    `json:"user_id"`
	FullName     string    `json:"full_name"`
	LastActivity time.Time `json:"last_activity_at"`
	Blockers     []string  `json:"blockers"`
}

// reviewed reads every page of the profiles due for review. Other tests share
// the database and the same clock, so the list holds their customers too.
func (s *shop) reviewed() map[string]reviewBody {
	s.t.Helper()
	out := map[string]reviewBody{}
	for page := 1; ; page++ {
		body := decode[struct {
			Items []reviewBody `json:"items"`
			Total int          `json:"total"`
		}](s.t, s.send(s.a, "GET", "/admin/profile-retention?page_size=100&page="+strconv.Itoa(page), "", http.StatusOK))
		for _, r := range body.Items {
			out[r.UserID] = r
		}
		if len(body.Items) == 0 || page*100 >= body.Total {
			return out
		}
	}
}

// Decision D39: a customer's name and phone stay while the account is in use.
// After 24 months unused an admin reviews the profile and may erase it, unless
// the store may still need to reach the customer.
func TestAnUnusedProfileIsErasedOnlyAfterReview(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 3)
	person := func(name string) (*http.Cookie, string) {
		email, id := s.account(name)
		c, _ := s.login(email)
		s.send(c, "PUT", "/me/profile", fmt.Sprintf(`{"full_name":"Khách %s %s","phone":"0901 234 567"}`, name, s.f.Prefix), http.StatusOK)
		return c, string(id)
	}
	gone, goneID := person("gone")
	done := s.place(gone, "k1", "")
	s.act(gone, "/bookings/"+done.ID+"/cancel", `{"reason":"CUSTOMER_REQUEST","note":""}`)
	// Still holding a booking.
	busy, busyID := person("busy")
	s.place(busy, "k2", "")
	// Paid, cancelled, refund not paid out yet.
	owed, owedID := person("owed")
	refund := s.place(owed, "k3", "")
	s.act(s.money, "/bookings/"+refund.ID+"/payments", pay("p1", "CASH", map[string]int64{"RESERVATION": 15_000}))
	s.act(owed, "/bookings/"+refund.ID+"/cancel", `{"reason":"CUSTOMER_REQUEST","note":""}`)
	// Waiting for an answer.
	asking, askingID := person("asking")
	s.send(asking, "POST", "/me/support-requests", `{"topic":"OTHER","subject":"Hỏi","booking_code":"","message":"Hỏi"}`, http.StatusCreated)
	// Came back a year later.
	_, backID := person("back")
	if _, err := s.f.Pool.Exec(context.Background(), `UPDATE sessions SET last_seen_at = $2 WHERE user_id = $1`, backID, pgtest.Now.AddDate(1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	// Staff profiles are not customer profiles.
	s.send(s.staff, "PUT", "/me/profile", `{"full_name":"Nhân viên","phone":"0900 000 001"}`, http.StatusOK)
	staffID := decode[struct {
		ID string `json:"id"`
	}](t, s.send(s.staff, "GET", "/auth/me", "", http.StatusOK)).ID

	erase := func(id, reason string, status int) {
		t.Helper()
		s.send(s.a, "POST", "/admin/users/"+id+"/profile-erase", fmt.Sprintf(`{"reason":%q}`, reason), status)
	}
	months24 := pgtest.Now.AddDate(0, 24, 0)
	s.shop.Set(months24.Add(-time.Minute))
	if _, listed := s.reviewed()[goneID]; listed {
		t.Fatal("a profile is up for review before 24 months")
	}
	erase(goneID, "Hết thời hạn lưu", http.StatusConflict)

	s.shop.Set(months24.Add(time.Minute))
	got := s.reviewed()
	want := map[string][]string{goneID: {}, busyID: {"OPEN_BOOKING"}, owedID: {"MONEY_OPEN"}, askingID: {"OPEN_SUPPORT"}}
	for id, blockers := range want {
		r, ok := got[id]
		if !ok || !slices.Equal(r.Blockers, blockers) || r.FullName == "" {
			t.Fatalf("review of %s = %+v (listed %v), want blockers %v", id, r, ok, blockers)
		}
	}
	for _, id := range []string{backID, staffID} {
		if _, listed := got[id]; listed {
			t.Fatalf("%s must not be up for review", id)
		}
	}
	for _, id := range []string{busyID, owedID, askingID} {
		erase(id, "Hết thời hạn lưu", http.StatusConflict)
	}
	s.expect(s.do(call{method: "POST", path: "/api/v1/admin/users/" + goneID + "/profile-erase", body: `{"reason":" "}`, cookie: s.a}), http.StatusBadRequest, "")

	erase(goneID, "Hết thời hạn lưu", http.StatusNoContent)
	if p := decode[map[string]string](t, s.send(gone, "GET", "/me/profile", "", http.StatusOK)); p["full_name"] != "" || p["phone"] != "" || p["email"] == "" {
		t.Fatalf("profile after erasure = %+v", p)
	}
	// The booking stays and now names the customer by email.
	if c := decode[contactBody](t, s.send(s.a, "GET", "/bookings/"+done.ID, "", http.StatusOK)).Customer; c.Name != s.f.Prefix+"-gone@example.test" || c.Phone != "" {
		t.Fatalf("booking customer after erasure = %+v", c)
	}
	if _, listed := s.reviewed()[goneID]; listed {
		t.Fatal("an erased profile is still up for review")
	}
	if n := s.count(`SELECT count(*) FROM audit_events WHERE action = 'customer.profile_erased' AND resource_id = $1 AND reason = 'Hết thời hạn lưu'
		AND actor_kind = 'user' AND refs::text NOT LIKE '%0901%'`, goneID); n != 1 {
		t.Fatalf("%d audit records of the erasure", n)
	}
	erase(goneID, "Hết thời hạn lưu", http.StatusNotFound)
	// Only admins review and erase; the route sweep covers every role.
	s.expect(s.do(call{method: "POST", path: "/api/v1/admin/users/" + busyID + "/profile-erase", body: `{"reason":"x"}`, cookie: s.money}), http.StatusForbidden, "")
}
