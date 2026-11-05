package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/httpapi"
	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/platform/ratelimit"
)

// newCrowd is a shop whose rate limits leave room for many people signing in
// from one address.
func newCrowd(t *testing.T) *shop {
	t.Helper()
	limits := httpapi.DefaultRateLimits()
	plenty := ratelimit.Rate{N: 1000, Window: time.Minute}
	limits.RegisterPerIP, limits.VerifyConfirmPerIP, limits.LoginPerIP = plenty, plenty, plenty
	return shopWith(t, options{limits: limits})
}

func (s *shop) people(n int) []*http.Cookie {
	s.t.Helper()
	out := make([]*http.Cookie, n)
	for i := range out {
		out[i] = s.signIn(fmt.Sprintf("person-%d", i))
	}
	return out
}

// steps turns calls into steps that store their answers in out.
func (a *api) steps(out []*httptest.ResponseRecorder, calls []call) []func() {
	steps := make([]func(), len(calls))
	for i, c := range calls {
		steps[i] = func() { out[i] = a.do(c) }
	}
	return steps
}

// race sends every call at the same moment and returns the answers in order.
func (a *api) race(calls ...call) []*httptest.ResponseRecorder {
	a.t.Helper()
	out := make([]*httptest.ResponseRecorder, len(calls))
	a.f.Race(a.steps(out, calls)...)
	return out
}

// raceForStock is race with the variant's shelf held until every call waits
// on it, so each of them has passed its checks before the first one commits.
func (a *api) raceForStock(variant catalog.VariantID, calls ...call) []*httptest.ResponseRecorder {
	a.t.Helper()
	out := make([]*httptest.ResponseRecorder, len(calls))
	a.f.RaceBehind(`SELECT 1 FROM assets WHERE variant_id = $1 FOR UPDATE`, []any{variant}, a.steps(out, calls)...)
	return out
}

// tally counts the answers by status. Losing a race must be a clean refusal,
// so a server error fails the test whatever else it checks.
func (a *api) tally(answers []*httptest.ResponseRecorder) map[int]int {
	a.t.Helper()
	out := map[int]int{}
	for _, w := range answers {
		out[w.Code]++
		if w.Code >= 500 {
			a.t.Errorf("server error: %d %s", w.Code, w.Body)
		}
	}
	return out
}

func (a *api) wantTally(answers []*httptest.ResponseRecorder, want map[int]int) {
	a.t.Helper()
	got := a.tally(answers)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		for _, w := range answers {
			a.t.Log(w.Code, w.Body)
		}
		a.t.Fatalf("answers = %v, want %v", got, want)
	}
}

// count runs a SELECT count(*) and returns the number.
func (a *api) count(query string, args ...any) int {
	a.t.Helper()
	var n int
	if err := a.f.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		a.t.Fatal(err)
	}
	return n
}

// noAssetHeldTwice reads the database directly: whatever the API answered, no
// physical asset may be held by two active allocations at the same time.
func (a *api) noAssetHeldTwice() {
	a.t.Helper()
	if n := a.count(`SELECT count(*) FROM allocations x JOIN allocations y
		ON x.asset_id = y.asset_id AND x.id < y.id AND x.block && y.block
		WHERE x.status = 'ACTIVE' AND y.status = 'ACTIVE'`); n != 0 {
		a.t.Fatalf("%d pairs of allocations hold one asset at the same time", n)
	}
}

func (s *shop) heldOn(variant catalog.VariantID) int {
	s.t.Helper()
	return s.count(`SELECT count(*) FROM allocations WHERE variant_id = $1 AND status = 'ACTIVE'`, variant)
}

func (s *shop) bookingCall(who *http.Cookie, body string) call {
	return call{method: "POST", path: "/api/v1/bookings", body: body, cookie: who}
}

func TestTheLastItemGoesToExactlyOneCustomer(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 1)
	crowd := s.people(12)
	for round, date := range []string{"2026-11-11", "2026-11-17", "2026-11-24"} {
		calls := make([]call, len(crowd))
		for i, who := range crowd {
			calls[i] = s.bookingCall(who, s.order(s.c.JacketM, date, fmt.Sprintf("r%d-%d", round, i), ""))
		}
		answers := s.raceForStock(s.c.JacketM, calls...)
		s.wantTally(answers, map[int]int{http.StatusCreated: 1, http.StatusConflict: len(crowd) - 1})
		for _, w := range answers {
			if w.Code == http.StatusConflict {
				s.expect(w, http.StatusConflict, "inventory.insufficient")
			}
		}
		if got := s.heldOn(s.c.JacketM); got != round+1 {
			t.Fatalf("after round %d the variant holds %d allocations", round, got)
		}
	}
	s.noAssetHeldTwice()
}

