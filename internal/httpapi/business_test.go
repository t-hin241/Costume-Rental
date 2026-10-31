package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/httpapi"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/platform/ratelimit"
)

type moneyBody struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

type offeringBody struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Slug           string   `json:"slug"`
	Name           string   `json:"name"`
	Sizes          []string `json:"sizes"`
	ComponentCount int      `json:"component_count"`
	FromPrice      moneyBody
}

type transitionBody struct {
	Action, Actor, Note string
}

type detailBody struct {
	ID           string   `json:"id"`
	Code         string   `json:"code"`
	Status       string   `json:"status"`
	PaymentState string   `json:"payment_state"`
	Actions      []string `json:"actions"`
	Headline     string   `json:"headline"`
	Customer     struct {
		ID, Name string
	} `json:"customer"`
	Items []struct {
		AssetID string `json:"asset_id"`
		State   string `json:"state"`
	} `json:"items"`
	Lines []struct {
		Selection string `json:"selection"`
	} `json:"lines"`
	Transitions []transitionBody `json:"transitions"`
	Finance     struct {
		PaidTotal     moneyBody `json:"paid_total"`
		SecurityHeld  moneyBody `json:"security_held"`
		Outstanding   moneyBody `json:"outstanding"`
		RefundPending moneyBody `json:"refund_pending"`
		Charges       []struct{ Kind string }
		Payments      []struct{ Method string }
		Refunds       []struct {
			ID      string
			Status  string
			Actions []string
		}
	} `json:"finance"`
}

// shop is a store with a catalog and one signed-in person per role.
type shop struct {
	*api
	c                         pgtest.Catalog
	customer, staff, money, a *http.Cookie
}

func newShop(t *testing.T) *shop {
	t.Helper()
	a := newAPI(t, options{})
	s := &shop{api: a, c: a.f.Catalog()}
	s.customer = s.signIn("customer")
	s.staff = s.signIn("staff", identity.RoleRentalStaff)
	s.money = s.signIn("finance", identity.RoleFinance)
	s.a = s.signIn("admin", identity.RoleAdmin)
	return s
}

func (s *shop) signIn(name string, roles ...identity.Role) *http.Cookie {
	s.t.Helper()
	email, _ := s.account(name, roles...)
	c, _ := s.login(email)
	return c
}

func (s *shop) send(cookie *http.Cookie, method, path, body string, status int) *httptest.ResponseRecorder {
	s.t.Helper()
	w := s.do(call{method: method, path: "/api/v1" + path, body: body, cookie: cookie})
	s.expect(w, status, "")
	return w
}

func (s *shop) act(cookie *http.Cookie, path, body string) detailBody {
	s.t.Helper()
	return decode[detailBody](s.t, s.send(cookie, "POST", path, body, http.StatusOK))
}

// order is one jacket for one day, picked up on the given date.
func (s *shop) order(variant catalog.VariantID, date, key, extra string) string {
	return fmt.Sprintf(`{"package_id":%q,"pickup_date":%q,"slot_id":"morning","outbound":"STORE","return":"STORE",
		"lines":[{"kind":"ITEM","offering_id":%q,"variant_id":%q,"quantity":1}],"request_key":%q%s}`,
		s.c.OneDay.ID, date, s.c.Jacket.ID, variant, key, extra)
}

func (s *shop) place(customer *http.Cookie, key, extra string) detailBody {
	s.t.Helper()
	return decode[detailBody](s.t, s.send(customer, "POST", "/bookings", s.order(s.c.JacketM, "2026-11-11", key, extra), http.StatusCreated))
}

func pay(key, method string, portions map[string]int64) string {
	parts := []string{}
	for bucket, amount := range portions {
		parts = append(parts, fmt.Sprintf(`{"bucket":%q,"amount":{"amount":%d,"currency":"VND"}}`, bucket, amount))
	}
	return fmt.Sprintf(`{"idempotency_key":%q,"method":%q,"portions":[%s]}`, key, method, strings.Join(parts, ","))
}

func TestGuestsBrowseTheStorefront(t *testing.T) {
	a := newAPI(t, options{})
	s := &shop{api: a, c: a.f.Catalog()}
	s.f.Assets(s.c.JacketM, 2)

	page := decode[struct {
		Items  []offeringBody `json:"items"`
		Total  int            `json:"total"`
		Facets struct {
			Sizes []string `json:"sizes"`
		} `json:"facets"`
	}](t, s.send(nil, "GET", "/catalog/offerings?page_size=100", "", http.StatusOK))
	mine := map[string]offeringBody{}
	for _, o := range page.Items {
		mine[o.Slug] = o
	}
	if o := mine[s.c.Jacket.Slug]; o.Kind != "ITEM" || !slices.Equal(o.Sizes, []string{"L", "M"}) && !slices.Equal(o.Sizes, []string{"M", "L"}) {
		t.Fatalf("jacket = %+v", o)
	}
	if o := mine[s.c.Set.Slug]; o.Kind != "BUNDLE" || o.ComponentCount != 3 {
		t.Fatalf("bundle = %+v", o)
	}
	if _, ok := mine[s.c.Belt.Slug]; ok {
		t.Fatal("an item only rentable in a bundle must not be listed alone")
	}

	detail := decode[struct {
		Prices          []struct{ Price moneyBody }
		SecurityDeposit moneyBody `json:"security_deposit"`
		Variants        []struct{ SKU string }
	}](t, s.send(nil, "GET", "/catalog/offerings/ITEM/"+s.c.Jacket.Slug, "", http.StatusOK))
	if len(detail.Prices) != 2 || len(detail.Variants) != 2 || detail.SecurityDeposit.Amount != 300_000 {
		t.Fatalf("detail = %+v", detail)
	}
	s.expect(s.do(call{method: "GET", path: "/api/v1/catalog/offerings/BOTH/" + s.c.Jacket.Slug}), http.StatusNotFound, "request.not_found")

	info := decode[map[string]any](t, s.send(nil, "GET", "/store/info", "", http.StatusOK))
	if info["online_payment_available"] != false || info["name"] != "Cửa hàng thử" {
		t.Fatalf("store = %+v", info)
	}
	calendar := decode[struct {
		Timezone string
		MinDate  string `json:"min_date"`
		Days     []struct {
			Date   string
			Open   bool
			Reason *string
			Slots  []struct{ ID, Label string }
		}
	}](t, s.send(nil, "GET", "/store/calendar?from=2026-11-19&to=2026-11-21", "", http.StatusOK))
	// Today is a closed Sunday, so the first bookable day is tomorrow.
	if calendar.Timezone != "Asia/Ho_Chi_Minh" || calendar.MinDate != "2026-11-02" || len(calendar.Days) != 3 {
		t.Fatalf("calendar = %+v", calendar)
	}
	if !calendar.Days[0].Open || len(calendar.Days[0].Slots) != 2 || calendar.Days[1].Open || calendar.Days[1].Reason == nil {
		t.Fatalf("calendar days = %+v", calendar.Days)
	}
	s.send(nil, "GET", "/store/calendar?from=2026-11-19", "", http.StatusBadRequest)

	policy := decode[struct {
		HoldMinutes int `json:"hold_minutes"`
		Reservation struct {
			RentPercent *int `json:"rent_percent"`
		}
	}](t, s.send(nil, "GET", "/policies/public", "", http.StatusOK))
	if policy.HoldMinutes != 30 || policy.Reservation.RentPercent == nil || *policy.Reservation.RentPercent != 30 {
		t.Fatalf("policy = %+v", policy)
	}
}

