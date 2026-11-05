package httpapi_test

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"webee_rentor/internal/platform/postgres/pgtest"
)

type deliveryBody struct {
	ID           string  `json:"id"`
	BookingID    string  `json:"booking_id"`
	Direction    string  `json:"direction"`
	Status       string  `json:"status"`
	ShipmentCode *string `json:"shipment_code"`
	Address      struct {
		RecipientName string `json:"recipient_name"`
		Phone         string `json:"phone"`
		Line          string `json:"line"`
		WardName      string `json:"ward_name"`
	} `json:"address"`
	Attempts []struct {
		ID    string `json:"id"`
		Fault string `json:"fault"`
	} `json:"attempts"`
	Actions        []string `json:"actions"`
	AwaitingAction bool     `json:"awaiting_follow_up"`
}

// courierOrder is one jacket delivered and collected by courier on Tuesday
// 2026-11-10 (fixture windows), to a fake address in ward W1.
func (s *shop) courierOrder(key string) string {
	return fmt.Sprintf(`{"package_id":%q,"pickup_date":"2026-11-10","slot_id":"deliver-am","outbound":"COURIER","return":"COURIER",
		"lines":[{"kind":"ITEM","offering_id":%q,"variant_id":%q,"quantity":1}],"request_key":%q,
		"address":{"recipient_name":"Khách Test","phone":"0911111111","line":"2 Đường B","ward_code":"W1"}}`,
		s.c.OneDay.ID, s.c.Jacket.ID, s.c.JacketM, key)
}

func (s *shop) deliveries(cookie *http.Cookie, booking string) []deliveryBody {
	s.t.Helper()
	return decode[struct {
		Items []deliveryBody `json:"items"`
	}](s.t, s.send(cookie, "GET", "/bookings/"+booking+"/deliveries", "", http.StatusOK)).Items
}

func leg(ds []deliveryBody, direction string) deliveryBody {
	for _, d := range ds {
		if d.Direction == direction && d.Status != "CANCELLED" && d.Status != "CLOSED_UNFULFILLED" {
			return d
		}
	}
	return deliveryBody{}
}