func TestTwoLeftMeansTwoBookings(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 2)
	crowd := s.people(10)
	calls := make([]call, len(crowd))
	for i, who := range crowd {
		calls[i] = s.bookingCall(who, s.order(s.c.JacketM, "2026-11-11", fmt.Sprintf("k%d", i), ""))
	}
	s.wantTally(s.raceForStock(s.c.JacketM, calls...), map[int]int{http.StatusCreated: 2, http.StatusConflict: 8})
	if got := s.heldOn(s.c.JacketM); got != 2 {
		t.Fatalf("held = %d", got)
	}
	s.noAssetHeldTwice()
}

// A double click on the last item: the second request must not be told the
// item is gone by the booking the first one just made.
func TestADoubleClickOnTheLastItemIsOneBooking(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 1)
	calls := make([]call, 6)
	for i := range calls {
		calls[i] = s.bookingCall(s.customer, s.order(s.c.JacketM, "2026-11-11", "same-click", ""))
	}
	answers := s.raceForStock(s.c.JacketM, calls...)
	s.wantTally(answers, map[int]int{http.StatusCreated: 6})
	first := decode[detailBody](t, answers[0])
	for _, w := range answers {
		if got := decode[detailBody](t, w); got.ID != first.ID {
			t.Fatalf("two bookings from one click: %s, %s", got.ID, first.ID)
		}
	}
	if got := s.heldOn(s.c.JacketM); got != 1 {
		t.Fatalf("held = %d", got)
	}
}

func TestADoubleClickWithStockToSpareIsStillOneBooking(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 5)
	calls := make([]call, 6)
	for i := range calls {
		calls[i] = s.bookingCall(s.customer, s.order(s.c.JacketM, "2026-11-11", "same-click", ""))
	}
	s.wantTally(s.raceForStock(s.c.JacketM, calls...), map[int]int{http.StatusCreated: 6})
	if got := s.heldOn(s.c.JacketM); got != 1 {
		t.Fatalf("one click held %d assets", got)
	}
}

// A bundle needs every part or nothing, while single items compete for the
// same shelf: one pair of pants means one winner, and the losers hold nothing.
func TestABundleAndSingleItemsRaceForOneShelf(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 3)
	s.f.Assets(s.c.PantsM, 1)
	s.f.Assets(s.c.BeltStd, 3)
	crowd := s.people(8)
	bundle := func(key string) string {
		return fmt.Sprintf(`{"package_id":%q,"pickup_date":"2026-11-11","slot_id":"morning","outbound":"STORE","return":"STORE",
			"lines":[{"kind":"BUNDLE","offering_id":%q,"selections":{"jacket":%q,"pants":%q,"belt":%q},"quantity":1}],"request_key":%q}`,
			s.c.OneDay.ID, s.c.Set.ID, s.c.JacketM, s.c.PantsM, s.c.BeltStd, key)
	}
	pants := func(key string) string {
		return fmt.Sprintf(`{"package_id":%q,"pickup_date":"2026-11-11","slot_id":"morning","outbound":"STORE","return":"STORE",
			"lines":[{"kind":"ITEM","offering_id":%q,"variant_id":%q,"quantity":1}],"request_key":%q}`,
			s.c.OneDay.ID, s.c.Pants.ID, s.c.PantsM, key)
	}
	calls := make([]call, len(crowd))
	for i, who := range crowd {
		if i%2 == 0 {
			calls[i] = s.bookingCall(who, bundle(fmt.Sprint("k", i)))
		} else {
			calls[i] = s.bookingCall(who, pants(fmt.Sprint("k", i)))
		}
	}
	s.wantTally(s.race(calls...), map[int]int{http.StatusCreated: 1, http.StatusConflict: 7})
	jackets, belts := s.heldOn(s.c.JacketM), s.heldOn(s.c.BeltStd)
	if s.heldOn(s.c.PantsM) != 1 || jackets != belts || jackets > 1 {
		t.Fatalf("partly allocated: jackets %d, belts %d", jackets, belts)
	}
	s.noAssetHeldTwice()
}