func TestAvailabilityAndQuoteAreAnsweredForGuests(t *testing.T) {
	a := newAPI(t, options{})
	s := &shop{api: a, c: a.f.Catalog()}
	s.f.Assets(s.c.JacketM, 1)

	line := func(qty int) string {
		return fmt.Sprintf(`{"package_id":%q,"pickup_date":"2026-11-11","slot_id":"morning","outbound":"STORE","return":"STORE",
			"lines":[{"kind":"ITEM","offering_id":%q,"variant_id":%q,"quantity":%d}]}`, s.c.OneDay.ID, s.c.Jacket.ID, s.c.JacketM, qty)
	}
	type availability struct {
		Available bool
		Shortages []struct {
			Label     string
			Requested int
			Available int
		}
	}
	if got := decode[availability](t, s.send(nil, "POST", "/availability", line(1), http.StatusOK)); !got.Available {
		t.Fatalf("availability = %+v", got)
	}
	short := decode[availability](t, s.send(nil, "POST", "/availability", line(2), http.StatusOK))
	if short.Available || len(short.Shortages) != 1 || short.Shortages[0].Requested != 2 || short.Shortages[0].Available != 1 || short.Shortages[0].Label != "Áo khoác (M)" {
		t.Fatalf("availability = %+v", short)
	}

	q := decode[struct {
		Rent     moneyBody `json:"rent_total"`
		Deposit  moneyBody `json:"reservation_deposit"`
		Security moneyBody `json:"security_total"`
		Confirm  moneyBody `json:"due_to_confirm"`
		Handover moneyBody `json:"due_before_handover"`
		Refund   moneyBody `json:"refundable_total"`
		Lines    []struct {
			Label string
			Rent  moneyBody
		}
	}](t, s.send(nil, "POST", "/quotes", line(1), http.StatusOK))
	if q.Rent.Amount != 50_000 || q.Deposit.Amount != 15_000 || q.Security.Amount != 300_000 {
		t.Fatalf("quote = %+v", q)
	}
	if q.Confirm.Amount != 15_000 || q.Handover.Amount != 350_000 || q.Refund.Amount != 300_000 {
		t.Fatalf("quote totals = %+v", q)
	}
	if len(q.Lines) != 1 || q.Lines[0].Label != "Áo khoác (M)" {
		t.Fatalf("quote lines = %+v", q.Lines)
	}
}

func TestPublicQuotesAreRateLimited(t *testing.T) {
	limits := httpapi.DefaultRateLimits()
	limits.QuotePerIP = ratelimit.Rate{N: 1, Window: time.Minute}
	a := newAPI(t, options{limits: limits})
	s := &shop{api: a, c: a.f.Catalog()}
	body := s.order(s.c.JacketM, "2026-11-11", "", "")
	body = strings.Replace(body, `,"request_key":""`, "", 1)
	s.send(nil, "POST", "/quotes", body, http.StatusOK)
	s.expect(s.do(call{method: "POST", path: "/api/v1/quotes", body: body}), http.StatusTooManyRequests, "")
}

