package httpapi_test

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/rentals"
)

type contactBody struct {
	Customer struct {
		Name, Phone string
	} `json:"customer"`
}

// Staff call customers about live bookings. A month after a booking ended
// with its money settled, they still see whose it was but not the number, and
// cannot find it by that number (decision D40).
func TestStaffStopSeeingAPhoneNumberAMonthAfterTheBookingEnded(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 2)
	// Tests share the database, so the name is this test's own.
	name := "Khách " + s.f.Prefix
	s.send(s.customer, "PUT", "/me/profile", fmt.Sprintf(`{"full_name":%q,"phone":"0901 234 567"}`, name), http.StatusOK)
	settled := s.place(s.customer, "k1", "")
	s.act(s.customer, "/bookings/"+settled.ID+"/cancel", `{"reason":"CUSTOMER_REQUEST","note":""}`)
	// Paid, then cancelled: the refund is still to be paid out.
	refunding := s.place(s.customer, "k2", "")
	s.act(s.money, "/bookings/"+refunding.ID+"/payments", pay("p1", "CASH", map[string]int64{"RESERVATION": 15_000}))
	s.act(s.customer, "/bookings/"+refunding.ID+"/cancel", `{"reason":"CUSTOMER_REQUEST","note":""}`)

	phoneOf := func(who string, id string) string {
		t.Helper()
		cookie := map[string]*http.Cookie{"staff": s.staff, "finance": s.money, "admin": s.a, "customer": s.customer}[who]
		return decode[contactBody](t, s.send(cookie, "GET", "/bookings/"+id, "", http.StatusOK)).Customer.Phone
	}
	found := func(who string, term string) int {
		t.Helper()
		cookie := map[string]*http.Cookie{"staff": s.staff, "admin": s.a}[who]
		return decode[struct {
			Total int `json:"total"`
		}](t, s.send(cookie, "GET", "/bookings?q="+url.QueryEscape(term), "", http.StatusOK)).Total
	}

	s.shop.Set(pgtest.Now.Add(rentals.ContactAfterEnd - time.Minute)) // a minute before the month is up
	if got := phoneOf("staff", settled.ID); got != "0901 234 567" {
		t.Fatalf("within the month staff see %q", got)
	}
	if n := found("staff", "234 567"); n != 2 {
		t.Fatalf("within the month a phone search finds %d bookings", n)
	}

	s.shop.Set(pgtest.Now.Add(rentals.ContactAfterEnd))
	for _, who := range []string{"staff", "finance"} {
		if got := phoneOf(who, settled.ID); got != "***567" {
			t.Fatalf("%s see %q on a booking settled a month ago", who, got)
		}
		if got := phoneOf(who, refunding.ID); got != "0901 234 567" {
			t.Fatalf("%s see %q on a booking with a refund to pay", who, got)
		}
	}
	if got := decode[contactBody](t, s.send(s.staff, "GET", "/bookings/"+settled.ID, "", http.StatusOK)).Customer.Name; got != name {
		t.Fatalf("staff must still see whose booking it was: %q", got)
	}
	for _, who := range []string{"admin", "customer"} {
		if got := phoneOf(who, settled.ID); got != "0901 234 567" {
			t.Fatalf("%s see %q", who, got)
		}
	}
	// The search follows the list, not the ledger: a finished booking past the
	// month is not found by number even while its refund is open.
	if n := found("staff", "234 567"); n != 0 {
		t.Fatalf("staff find %d bookings by an old phone number", n)
	}
	if n := found("staff", name); n != 2 {
		t.Fatalf("staff find %d bookings by name", n)
	}
	if n := found("staff", settled.Code); n != 1 {
		t.Fatalf("staff find %d bookings by code", n)
	}
	if n := found("admin", "234 567"); n != 2 {
		t.Fatalf("admin find %d bookings by phone", n)
	}
}