// The turnaround buffer belongs to the block: the next day overlaps, a week
// later does not, and the race changes neither.
func TestOverlappingDatesRaceButSeparateDatesBothWin(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 1)
	crowd := s.people(3)
	answers := s.race(
		s.bookingCall(crowd[0], s.order(s.c.JacketM, "2026-11-11", "a", "")),
		s.bookingCall(crowd[1], s.order(s.c.JacketM, "2026-11-12", "b", "")),
		s.bookingCall(crowd[2], s.order(s.c.JacketM, "2026-11-24", "c", "")),
	)
	s.wantTally(answers, map[int]int{http.StatusCreated: 2, http.StatusConflict: 1})
	if answers[2].Code != http.StatusCreated {
		t.Fatalf("a date nobody else wants was refused: %s", answers[2].Body)
	}
	s.noAssetHeldTwice()
}

func TestTwoBookingsReschedulingIntoOneFreeDate(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 1)
	crowd := s.people(2)
	type schedule struct {
		PickupAt string `json:"pickup_at"`
	}
	first := decode[detailBody](t, s.send(crowd[0], "POST", "/bookings", s.order(s.c.JacketM, "2026-11-11", "a", ""), http.StatusCreated))
	second := decode[detailBody](t, s.send(crowd[1], "POST", "/bookings", s.order(s.c.JacketM, "2026-11-17", "b", ""), http.StatusCreated))
	before := []schedule{
		decode[schedule](t, s.send(crowd[0], "GET", "/bookings/"+first.ID, "", http.StatusOK)),
		decode[schedule](t, s.send(crowd[1], "GET", "/bookings/"+second.ID, "", http.StatusOK)),
	}
	move := `{"pickup_date":"2026-11-24","slot_id":"morning"}`
	answers := s.race(
		call{method: "POST", path: "/api/v1/bookings/" + first.ID + "/reschedule", body: move, cookie: crowd[0]},
		call{method: "POST", path: "/api/v1/bookings/" + second.ID + "/reschedule", body: move, cookie: crowd[1]},
	)
	s.wantTally(answers, map[int]int{http.StatusOK: 1, http.StatusConflict: 1})
	loser, id, who := 1, second.ID, crowd[1]
	if answers[0].Code != http.StatusOK {
		loser, id, who = 0, first.ID, crowd[0]
	}
	if got := decode[schedule](t, s.send(who, "GET", "/bookings/"+id, "", http.StatusOK)); got != before[loser] {
		t.Fatalf("the refused booking moved: %+v, was %+v", got, before[loser])
	}
	if got := s.heldOn(s.c.JacketM); got != 2 {
		t.Fatalf("held = %d, want each booking to keep one", got)
	}
	s.noAssetHeldTwice()
}

func TestAMoveAndANewBookingRaceForOneDate(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 1)
	crowd := s.people(2)
	moved := decode[detailBody](t, s.send(crowd[0], "POST", "/bookings", s.order(s.c.JacketM, "2026-11-11", "a", ""), http.StatusCreated))
	answers := s.race(
		call{method: "POST", path: "/api/v1/bookings/" + moved.ID + "/reschedule", body: `{"pickup_date":"2026-11-24","slot_id":"morning"}`, cookie: crowd[0]},
		s.bookingCall(crowd[1], s.order(s.c.JacketM, "2026-11-24", "b", "")),
	)
	if got := s.tally(answers); got[http.StatusConflict] != 1 {
		t.Fatalf("answers = %v, want exactly one refusal", got)
	}
	s.noAssetHeldTwice()
}

// One booking moved twice at once ends on one date, not on both.
func TestTwoMovesOfOneBookingLeaveOneAllocation(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 2)
	b := s.place(s.customer, "k1", "")
	path := "/api/v1/bookings/" + b.ID + "/reschedule"
	answers := s.race(
		call{method: "POST", path: path, body: `{"pickup_date":"2026-11-17","slot_id":"morning"}`, cookie: s.customer},
		call{method: "POST", path: path, body: `{"pickup_date":"2026-11-24","slot_id":"morning"}`, cookie: s.customer},
	)
	s.tally(answers)
	if got := s.heldOn(s.c.JacketM); got != 1 {
		t.Fatalf("held = %d, want one", got)
	}
}