func TestABookingRunsFromPlacementToCompletionOverTheAPI(t *testing.T) {
	s := newShop(t)
	assets := s.f.Assets(s.c.JacketM, 1)

	b := s.place(s.customer, "k1", "")
	if b.Status != "PENDING_CONFIRMATION" || b.PaymentState != "UNPAID" || !strings.Contains(b.Lines[0].Selection, "M") {
		t.Fatalf("placed = %+v", b)
	}
	if !slices.Contains(b.Actions, "cancel") || slices.Contains(b.Actions, "confirm") {
		t.Fatalf("customer actions = %v", b.Actions)
	}
	if again := s.place(s.customer, "k1", ""); again.ID != b.ID {
		t.Fatalf("the same request key must return the same booking: %s, %s", again.ID, b.ID)
	}
	path := "/bookings/" + b.ID

	// The asset is taken, so nobody else gets it.
	other := s.signIn("other")
	s.expect(s.do(call{method: "POST", path: "/api/v1/bookings", body: s.order(s.c.JacketM, "2026-11-11", "k2", ""), cookie: other}), http.StatusConflict, "inventory.insufficient")

	paid := s.act(s.money, path+"/payments", pay("p1", "CASH", map[string]int64{"RESERVATION": 15_000}))
	if paid.Finance.PaidTotal.Amount != 15_000 || paid.PaymentState != "PARTIALLY_PAID" {
		t.Fatalf("after payment = %+v", paid.Finance)
	}
	if again := s.act(s.money, path+"/payments", pay("p1", "CASH", map[string]int64{"RESERVATION": 15_000})); again.Finance.PaidTotal.Amount != 15_000 {
		t.Fatalf("a repeated payment must not count twice: %+v", again.Finance)
	}

	staffView := decode[detailBody](t, s.send(s.staff, "GET", path, "", http.StatusOK))
	if !slices.Contains(staffView.Actions, "confirm") {
		t.Fatalf("staff actions = %v", staffView.Actions)
	}
	if got := s.act(s.staff, path+"/confirm", ""); got.Status != "CONFIRMED" {
		t.Fatalf("confirm = %s", got.Status)
	}
	if got := s.act(s.staff, path+"/start-preparation", ""); got.Status != "PREPARING" {
		t.Fatalf("preparation = %s", got.Status)
	}
	s.act(s.money, path+"/payments", pay("p2", "BANK_TRANSFER", map[string]int64{"RENTAL": 35_000, "SECURITY": 300_000}))

	s.shop.Set(pgtest.Now.AddDate(0, 0, 10))
	view := decode[detailBody](t, s.send(s.staff, "GET", path, "", http.StatusOK))
	if len(view.Items) != 1 || view.Items[0].AssetID != string(assets[0]) || !slices.Contains(view.Actions, "handover") {
		t.Fatalf("before handover = %+v", view)
	}
	out := s.act(s.staff, path+"/handover", fmt.Sprintf(`{"checklist_completed":true,"delivered":[{"asset_id":%q,"preexisting_issues":""}]}`, assets[0]))
	if out.Status != "RENTED" || out.Items[0].State != "WITH_CUSTOMER" {
		t.Fatalf("handover = %+v", out)
	}

	s.shop.Set(pgtest.Now.AddDate(0, 0, 11))
	back := s.act(s.staff, path+"/returns", fmt.Sprintf(`{"asset_ids":[%q]}`, assets[0]))
	if back.Status != "RETURN_INSPECTION" || back.Items[0].State != "RETURNED" {
		t.Fatalf("return = %+v", back)
	}
	inspected := s.act(s.staff, path+"/inspections", fmt.Sprintf(`{"asset_id":%q,"conditions":["NORMAL_WEAR"],"notes":"","evidence":[]}`, assets[0]))
	if !slices.Contains(decode[detailBody](t, s.send(s.money, "GET", path, "", http.StatusOK)).Actions, "settle") {
		t.Fatalf("settle not offered after inspection: %+v", inspected.Actions)
	}

	settled := s.act(s.money, path+"/settle", "")
	if len(settled.Finance.Refunds) == 0 || settled.Finance.RefundPending.Amount != 300_000 {
		t.Fatalf("settled = %+v", settled.Finance)
	}
	refund := settled.Finance.Refunds[0]
	if !slices.Contains(refund.Actions, "payout") {
		t.Fatalf("refund actions = %+v", refund)
	}
	done := s.act(s.money, path+"/refunds/"+refund.ID+"/payout", `{"method":"CASH","reference":"","paid_at":"2026-11-12T01:00:00Z"}`)
	if done.Finance.RefundPending.Amount != 0 {
		t.Fatalf("after payout = %+v", done.Finance)
	}
	if got := s.act(s.staff, path+"/complete", ""); got.Status != "COMPLETED" {
		t.Fatalf("complete = %s", got.Status)
	}

	staffFinal := decode[detailBody](t, s.send(s.staff, "GET", path, "", http.StatusOK))
	for _, tr := range staffFinal.Transitions {
		if strings.Contains(tr.Note, string(assets[0])) && tr.Note != string(assets[0]) || tr.Note == b.ID {
			t.Fatalf("history shows an internal id: %+v", tr)
		}
	}

	// The customer follows it all, in their own words.
	final := decode[detailBody](t, s.send(s.customer, "GET", path, "", http.StatusOK))
	if final.Customer.Name == "" || len(final.Actions) != 0 || len(final.Transitions) < 5 {
		t.Fatalf("customer view = %+v", final)
	}
	for _, tr := range final.Transitions {
		if tr.Actor != "Bạn" && tr.Actor != "Cửa hàng" {
			t.Fatalf("a customer must not see who at the store did what: %+v", tr)
		}
	}
	history := decode[struct {
		Items []detailBody `json:"items"`
		Total int          `json:"total"`
	}](t, s.send(s.customer, "GET", "/me/bookings?scope=history", "", http.StatusOK))
	if history.Total != 1 || history.Items[0].ID != b.ID {
		t.Fatalf("history = %+v", history)
	}
	active := decode[struct {
		Total int `json:"total"`
	}](t, s.send(s.customer, "GET", "/me/bookings?scope=active", "", http.StatusOK))
	if active.Total != 0 {
		t.Fatalf("active = %+v", active)
	}
}

// rented takes one jacket to the customer and returns its booking and asset.
func (s *shop) rented() (string, string) {
	s.t.Helper()
	asset := string(s.f.Assets(s.c.JacketM, 1)[0])
	path := "/bookings/" + s.place(s.customer, "k1", "").ID
	s.act(s.money, path+"/payments", pay("p1", "CASH", map[string]int64{"RESERVATION": 15_000}))
	s.act(s.staff, path+"/confirm", "")
	s.act(s.staff, path+"/start-preparation", "")
	s.act(s.money, path+"/payments", pay("p2", "CASH", map[string]int64{"RENTAL": 35_000, "SECURITY": 300_000}))
	s.shop.Set(pgtest.Now.AddDate(0, 0, 10))
	s.act(s.staff, path+"/handover", fmt.Sprintf(`{"checklist_completed":true,"delivered":[{"asset_id":%q,"preexisting_issues":""}]}`, asset))
	return path, asset
}