// Decision D43: checkout offers the courier once an area is loaded, and staff
// run each shipment by hand from the carrier's app.
func TestCourierCheckoutAndManualDelivery(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 1)
	opts := s.send(nil, "GET", "/store/fulfillment-options", "", http.StatusOK).Body.String()
	if !strings.Contains(opts, `"COURIER"`) || !strings.Contains(opts, "Phường Một") {
		t.Fatalf("fulfillment options = %s", opts)
	}
	cal := s.send(nil, "GET", "/store/calendar?from=2026-11-10&to=2026-11-10&outbound=COURIER", "", http.StatusOK).Body.String()
	if !strings.Contains(cal, `"deliver-am"`) || strings.Contains(cal, `"morning"`) {
		t.Fatalf("courier calendar = %s", cal)
	}
	far := strings.Replace(s.courierOrder("k0"), `"ward_code":"W1"`, `"ward_code":"W9"`, 1)
	s.expect(s.do(call{method: "POST", path: "/api/v1/bookings", body: far, cookie: s.customer}), http.StatusBadRequest, "delivery.out_of_service_area")

	b := decode[detailBody](t, s.send(s.customer, "POST", "/bookings", s.courierOrder("k1"), http.StatusCreated))
	path := "/bookings/" + b.ID
	mine := s.deliveries(s.customer, b.ID)
	out := leg(mine, "OUTBOUND")
	if len(mine) != 2 || out.Address.Line != "2 Đường B" || out.Address.WardName != "Phường Một" || len(out.Actions) != 0 {
		t.Fatalf("customer view = %+v", mine)
	}
	s.send(s.signIn("stranger"), "GET", "/bookings/"+b.ID+"/deliveries", "", http.StatusForbidden)
	board := decode[struct {
		Items []deliveryBody `json:"items"`
	}](t, s.send(s.staff, "GET", "/delivery/tasks", "", http.StatusOK))
	if !slices.ContainsFunc(board.Items, func(d deliveryBody) bool { return d.ID == out.ID && slices.Contains(d.Actions, "RECORD_SHIPMENT") }) {
		t.Fatalf("board = %+v", board.Items)
	}

	s.act(s.money, path+"/payments", pay("p1", "CASH", map[string]int64{"RESERVATION": 15_000}))
	s.act(s.staff, path+"/confirm", "")
	s.act(s.staff, path+"/start-preparation", "")
	s.act(s.money, path+"/payments", pay("p2", "CASH", map[string]int64{"RENTAL": 95_000, "SECURITY": 300_000}))
	handover := fmt.Sprintf(`{"checklist_completed":true,"delivered":[{"asset_id":%q,"preexisting_issues":""}]}`, s.firstAsset(path))
	s.expect(s.do(call{method: "POST", path: "/api/v1" + path + "/handover", body: handover, cookie: s.staff}), http.StatusConflict, "delivery.shipment_required")
	s.send(s.staff, "POST", "/delivery/tasks/"+out.ID+"/shipment", `{"shipment_code":"GHN123"}`, http.StatusOK)
	s.shop.Set(pgtest.Local(t, "2026-11-10 06:00"))
	s.act(s.staff, path+"/handover", handover)
	if out = leg(s.deliveries(s.staff, b.ID), "OUTBOUND"); out.Status != "PICKED_UP" || out.ShipmentCode == nil || *out.ShipmentCode != "GHN123" {
		t.Fatalf("after handover = %+v", out)
	}

	s.shop.Set(pgtest.Local(t, "2026-11-10 10:00"))
	w := s.send(s.staff, "POST", "/delivery/tasks/"+out.ID+"/events", `{"status":"DELIVERY_FAILED","gave_up":true,"reason":"hết số lần giao"}`, http.StatusOK)
	out = decode[deliveryBody](t, w)
	if !out.AwaitingAction || slices.Contains(out.Actions, "CLOSE") || !slices.Contains(out.Actions, "CLASSIFY_ATTEMPT") {
		t.Fatalf("given up, not classified = %+v", out)
	}
	s.send(s.staff, "POST", "/delivery/tasks/"+out.ID+"/classify", fmt.Sprintf(`{"attempt_id":%q,"fault":"CUSTOMER","reason":"khách tắt máy"}`, out.Attempts[0].ID), http.StatusOK)
	s.send(s.staff, "POST", "/delivery/tasks/"+out.ID+"/close", `{"follow_up":"END_BOOKING","reason":"kiện đã về kho"}`, http.StatusOK)
	if got := decode[detailBody](t, s.send(s.staff, "GET", path, "", http.StatusOK)); got.Status != "RETURN_INSPECTION" {
		t.Fatalf("booking after ending undelivered = %s", got.Status)
	}
}

// firstAsset is the asset allocated to the booking.
func (s *shop) firstAsset(path string) string {
	s.t.Helper()
	var id string
	if err := s.f.Pool.QueryRow(s.t.Context(), `SELECT asset_id FROM allocations WHERE booking_id = $1 AND status = 'ACTIVE' LIMIT 1`,
		strings.TrimPrefix(path, "/bookings/")).Scan(&id); err != nil {
		s.t.Fatal(err)
	}
	return id
}

// Decision D43: delivery addresses follow the contact rule of D40.
func TestDeliveryAddressFollowsTheContactRule(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 1)
	b := decode[detailBody](t, s.send(s.customer, "POST", "/bookings", s.courierOrder("k1"), http.StatusCreated))
	s.act(s.staff, "/bookings/"+b.ID+"/cancel", `{"reason":"STAFF_DECISION","note":"khách đổi ý"}`)
	if d := leg(s.deliveries(s.staff, b.ID), "OUTBOUND"); d.ID != "" {
		t.Fatalf("a cancelled booking kept a shipment in progress: %+v", d)
	}
	all := s.deliveries(s.staff, b.ID)
	if all[0].Address.Line != "2 Đường B" || all[0].Status != "CANCELLED" {
		t.Fatalf("within 30 days = %+v", all[0])
	}
	s.shop.Set(pgtest.Now.AddDate(0, 0, 31))
	all = s.deliveries(s.staff, b.ID)
	if a := all[0].Address; a.Line != "" || a.Phone != "***111" || a.RecipientName != "Khách Test" || a.WardName != "Phường Một" {
		t.Fatalf("after the contact period = %+v", a)
	}
	if a := s.deliveries(s.customer, b.ID)[0].Address; a.Line != "2 Đường B" {
		t.Fatalf("the customer always sees their own address: %+v", a)
	}
}