func TestCancellingFreesTheItemWithoutDoubleAllocating(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 1)
	crowd := s.people(5)
	b := s.place(s.customer, "k1", "")
	calls := []call{{method: "POST", path: "/api/v1/bookings/" + b.ID + "/cancel", body: `{"reason":"CUSTOMER_REQUEST","note":""}`, cookie: s.customer}}
	for i, who := range crowd {
		calls = append(calls, s.bookingCall(who, s.order(s.c.JacketM, "2026-11-11", fmt.Sprint("k", i), "")))
	}
	answers := s.race(calls...)
	s.tally(answers)
	if answers[0].Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", answers[0].Code, answers[0].Body)
	}
	if got := s.heldOn(s.c.JacketM); got > 1 {
		t.Fatalf("held = %d", got)
	}
	s.noAssetHeldTwice()
}

func TestTheLastCouponUseGoesToOneCustomer(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 8)
	code := strings.ToUpper(s.f.Prefix) + "LAST"
	s.send(s.a, "POST", "/admin/promotions", fmt.Sprintf(`{"code":%q,"name":"Giảm 10%%","type":"COUPON","status":"ACTIVE","value_type":"PERCENT","percent":10,"amount":null,
		"valid_from":"2026-11-01","valid_until":"2026-11-30","combinable":false,"usage_limit":1}`, code), http.StatusCreated)
	coupon := fmt.Sprintf(`,"coupon_code":%q`, code)
	crowd := s.people(8)
	calls := make([]call, len(crowd))
	for i, who := range crowd {
		calls[i] = s.bookingCall(who, s.order(s.c.JacketM, "2026-11-11", fmt.Sprint("k", i), coupon))
	}
	answers := s.raceForStock(s.c.JacketM, calls...)
	counts := s.tally(answers)
	if counts[http.StatusCreated] != 1 {
		t.Fatalf("answers = %v, want one booking with the coupon", counts)
	}
	if n := s.count(`SELECT count(*) FROM promotion_redemptions r JOIN promotion_campaigns c ON c.id = r.campaign_id
		WHERE c.code = $1 AND r.status = 'ACTIVE'`, code); n != 1 {
		t.Fatalf("coupon redeemed %d times", n)
	}
	if got := s.heldOn(s.c.JacketM); got != 1 {
		t.Fatalf("refused bookings left %d allocations", got)
	}
}

func TestARepeatedPaymentRequestCountsOnce(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 1)
	b := s.place(s.customer, "k1", "")
	body := pay("desk-1", "CASH", map[string]int64{"RESERVATION": 15_000})
	calls := make([]call, 8)
	for i := range calls {
		calls[i] = call{method: "POST", path: "/api/v1/bookings/" + b.ID + "/payments", body: body, cookie: s.money}
	}
	s.wantTally(s.race(calls...), map[int]int{http.StatusOK: 8})
	view := decode[detailBody](t, s.send(s.money, "GET", "/bookings/"+b.ID, "", http.StatusOK))
	if view.Finance.PaidTotal.Amount != 15_000 || len(view.Finance.Payments) != 1 {
		t.Fatalf("finance = %+v", view.Finance)
	}
	if n := s.count(`SELECT count(*) FROM audit_events WHERE action = 'payment.recorded' AND refs->>'booking_id' = $1`, b.ID); n != 1 {
		t.Fatalf("audited %d payments", n)
	}
}

func TestTwoDesksCannotCollectTheSameAmount(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 1)
	b := s.place(s.customer, "k1", "")
	calls := make([]call, 6)
	for i := range calls {
		calls[i] = call{method: "POST", path: "/api/v1/bookings/" + b.ID + "/payments",
			body: pay(fmt.Sprint("desk-", i), "CASH", map[string]int64{"SECURITY": 300_000}), cookie: s.money}
	}
	answers := s.race(calls...)
	s.wantTally(answers, map[int]int{http.StatusOK: 1, http.StatusConflict: 5})
	view := decode[detailBody](t, s.send(s.money, "GET", "/bookings/"+b.ID, "", http.StatusOK))
	if view.Finance.PaidTotal.Amount != 300_000 {
		t.Fatalf("collected %d", view.Finance.PaidTotal.Amount)
	}
}