func TestRescheduleMovesTheBookingOrLeavesItAlone(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 1)
	b := s.place(s.customer, "k1", "")
	path := "/bookings/" + b.ID

	s.send(s.customer, "POST", path+"/reschedule", `{"pickup_date":"2026-11-15","slot_id":"morning"}`, http.StatusBadRequest) // closed Sunday
	s.send(s.customer, "POST", path+"/reschedule", `{"pickup_date":"someday","slot_id":"morning"}`, http.StatusBadRequest)
	type schedule struct {
		PickupAt string `json:"pickup_at"`
	}
	if got := decode[schedule](t, s.send(s.customer, "GET", path, "", http.StatusOK)); got.PickupAt != "2026-11-11T02:00:00Z" {
		t.Fatalf("a refused change moved the booking: %+v", got)
	}
	moved := decode[schedule](t, s.send(s.customer, "POST", path+"/reschedule", `{"pickup_date":"2026-11-12","slot_id":"afternoon"}`, http.StatusOK))
	if moved.PickupAt != "2026-11-12T08:00:00Z" {
		t.Fatalf("moved = %+v", moved)
	}
	staff := decode[detailBody](t, s.send(s.staff, "GET", path, "", http.StatusOK))
	i := slices.IndexFunc(staff.Transitions, func(tr transitionBody) bool { return tr.Action == "booking.reschedule" })
	if i < 0 || staff.Transitions[i].Note != "Lịch cũ: 09:00 11/11/2026 đến 09:00 12/11/2026" {
		t.Fatalf("history = %+v", staff.Transitions)
	}
}

func TestAMissingItemIsVerifiedBeforeItCountsAsLost(t *testing.T) {
	s := newShop(t)
	path, asset := s.rented()
	body := fmt.Sprintf(`{"asset_id":%q}`, asset)
	s.expect(s.do(call{method: "POST", path: "/api/v1" + path + "/report-missing", body: body, cookie: s.customer}), http.StatusForbidden, "identity.forbidden")

	s.shop.Set(pgtest.Now.AddDate(0, 0, 12))
	missing := s.act(s.staff, path+"/report-missing", body)
	if missing.Items[0].State != "MISSING_PENDING" {
		t.Fatalf("missing = %+v", missing.Items)
	}
	lost := fmt.Sprintf(`{"asset_id":%q,"reason":"Khách báo làm mất"}`, asset)
	s.expect(s.do(call{method: "POST", path: "/api/v1" + path + "/confirm-lost", body: lost, cookie: s.a}), http.StatusConflict, "rentals.verification_window_open")

	s.shop.Set(pgtest.Now.AddDate(0, 0, 15))
	if got := s.act(s.a, path+"/confirm-lost", lost); got.Items[0].State != "LOST" {
		t.Fatalf("lost = %+v", got.Items)
	}
}

func TestFeesAreProposedDecidedAndSettled(t *testing.T) {
	s := newShop(t)
	path, asset := s.rented()
	s.shop.Set(pgtest.Now.AddDate(0, 0, 11))
	s.act(s.staff, path+"/returns", fmt.Sprintf(`{"asset_ids":[%q]}`, asset))
	s.send(s.staff, "POST", path+"/inspections", fmt.Sprintf(`{"asset_id":%q,"conditions":["ABNORMAL_SOILING"],"notes":"","evidence":[]}`, asset), http.StatusBadRequest)
	s.act(s.staff, path+"/inspections", fmt.Sprintf(`{"asset_id":%q,"conditions":["ABNORMAL_SOILING"],"notes":"Dính sơn","evidence":["anh-1"]}`, asset))

	type fee struct {
		ID      string   `json:"id"`
		Status  string   `json:"status"`
		Basis   string   `json:"basis"`
		Actions []string `json:"actions"`
	}
	propose := func(amount int64) fee {
		return decode[fee](t, s.send(s.staff, "POST", path+"/fees", fmt.Sprintf(`{"asset_id":%q,"kind":"CLEANING","amount":{"amount":%d,"currency":"VND"},"basis":"Bảng phí vệ sinh","evidence":["anh-1"],"confirmed_new_damage":false}`, asset, amount), http.StatusCreated))
	}
	first := propose(40_000)
	if first.Status != "PROPOSED" {
		t.Fatalf("fee = %+v", first)
	}
	view := decode[struct {
		Fees []fee `json:"fees"`
	}](t, s.send(s.money, "GET", path, "", http.StatusOK))
	if len(view.Fees) != 1 || !slices.Contains(view.Fees[0].Actions, "approve") {
		t.Fatalf("finance sees %+v", view.Fees)
	}
	s.send(s.staff, "POST", path+"/fees/"+first.ID+"/decision", `{"decision":"APPROVE","note":""}`, http.StatusForbidden)
	decided := s.act(s.money, path+"/fees/"+first.ID+"/decision", `{"decision":"APPROVE","note":""}`)
	if decided.Finance.Outstanding.Amount != 40_000 {
		t.Fatalf("after approval = %+v", decided.Finance)
	}

	settled := s.act(s.money, path+"/settle", "")
	if settled.Finance.RefundPending.Amount != 260_000 {
		t.Fatalf("the fee must come out of the deposit: %+v", settled.Finance)
	}
	refund := settled.Finance.Refunds[0]
	s.expect(s.do(call{method: "POST", path: "/api/v1" + path + "/refunds/" + refund.ID + "/approve", cookie: s.customer}), http.StatusForbidden, "")
	s.send(s.money, "POST", path+"/refunds/"+refund.ID+"/reject", `{"note":""}`, http.StatusBadRequest)
	rejected := s.act(s.money, path+"/refunds/"+refund.ID+"/reject", `{"note":"Khách nhận tiền sau"}`)
	if rejected.Finance.Refunds[0].Status != "REJECTED" {
		t.Fatalf("rejected = %+v", rejected.Finance.Refunds)
	}
}