func TestAPaymentKeyMeansOnePayment(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 2)
	b := s.place(s.customer, "k1", "")
	path := "/bookings/" + b.ID + "/payments"
	first := pay("desk-1", "CASH", map[string]int64{"RESERVATION": 15_000})
	s.act(s.money, path, first)

	// The same key with other terms is a mistake, not a retry.
	s.expect(s.do(call{method: "POST", path: "/api/v1" + path, body: pay("desk-1", "CASH", map[string]int64{"RESERVATION": 10_000}), cookie: s.money}),
		http.StatusConflict, "payments.idempotency_conflict")
	s.expect(s.do(call{method: "POST", path: "/api/v1" + path, body: pay("desk-1", "BANK_TRANSFER", map[string]int64{"RESERVATION": 15_000}), cookie: s.money}),
		http.StatusConflict, "payments.idempotency_conflict")

	// Keys belong to one booking.
	other := s.signIn("other")
	second := decode[detailBody](t, s.send(other, "POST", "/bookings", s.order(s.c.JacketM, "2026-11-11", "k2", ""), http.StatusCreated))
	if got := s.act(s.money, "/bookings/"+second.ID+"/payments", first); got.Finance.PaidTotal.Amount != 15_000 {
		t.Fatalf("second booking paid = %+v", got.Finance)
	}

	// A retry that arrives after the booking ended changes nothing.
	s.shop.Set(pgtest.Now.AddDate(0, 0, 6))
	s.act(s.customer, "/bookings/"+b.ID+"/cancel", `{"reason":"CUSTOMER_REQUEST","note":""}`)
	before := decode[detailBody](t, s.send(s.money, "GET", "/bookings/"+b.ID, "", http.StatusOK))
	after := s.act(s.money, path, first)
	if after.Finance.PaidTotal != before.Finance.PaidTotal || len(after.Finance.Payments) != 1 || len(after.Finance.Refunds) != len(before.Finance.Refunds) {
		t.Fatalf("a late retry changed the money: %+v, was %+v", after.Finance, before.Finance)
	}
	s.expect(s.do(call{method: "POST", path: "/api/v1" + path, body: pay("desk-2", "CASH", map[string]int64{"RESERVATION": 15_000}), cookie: s.money}),
		http.StatusConflict, "payments.exceeds_outstanding")
}

func TestABookingThatEndedTakesNoMoreMoney(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 1)
	b := s.place(s.customer, "k1", "")
	path := "/bookings/" + b.ID
	s.act(s.customer, path+"/cancel", `{"reason":"CUSTOMER_REQUEST","note":""}`)
	for _, bucket := range []string{"RENTAL", "RESERVATION", "SECURITY"} {
		s.expect(s.do(call{method: "POST", path: "/api/v1" + path + "/payments", body: pay("late-"+bucket, "CASH", map[string]int64{bucket: 1_000}), cookie: s.money}),
			http.StatusConflict, "payments.exceeds_outstanding")
	}
}

func TestABadPaymentRecordsNothing(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 1)
	b := s.place(s.customer, "k1", "")
	path := "/api/v1/bookings/" + b.ID + "/payments"
	amount := func(a string) string {
		return `{"idempotency_key":"x","method":"CASH","portions":[{"bucket":"RENTAL","amount":{"amount":` + a + `,"currency":"VND"}}]}`
	}
	for name, body := range map[string]string{
		"zero":          amount("0"),
		"negative":      amount("-1000"),
		"fraction":      amount("10.5"),
		"overflow":      amount("9223372036854775808"),
		"largest":       amount("9223372036854775807"),
		"over the rent": amount("50001"),
		"currency":      `{"idempotency_key":"x","method":"CASH","portions":[{"bucket":"RENTAL","amount":{"amount":1000,"currency":"USD"}}]}`,
		"bucket":        `{"idempotency_key":"x","method":"CASH","portions":[{"bucket":"TIP","amount":{"amount":1000,"currency":"VND"}}]}`,
		"method":        `{"idempotency_key":"x","method":"GATEWAY","portions":[{"bucket":"RENTAL","amount":{"amount":1000,"currency":"VND"}}]}`,
		"no key":        `{"idempotency_key":" ","method":"CASH","portions":[{"bucket":"RENTAL","amount":{"amount":1000,"currency":"VND"}}]}`,
		"no portions":   `{"idempotency_key":"x","method":"CASH","portions":[]}`,
		"same bucket twice": `{"idempotency_key":"x","method":"CASH","portions":[{"bucket":"RENTAL","amount":{"amount":1,"currency":"VND"}},
			{"bucket":"RENTAL","amount":{"amount":1,"currency":"VND"}}]}`,
		"a status of its own": `{"idempotency_key":"x","method":"CASH","status":"SUCCEEDED","portions":[{"bucket":"RENTAL","amount":{"amount":1000,"currency":"VND"}}]}`,
	} {
		if w := s.do(call{method: "POST", path: path, body: body, cookie: s.money}); w.Code < 400 || w.Code >= 500 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if n := s.count(`SELECT count(*) FROM payments WHERE booking_id = $1`, b.ID); n != 0 {
		t.Fatalf("%d bad payments were recorded", n)
	}
}