func TestABundleAndItsPartsDrawOnTheSameStock(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 1)
	s.f.Assets(s.c.PantsM, 1)
	s.f.Assets(s.c.BeltStd, 1)
	bundle := fmt.Sprintf(`{"package_id":%q,"pickup_date":"2026-11-11","slot_id":"morning","outbound":"STORE","return":"STORE",
		"lines":[{"kind":"BUNDLE","offering_id":%q,"selections":{"jacket":%q,"pants":%q,"belt":%q},"quantity":1}],"request_key":"k1"}`,
		s.c.OneDay.ID, s.c.Set.ID, s.c.JacketM, s.c.PantsM, s.c.BeltStd)
	b := decode[detailBody](t, s.send(s.customer, "POST", "/bookings", bundle, http.StatusCreated))
	if len(b.Items) != 3 || !strings.Contains(b.Lines[0].Selection, "Áo khoác: M") {
		t.Fatalf("bundle = %+v", b)
	}
	other := s.signIn("other")
	s.expect(s.do(call{method: "POST", path: "/api/v1/bookings", body: s.order(s.c.JacketM, "2026-11-11", "k2", ""), cookie: other}), http.StatusConflict, "inventory.insufficient")
}

func TestPlacingABookingAcceptsOnlyWhatACustomerMaySay(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 1)
	for _, extra := range []string{`,"customer_id":"someone-else"`, `,"rent_total":{"amount":1,"currency":"VND"}`, `,"status":"CONFIRMED"`} {
		s.send(s.customer, "POST", "/bookings", s.order(s.c.JacketM, "2026-11-11", "k1", extra), http.StatusBadRequest)
	}
	s.send(s.customer, "POST", "/bookings", s.order(s.c.JacketM, "2026-11-11", "", ""), http.StatusBadRequest)
	courier := strings.Replace(s.order(s.c.JacketM, "2026-11-11", "k1", ""), `"outbound":"STORE"`, `"outbound":"COURIER"`, 1)
	if w := s.do(call{method: "POST", path: "/api/v1/bookings", body: courier, cookie: s.customer}); w.Code < 400 || w.Code >= 500 {
		t.Fatalf("courier booking = %d: %s", w.Code, w.Body)
	}
}

func TestBookingsStayWithTheirOwner(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 1)
	b := s.place(s.customer, "k1", "")
	path := "/api/v1/bookings/" + b.ID

	stranger := s.signIn("stranger")
	s.expect(s.do(call{method: "GET", path: path, cookie: stranger}), http.StatusForbidden, "identity.forbidden")
	s.expect(s.do(call{method: "GET", path: path + "/cancellation-preview", cookie: stranger}), http.StatusForbidden, "identity.forbidden")
	s.expect(s.do(call{method: "POST", path: path + "/cancel", body: `{"reason":"CUSTOMER_REQUEST","note":""}`, cookie: stranger}), http.StatusForbidden, "identity.forbidden")

	mine := decode[struct {
		Total int `json:"total"`
	}](t, s.send(stranger, "GET", "/me/bookings", "", http.StatusOK))
	if mine.Total != 0 {
		t.Fatalf("stranger sees %d bookings", mine.Total)
	}
	// A customer cannot speak for the store on their own booking.
	s.expect(s.do(call{method: "POST", path: path + "/confirm", cookie: s.customer}), http.StatusForbidden, "")
	s.expect(s.do(call{method: "POST", path: path + "/payments", body: pay("x", "CASH", map[string]int64{"RESERVATION": 15_000}), cookie: s.customer}), http.StatusForbidden, "")
	// Stock staff run the shelves, not the bookings.
	stock := s.signIn("stock", identity.RoleInventoryManager)
	s.expect(s.do(call{method: "GET", path: path, cookie: stock}), http.StatusForbidden, "identity.forbidden")
}

func TestEndpointsRequireTheRightRole(t *testing.T) {
	s := newShop(t)
	staffOnly := []string{"/bookings", "/ops/overview", "/ops/alerts", "/ops/schedule?from=2026-11-01&to=2026-11-07", "/inventory/assets", "/support-requests"}
	adminOnly := []string{
		"/admin/dashboard", "/admin/users", "/admin/audit-events", "/admin/catalog/items", "/admin/catalog/bundles", "/admin/policies/active", "/admin/promotions",
	}
	signedIn := []string{"/me/bookings", "/me/profile", "/me/support-requests"}
	for _, path := range slices.Concat(staffOnly, adminOnly, signedIn) {
		s.expect(s.do(call{method: "GET", path: "/api/v1" + path}), http.StatusUnauthorized, "")
	}
	for _, path := range slices.Concat(staffOnly, adminOnly) {
		s.expect(s.do(call{method: "GET", path: "/api/v1" + path, cookie: s.customer}), http.StatusForbidden, "")
	}
	for _, path := range adminOnly {
		s.expect(s.do(call{method: "GET", path: "/api/v1" + path, cookie: s.staff}), http.StatusForbidden, "")
	}
	for _, path := range slices.Concat(staffOnly, adminOnly) {
		s.send(s.a, "GET", path, "", http.StatusOK)
	}
	s.expect(s.do(call{method: "POST", path: "/api/v1/admin/catalog/categories", body: `{"name":"X","slug":"x"}`, cookie: s.staff}), http.StatusForbidden, "")
}

func TestCancellationPreviewMatchesTheCancellation(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 1)
	b := s.place(s.customer, "k1", "")
	path := "/bookings/" + b.ID
	s.act(s.money, path+"/payments", pay("p1", "CASH", map[string]int64{"RESERVATION": 15_000}))

	// Ten days before pickup the first tier is free; four days before it is 10%.
	s.shop.Set(pgtest.Now.AddDate(0, 0, 6))
	preview := decode[struct {
		FeePercent int       `json:"fee_percent"`
		Fee        moneyBody `json:"fee"`
		Refund     moneyBody `json:"refund"`
		Owed       moneyBody `json:"owed"`
		Basis      string    `json:"basis"`
	}](t, s.send(s.customer, "GET", path+"/cancellation-preview", "", http.StatusOK))
	if preview.FeePercent != 10 || preview.Fee.Amount != 5_000 || preview.Refund.Amount != 10_000 || preview.Owed.Amount != 0 || preview.Basis == "" {
		t.Fatalf("preview = %+v", preview)
	}
	if again := decode[detailBody](t, s.send(s.customer, "GET", path, "", http.StatusOK)); again.Status != "PENDING_CONFIRMATION" {
		t.Fatalf("a preview must change nothing: %s", again.Status)
	}
	// A customer always sees their own terms, whatever they ask for.
	asStore := decode[struct {
		Fee moneyBody `json:"fee"`
	}](t, s.send(s.customer, "GET", path+"/cancellation-preview?reason=STAFF_DECISION", "", http.StatusOK))
	if asStore.Fee.Amount != preview.Fee.Amount {
		t.Fatalf("customer preview as the store = %+v", asStore)
	}
	free := decode[struct {
		Fee moneyBody `json:"fee"`
	}](t, s.send(s.staff, "GET", path+"/cancellation-preview?reason=STAFF_DECISION", "", http.StatusOK))
	if free.Fee.Amount != 0 {
		t.Fatalf("store cancellation preview = %+v", free)
	}

	got := s.act(s.customer, path+"/cancel", `{"reason":"CUSTOMER_REQUEST","note":"Đổi kế hoạch"}`)
	if got.Status != "CANCELLED" || got.Finance.RefundPending.Amount != preview.Refund.Amount {
		t.Fatalf("cancelled = %+v, preview = %+v", got.Finance, preview)
	}
}

func TestCouponsAreLimitedAndComeBackWhenABookingIsCancelled(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 2)
	code := strings.ToUpper(s.f.Prefix) + "GIAM10"
	w := s.send(s.a, "POST", "/admin/promotions", fmt.Sprintf(`{"code":%q,"name":"Giảm 10%%","type":"COUPON","status":"ACTIVE","value_type":"PERCENT","percent":10,"amount":null,
		"valid_from":"2026-11-01","valid_until":"2026-11-30","combinable":false,"usage_limit":1}`, code), http.StatusCreated)
	campaign := decode[struct {
		ID         string `json:"id"`
		ValidUntil string `json:"valid_until"`
	}](t, w)
	if !strings.HasPrefix(campaign.ValidUntil, "2026-11-30T") {
		t.Fatalf("the last valid day must read back as entered: %s", campaign.ValidUntil)
	}

	coupon := fmt.Sprintf(`,"coupon_code":%q`, code)
	q := strings.Replace(s.order(s.c.JacketM, "2026-11-11", "", coupon), `"request_key":"",`, "", 1)
	quote := decode[struct {
		Discount moneyBody `json:"discount_total"`
		Rent     moneyBody `json:"rent_total"`
	}](t, s.send(nil, "POST", "/quotes", q, http.StatusOK))
	if quote.Discount.Amount != 5_000 || quote.Rent.Amount != 45_000 {
		t.Fatalf("quote = %+v", quote)
	}

	b := s.place(s.customer, "k1", coupon)
	other := s.signIn("other")
	w = s.do(call{method: "POST", path: "/api/v1/bookings", body: s.order(s.c.JacketM, "2026-11-11", "k2", coupon), cookie: other})
	if w.Code < 400 || w.Code >= 500 {
		t.Fatalf("a used-up coupon must be refused: %d %s", w.Code, w.Body)
	}
	used := decode[[]struct {
		Code string `json:"code"`
		Used int    `json:"used"`
	}](t, s.send(s.a, "GET", "/admin/promotions", "", http.StatusOK))
	for _, c := range used {
		if c.Code == code && c.Used != 1 {
			t.Fatalf("used = %d", c.Used)
		}
	}

	s.act(s.customer, "/bookings/"+b.ID+"/cancel", `{"reason":"CUSTOMER_REQUEST","note":""}`)
	s.send(other, "POST", "/bookings", s.order(s.c.JacketM, "2026-11-11", "k3", coupon), http.StatusCreated)
}

func TestPaymentGatewayIsReportedAsUnavailable(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 1)
	b := s.place(s.customer, "k1", "")
	s.expect(s.do(call{method: "POST", path: "/api/v1/bookings/" + b.ID + "/payment-sessions", body: `{"purpose":"RESERVATION","channel":"BANK_QR","idempotency_key":"s1"}`, cookie: s.customer}),
		http.StatusConflict, "payments.gateway_unavailable")
	view := decode[map[string]any](t, s.send(s.customer, "GET", "/bookings/"+b.ID, "", http.StatusOK))
	if options, ok := view["payment_options"].([]any); !ok || len(options) != 0 {
		t.Fatalf("payment_options = %#v", view["payment_options"])
	}
}

func TestProfileBelongsToTheSignedInPerson(t *testing.T) {
	s := newShop(t)
	saved := decode[map[string]string](t, s.send(s.customer, "PUT", "/me/profile", `{"full_name":"Nguyễn Thử","phone":"0901234567"}`, http.StatusOK))
	if saved["full_name"] != "Nguyễn Thử" || saved["phone"] != "0901234567" || saved["email"] == "" {
		t.Fatalf("saved = %+v", saved)
	}
	if got := decode[map[string]string](t, s.send(s.customer, "GET", "/me/profile", "", http.StatusOK)); got["phone"] != "0901234567" {
		t.Fatalf("profile = %+v", got)
	}
	s.send(s.customer, "PUT", "/me/profile", `{"full_name":"X","phone":"abc"}`, http.StatusBadRequest)
	s.send(s.customer, "PUT", "/me/profile", `{"full_name":"X","phone":"0901234567","email":"other@example.test"}`, http.StatusBadRequest)
}