func cancelled(s *shop) (string, string) {
	s.t.Helper()
	s.f.Assets(s.c.JacketM, 1)
	b := s.place(s.customer, "k1", "")
	path := "/bookings/" + b.ID
	s.act(s.money, path+"/payments", pay("p1", "CASH", map[string]int64{"RESERVATION": 15_000}))
	got := s.act(s.customer, path+"/cancel", `{"reason":"CUSTOMER_REQUEST","note":""}`)
	if len(got.Finance.Refunds) != 1 {
		s.t.Fatalf("refunds = %+v", got.Finance.Refunds)
	}
	return path, got.Finance.Refunds[0].ID
}

const counterPayout = `{"method":"CASH","reference":"PT-1","paid_at":"2026-11-01T01:00:00Z"}`

func TestARefundIsPaidOutOnce(t *testing.T) {
	s := newCrowd(t)
	path, refund := cancelled(s)
	calls := make([]call, 6)
	for i := range calls {
		calls[i] = call{method: "POST", path: "/api/v1" + path + "/refunds/" + refund + "/payout", body: counterPayout, cookie: s.money}
	}
	s.wantTally(s.race(calls...), map[int]int{http.StatusOK: 1, http.StatusConflict: 5})
	view := decode[detailBody](t, s.send(s.money, "GET", path, "", http.StatusOK))
	if view.Finance.RefundedTotal.Amount != 15_000 || view.Finance.RefundPending.Amount != 0 {
		t.Fatalf("finance = %+v", view.Finance)
	}
	if n := s.count(`SELECT count(*) FROM audit_events WHERE action = 'refund.completed' AND resource_id = $1`, refund); n != 1 {
		t.Fatalf("audited %d payouts", n)
	}
}

func TestPayingOutAndRejectingARefundCannotBothHappen(t *testing.T) {
	s := newCrowd(t)
	path, refund := cancelled(s)
	answers := s.race(
		call{method: "POST", path: "/api/v1" + path + "/refunds/" + refund + "/payout", body: counterPayout, cookie: s.money},
		call{method: "POST", path: "/api/v1" + path + "/refunds/" + refund + "/reject", body: `{"note":"Sai đơn"}`, cookie: s.money},
	)
	s.wantTally(answers, map[int]int{http.StatusOK: 1, http.StatusConflict: 1})
	view := decode[detailBody](t, s.send(s.money, "GET", path, "", http.StatusOK))
	wantRefunded := int64(0)
	if answers[0].Code == http.StatusOK {
		wantRefunded = 15_000
	}
	if view.Finance.RefundedTotal.Amount != wantRefunded {
		t.Fatalf("refunded %d, want %d", view.Finance.RefundedTotal.Amount, wantRefunded)
	}
}

func TestCancellingTwiceAtOnceSettlesOnce(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 1)
	b := s.place(s.customer, "k1", "")
	path := "/bookings/" + b.ID
	s.act(s.money, path+"/payments", pay("p1", "CASH", map[string]int64{"RESERVATION": 15_000}))
	s.shop.Set(pgtest.Now.AddDate(0, 0, 6)) // 10% of the rent stays with the shop
	calls := make([]call, 6)
	for i := range calls {
		calls[i] = call{method: "POST", path: "/api/v1" + path + "/cancel", body: `{"reason":"CUSTOMER_REQUEST","note":""}`, cookie: s.customer}
	}
	s.wantTally(s.race(calls...), map[int]int{http.StatusOK: 1, http.StatusConflict: 5})
	view := decode[detailBody](t, s.send(s.money, "GET", path, "", http.StatusOK))
	if len(view.Finance.Refunds) != 1 || view.Finance.RefundPending.Amount != 10_000 {
		t.Fatalf("finance = %+v", view.Finance)
	}
	fees := 0
	for _, c := range view.Finance.Charges {
		if c.Kind == "CANCELLATION_FEE" {
			fees++
		}
	}
	if fees != 1 {
		t.Fatalf("%d cancellation fees", fees)
	}
	if got := s.heldOn(s.c.JacketM); got != 0 {
		t.Fatalf("held = %d", got)
	}
}