func TestSupportRequestsStayBetweenACustomerAndTheStore(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 1)
	b := s.place(s.customer, "k1", "")
	type request struct {
		ID       string `json:"id"`
		Code     string `json:"code"`
		Status   string `json:"status"`
		Messages []struct {
			From, Author, Body string
		}
	}
	body := func(code string) string {
		return fmt.Sprintf(`{"topic":"BOOKING","subject":"Đổi size","booking_code":%q,"message":"Mình muốn đổi size"}`, code)
	}
	r := decode[request](t, s.send(s.customer, "POST", "/me/support-requests", body(b.Code), http.StatusCreated))
	if r.Status != "OPEN" || !strings.HasPrefix(r.Code, "HT") || len(r.Messages) != 1 {
		t.Fatalf("request = %+v", r)
	}

	stranger := s.signIn("stranger")
	s.expect(s.do(call{method: "POST", path: "/api/v1/me/support-requests", body: body(b.Code), cookie: stranger}), http.StatusBadRequest, "")
	if mine := decode[[]request](t, s.send(stranger, "GET", "/me/support-requests", "", http.StatusOK)); len(mine) != 0 {
		t.Fatalf("stranger sees %+v", mine)
	}
	s.expect(s.do(call{method: "POST", path: "/api/v1/support-requests/" + r.ID + "/replies", body: `{"message":"x","resolve":false}`, cookie: s.customer}), http.StatusForbidden, "")

	queue := decode[[]request](t, s.send(s.staff, "GET", "/support-requests", "", http.StatusOK))
	if !slices.ContainsFunc(queue, func(x request) bool { return x.ID == r.ID }) {
		t.Fatalf("queue = %+v", queue)
	}
	replied := decode[request](t, s.send(s.staff, "POST", "/support-requests/"+r.ID+"/replies", `{"message":"Đã đổi giúp bạn","resolve":true}`, http.StatusOK))
	if replied.Status != "RESOLVED" || len(replied.Messages) != 2 || replied.Messages[1].From != "STAFF" {
		t.Fatalf("replied = %+v", replied)
	}
	s.expect(s.do(call{method: "POST", path: "/api/v1/support-requests/" + r.ID + "/replies", body: `{"message":"Thêm","resolve":false}`, cookie: s.staff}), http.StatusConflict, "support.resolved")
}

func TestStaffSeeTheirDayAndTheShelves(t *testing.T) {
	s := newShop(t)
	stock := s.signIn("stock", identity.RoleInventoryManager)
	assets := s.f.Assets(s.c.JacketM, 1)
	b := s.place(s.customer, "k1", "")
	s.act(s.money, "/bookings/"+b.ID+"/payments", pay("p1", "CASH", map[string]int64{"RESERVATION": 15_000}))
	s.act(s.staff, "/bookings/"+b.ID+"/confirm", "")

	s.shop.Set(pgtest.Now.AddDate(0, 0, 10)) // pickup day
	overview := decode[struct {
		Date     string
		Counters map[string]int
		Queue    []struct{ ID string }
	}](t, s.send(s.staff, "GET", "/ops/overview", "", http.StatusOK))
	if overview.Date != "2026-11-11" || overview.Counters["handovers_today"] < 1 {
		t.Fatalf("overview = %+v", overview)
	}
	scheduled := func() string {
		rows := decode[[]struct {
			Kind    string
			Booking struct{ ID string }
		}](t, s.send(s.staff, "GET", "/ops/schedule?from=2026-11-11&to=2026-11-12", "", http.StatusOK))
		for _, e := range rows {
			if e.Booking.ID == b.ID {
				return e.Kind
			}
		}
		return ""
	}
	if got := scheduled(); got != "HANDOVER" {
		t.Fatalf("before handover the schedule shows %q", got)
	}
	s.act(s.money, "/bookings/"+b.ID+"/payments", pay("p2", "CASH", map[string]int64{"RENTAL": 35_000, "SECURITY": 300_000}))
	s.act(s.staff, "/bookings/"+b.ID+"/start-preparation", "")
	s.act(s.staff, "/bookings/"+b.ID+"/handover", fmt.Sprintf(`{"checklist_completed":true,"delivered":[{"asset_id":%q,"preexisting_issues":""}]}`, assets[0]))
	if got := scheduled(); got != "RETURN" {
		t.Fatalf("after handover the schedule shows %q", got)
	}

	type asset struct {
		ID, Code, Status string
		Actions          []string
	}
	page := decode[struct {
		Items []asset
		Total int
	}](t, s.send(stock, "GET", "/inventory/assets?q="+string(assets[0]), "", http.StatusOK))
	if page.Total != 1 || page.Items[0].Status != "RENTED" {
		t.Fatalf("assets = %+v", page)
	}
	s.send(s.staff, "POST", "/inventory/assets/"+page.Items[0].ID+"/status", `{"action":"asset.retire","reason":"Rách","checklist_completed":false}`, http.StatusForbidden)

	created := decode[asset](t, s.send(stock, "POST", "/inventory/assets", fmt.Sprintf(`{"code":%q,"variant_id":%q}`, s.f.Prefix+"-NEW", s.c.JacketL), http.StatusCreated))
	if created.Status != "NEEDS_INSPECTION" {
		t.Fatalf("a new asset must be checked first: %+v", created)
	}
	passed := decode[asset](t, s.send(stock, "POST", "/inventory/assets/"+created.ID+"/status", `{"action":"asset.pass_inspection","reason":"","checklist_completed":true}`, http.StatusOK))
	if passed.Status != "READY" {
		t.Fatalf("passed = %+v", passed)
	}
	history := decode[[]struct {
		Action string
		To     string
		From   *string
	}](t, s.send(stock, "GET", "/inventory/assets/"+created.ID+"/history", "", http.StatusOK))
	if len(history) != 2 || history[0].From != nil && history[1].From != nil {
		t.Fatalf("history = %+v", history)
	}
}

func TestAdminManagesTheCatalogAndPublishesPolicy(t *testing.T) {
	s := newShop(t)
	category := decode[struct {
		ID, Slug string
	}](t, s.send(s.a, "POST", "/admin/catalog/categories", fmt.Sprintf(`{"name":"Anime","slug":%q}`, s.f.Prefix+"-anime"), http.StatusCreated))
	pack := decode[struct {
		ID            string
		DurationHours int `json:"duration_hours"`
	}](t, s.send(s.a, "POST", "/admin/catalog/packages", fmt.Sprintf(`{"code":%q,"label":"2 ngày","duration_hours":48,"status":"ACTIVE"}`, s.f.Prefix+"-2D"), http.StatusCreated))
	if pack.DurationHours != 48 {
		t.Fatalf("package = %+v", pack)
	}

	item := fmt.Sprintf(`{"slug":%q,"name":"Áo Gojo","category_id":%q,"description":"Mô tả","images":[{"url":"https://cdn.example.test/gojo.jpg","alt":"Áo Gojo"}],
		"status":"ACTIVE","individually_rentable":true,"variants":[{"id":"","sku":%q,"size":"M","color":"Đen","status":"ACTIVE"}],
		"prices":[{"package_id":%q,"amount":{"amount":90000,"currency":"VND"}}],"security_deposit":{"amount":200000,"currency":"VND"},
		"daily_rate":{"amount":45000,"currency":"VND"},"buffer_class":"garment"}`, s.f.Prefix+"-gojo", category.ID, s.f.Prefix+"-GOJO-M", pack.ID)
	saved := decode[struct {
		ID       string
		Variants []struct{ ID string }
	}](t, s.send(s.a, "POST", "/admin/catalog/items", item, http.StatusCreated))
	edited := strings.Replace(item, "Áo Gojo\",\"category_id", "Áo Gojo mới\",\"category_id", 1)
	edited = strings.Replace(edited, `"variants":[{"id":""`, fmt.Sprintf(`"variants":[{"id":%q`, saved.Variants[0].ID), 1)
	s.send(s.a, "PUT", "/admin/catalog/items/"+saved.ID, edited, http.StatusOK)
	public := decode[struct{ Name string }](t, s.send(nil, "GET", "/catalog/offerings/ITEM/"+s.f.Prefix+"-gojo", "", http.StatusOK))
	if public.Name != "Áo Gojo mới" {
		t.Fatalf("public item = %+v", public)
	}
	s.send(s.a, "POST", "/admin/catalog/items", strings.Replace(item, "https://cdn.example.test/gojo.jpg", "javascript:alert(1)", 1), http.StatusBadRequest)

	active := decode[map[string]any](t, s.send(s.a, "GET", "/admin/policies/active", "", http.StatusOK))
	active["version"] = s.f.Prefix + "-v2"
	active["booking"].(map[string]any)["hold_minutes"] = 45
	body, _ := json.Marshal(map[string]any{"policy": active, "reason": "Nới thời gian giữ hàng"})
	published := decode[struct {
		Version string
		Booking struct {
			HoldMinutes int `json:"hold_minutes"`
		}
	}](t, s.send(s.a, "POST", "/admin/policies", string(body), http.StatusCreated))
	if published.Booking.HoldMinutes != 45 {
		t.Fatalf("published = %+v", published)
	}
	// The next test must find the fixture policy again.
	t.Cleanup(func() {
		if err := pgtest.SeedConfig(context.Background(), s.f.Pool); err != nil {
			t.Fatal(err)
		}
	})
	if got := decode[struct {
		HoldMinutes int `json:"hold_minutes"`
	}](t, s.send(nil, "GET", "/policies/public", "", http.StatusOK)); got.HoldMinutes != 45 {
		t.Fatalf("public policy = %+v", got)
	}

	audit := decode[struct {
		Items []struct{ Action, Actor string }
		Total int
	}](t, s.send(s.a, "GET", "/admin/audit-events?q=policy.published&page_size=100", "", http.StatusOK))
	if audit.Total < 1 {
		t.Fatalf("audit = %+v", audit)
	}

	users := decode[struct {
		Items []struct {
			Email string
			Roles []string
		}
		Total int
	}](t, s.send(s.a, "GET", "/admin/users?role=rental_staff&page_size=100", "", http.StatusOK))
	if users.Total < 1 {
		t.Fatalf("users = %+v", users)
	}
	dashboard := decode[struct {
		RangeDays int `json:"range_days"`
		KPIs      []struct{ Key string }
		Series    []any `json:"revenue_series"`
	}](t, s.send(s.a, "GET", "/admin/dashboard?range_days=7", "", http.StatusOK))
	if dashboard.RangeDays != 7 || len(dashboard.KPIs) == 0 || len(dashboard.Series) != 7 {
		t.Fatalf("dashboard = %+v", dashboard)
	}
}

func TestListsReportBadPaging(t *testing.T) {
	s := newShop(t)
	s.send(s.staff, "GET", "/bookings?page=0", "", http.StatusBadRequest)
	s.send(s.staff, "GET", "/bookings?page_size=101", "", http.StatusBadRequest)
	s.send(nil, "GET", "/catalog/offerings?kind=NOPE", "", http.StatusBadRequest)
	s.send(s.customer, "GET", "/me/bookings?scope=everything", "", http.StatusBadRequest)
}