// Whichever goes first, every dong taken is either kept as the fee or on its
// way back, and a payment after the cancellation is refused.
func TestAPaymentRacingACancellationLeavesNoMoneyBehind(t *testing.T) {
	s := newCrowd(t)
	s.f.Assets(s.c.JacketM, 3)
	s.shop.Set(pgtest.Now.AddDate(0, 0, 1))
	for i, date := range []string{"2026-11-17", "2026-11-24", "2026-12-01"} {
		b := decode[detailBody](t, s.send(s.customer, "POST", "/bookings", s.order(s.c.JacketM, date, fmt.Sprint("k", i), ""), http.StatusCreated))
		path := "/api/v1/bookings/" + b.ID
		s.tally(s.race(
			call{method: "POST", path: path + "/payments", body: pay("p", "CASH", map[string]int64{"RESERVATION": 15_000}), cookie: s.money},
			call{method: "POST", path: path + "/cancel", body: `{"reason":"CUSTOMER_REQUEST","note":""}`, cookie: s.customer},
		))
		view := decode[detailBody](t, s.send(s.money, "GET", "/bookings/"+b.ID, "", http.StatusOK))
		var fee int64
		if len(view.Finance.Payments) > 0 {
			fee = view.Finance.PaidTotal.Amount - view.Finance.RefundPending.Amount
		}
		if view.Status != "CANCELLED" || fee < 0 || fee > 15_000 {
			t.Fatalf("after the race: %s, %+v", view.Status, view.Finance)
		}
	}
}

func TestSettlingTwiceAtOnceRefundsTheDepositOnce(t *testing.T) {
	s := newCrowd(t)
	path, asset := s.rented()
	s.shop.Set(pgtest.Now.AddDate(0, 0, 11))
	s.act(s.staff, path+"/returns", fmt.Sprintf(`{"asset_ids":[%q]}`, asset))
	s.act(s.staff, path+"/inspections", fmt.Sprintf(`{"asset_id":%q,"conditions":["NORMAL_WEAR"],"notes":"","evidence":[]}`, asset))

	calls := make([]call, 6)
	for i := range calls {
		calls[i] = call{method: "POST", path: "/api/v1" + path + "/settle", cookie: s.money}
	}
	s.tally(s.race(calls...))
	view := decode[detailBody](t, s.send(s.money, "GET", path, "", http.StatusOK))
	if len(view.Finance.Refunds) != 1 || view.Finance.RefundPending.Amount != 300_000 {
		t.Fatalf("finance = %+v", view.Finance)
	}

	s.act(s.money, path+"/refunds/"+view.Finance.Refunds[0].ID+"/payout", counterPayout)
	again := s.act(s.money, path+"/settle", "")
	if len(again.Finance.Refunds) != 1 || again.Finance.RefundedTotal.Amount != 300_000 {
		t.Fatalf("settling after the payout: %+v", again.Finance)
	}
}

func TestAFeeApprovedTwiceIsChargedOnce(t *testing.T) {
	s := newCrowd(t)
	path, asset := s.rented()
	s.shop.Set(pgtest.Now.AddDate(0, 0, 11))
	s.act(s.staff, path+"/returns", fmt.Sprintf(`{"asset_ids":[%q]}`, asset))
	s.act(s.staff, path+"/inspections", fmt.Sprintf(`{"asset_id":%q,"conditions":["ABNORMAL_SOILING"],"notes":"Dính sơn","evidence":["anh-1"]}`, asset))
	fee := decode[struct {
		ID string `json:"id"`
	}](t, s.send(s.staff, "POST", path+"/fees", fmt.Sprintf(`{"asset_id":%q,"kind":"CLEANING","amount":{"amount":40000,"currency":"VND"},"basis":"Bảng phí","evidence":["anh-1"],"confirmed_new_damage":false}`, asset), http.StatusCreated))

	calls := make([]call, 6)
	for i := range calls {
		calls[i] = call{method: "POST", path: "/api/v1" + path + "/fees/" + fee.ID + "/decision", body: `{"decision":"APPROVE","note":""}`, cookie: s.money}
	}
	s.wantTally(s.race(calls...), map[int]int{http.StatusOK: 1, http.StatusConflict: 5})
	view := decode[detailBody](t, s.send(s.money, "GET", path, "", http.StatusOK))
	charged := 0
	for _, c := range view.Finance.Charges {
		if c.Kind == "CLEANING_FEE" {
			charged++
		}
	}
	if charged != 1 || view.Finance.Outstanding.Amount != 40_000 {
		t.Fatalf("charged %d times: %+v", charged, view.Finance)
	}
}

func TestAHandoverRepeatedAtOnceHappensOnce(t *testing.T) {
	s := newCrowd(t)
	assets := s.f.Assets(s.c.JacketM, 1)
	b := s.place(s.customer, "k1", "")
	path := "/bookings/" + b.ID
	s.act(s.money, path+"/payments", pay("p1", "CASH", map[string]int64{"RESERVATION": 15_000}))
	s.act(s.staff, path+"/confirm", "")
	s.act(s.staff, path+"/start-preparation", "")
	s.act(s.money, path+"/payments", pay("p2", "CASH", map[string]int64{"RENTAL": 35_000, "SECURITY": 300_000}))
	s.shop.Set(pgtest.Now.AddDate(0, 0, 10))
	body := fmt.Sprintf(`{"checklist_completed":true,"delivered":[{"asset_id":%q,"preexisting_issues":""}]}`, assets[0])
	calls := make([]call, 5)
	for i := range calls {
		calls[i] = call{method: "POST", path: "/api/v1" + path + "/handover", body: body, cookie: s.staff}
	}
	s.wantTally(s.race(calls...), map[int]int{http.StatusOK: 1, http.StatusConflict: 4})
	if n := s.count(`SELECT count(*) FROM reservation_resolutions WHERE booking_id = $1`, b.ID); n != 1 {
		t.Fatalf("the reservation was applied %d times", n)
	}
	if n := s.count(`SELECT count(*) FROM asset_status_changes WHERE asset_id = $1 AND to_status = 'RENTED'`, assets[0]); n != 1 {
		t.Fatalf("the asset went out %d times", n)
	}
}

func TestAReturnRepeatedAtOnceChargesOneLateFee(t *testing.T) {
	s := newCrowd(t)
	path, asset := s.rented()
	s.shop.Set(pgtest.Now.AddDate(0, 0, 11).Add(5 * time.Hour))
	calls := make([]call, 5)
	for i := range calls {
		calls[i] = call{method: "POST", path: "/api/v1" + path + "/returns", body: fmt.Sprintf(`{"asset_ids":[%q]}`, asset), cookie: s.staff}
	}
	s.wantTally(s.race(calls...), map[int]int{http.StatusOK: 1, http.StatusConflict: 4})
	view := decode[detailBody](t, s.send(s.money, "GET", path, "", http.StatusOK))
	late := 0
	for _, c := range view.Finance.Charges {
		if c.Kind == "LATE_FEE" {
			late++
		}
	}
	if late != 1 {
		t.Fatalf("%d late fees: %+v", late, view.Finance.Charges)
	}
}

func TestCompletingTwiceAtOnceHappensOnce(t *testing.T) {
	s := newCrowd(t)
	path, asset := s.rented()
	s.shop.Set(pgtest.Now.AddDate(0, 0, 11))
	s.act(s.staff, path+"/returns", fmt.Sprintf(`{"asset_ids":[%q]}`, asset))
	s.act(s.staff, path+"/inspections", fmt.Sprintf(`{"asset_id":%q,"conditions":["NORMAL_WEAR"],"notes":"","evidence":[]}`, asset))
	settled := s.act(s.money, path+"/settle", "")
	s.act(s.money, path+"/refunds/"+settled.Finance.Refunds[0].ID+"/payout", counterPayout)
	calls := make([]call, 4)
	for i := range calls {
		calls[i] = call{method: "POST", path: "/api/v1" + path + "/complete", cookie: s.staff}
	}
	s.wantTally(s.race(calls...), map[int]int{http.StatusOK: 1, http.StatusConflict: 3})
}
