package httpapi_test

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/postgres/pgtest"
)

// Who calls in the sweep below. "owner" is the customer of the bookings under
// test and "stranger" another customer.
const (
	guest    = "guest"
	owner    = "owner"
	stranger = "stranger"
	desk     = "staff"
	stock    = "stock"
	finance  = "finance"
	admin    = "admin"
)

var everyPerson = []string{owner, stranger, desk, stock, finance, admin}

// access is what one route allows. Routes that would really change something
// for an allowed caller set denyOnly: the sweep then only proves the others
// are refused. The rest run against a cancelled booking, where an allowed
// caller gets past authorization and is stopped by the state.
type access struct {
	allow    []string
	path     string
	body     string
	denyOnly bool
}

// The anonymous part of the API. Everything else must appear in routes() below.
var publicRoutes = []string{
	"GET /healthz", "GET /readyz",
	"POST /api/v1/auth/register", "POST /api/v1/auth/verify-email", "POST /api/v1/auth/verify-email/resend",
	"POST /api/v1/auth/login", "POST /api/v1/auth/login/mfa/enroll", "POST /api/v1/auth/login/mfa", "POST /api/v1/auth/logout",
	"POST /api/v1/auth/password-reset", "POST /api/v1/auth/password-reset/confirm",
	"GET /api/v1/catalog/categories", "GET /api/v1/catalog/packages", "GET /api/v1/catalog/offerings", "GET /api/v1/catalog/offerings/{kind}/{slug}",
	"GET /api/v1/store/info", "GET /api/v1/store/calendar", "GET /api/v1/store/fulfillment-options", "GET /api/v1/policies/public", "GET /api/v1/media/{name}",
	"POST /api/v1/availability", "POST /api/v1/quotes",
}

// registeredRoutes reads the route table from the source, so a new endpoint
// cannot ship without saying who may call it.
func registeredRoutes(t *testing.T) []string {
	t.Helper()
	var out []string
	route := regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+ /[^"]+)"`)
	for _, file := range []string{"server.go", "services.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range route.FindAllSubmatch(src, -1) {
			out = append(out, string(m[1]))
		}
	}
	return out
}

// sweepTarget is the state the sweep runs on.
type sweepTarget struct {
	booking, refundlessBooking, feeBooking, fee, asset, stranger, support string
}

func routes(s *shop, x sweepTarget) map[string]access {
	id := "/api/v1/bookings/" + x.booking
	money := `{"amount":40000,"currency":"VND"}`
	front := []string{desk, admin}           // the counter
	office := []string{desk, finance, admin} // booking.read_all, support.handle
	shelves := []string{desk, stock, admin}  // inventory.read
	buyers := []string{owner, desk, admin}   // own booking, or any booking
	approvers := []string{finance, admin}    // refund.approve, fee.approve
	catalogs := []string{stock, admin}       // catalog.manage
	item := fmt.Sprintf(`{"slug":"x","name":"X","category_ids":[],"description":"","images":[],"status":"ACTIVE","individually_rentable":true,"variants":[],
		"prices":[],"security_deposit":%s,"daily_rate":%s,"buffer_class":"garment"}`, money, money)
	bundle := `{"slug":"x","name":"X","category_ids":[],"description":"","images":[],"status":"ACTIVE","components":[],"prices":[],"deposit_override":null}`
	category := `{"name":"X","slug":"x"}`
	pack := `{"code":"X","label":"X","duration_hours":24,"status":"ACTIVE"}`
	promotion := `{"code":"X","name":"X","type":"COUPON","status":"ACTIVE","value_type":"PERCENT","percent":10,"amount":null,"valid_from":"2026-11-01","valid_until":"2026-11-30","combinable":false,"usage_limit":1}`
	user := "/api/v1/admin/users/" + x.stranger
	reason := `{"reason":"thử"}`
	return map[string]access{
		"GET /api/v1/auth/me":                            {allow: everyPerson},
		"POST /api/v1/auth/password":                     {allow: everyPerson, denyOnly: true, body: `{}`},
		"POST /api/v1/auth/mfa/setup":                    {allow: everyPerson, denyOnly: true},
		"POST /api/v1/auth/mfa/setup/confirm":            {allow: everyPerson, denyOnly: true, body: `{}`},
		"POST /api/v1/auth/mfa/disable":                  {allow: everyPerson, denyOnly: true, body: `{}`},
		"GET /api/v1/me/bookings":                        {allow: everyPerson},
		"GET /api/v1/me/profile":                         {allow: everyPerson},
		"PUT /api/v1/me/profile":                         {allow: everyPerson, denyOnly: true, body: `{}`},
		"GET /api/v1/me/support-requests":                {allow: everyPerson},
		"POST /api/v1/me/support-requests":               {allow: everyPerson, denyOnly: true, body: `{}`},
		"POST /api/v1/bookings":                          {allow: everyPerson, denyOnly: true, body: `{}`},
		"GET /api/v1/bookings":                           {allow: office},
		"GET /api/v1/bookings/{id}":                      {allow: []string{owner, desk, finance, admin}, path: id},
		"GET /api/v1/bookings/{id}/cancellation-preview": {allow: buyers, path: id + "/cancellation-preview"},
		"POST /api/v1/bookings/{id}/cancel":              {allow: buyers, path: id + "/cancel", body: `{"reason":"CUSTOMER_REQUEST","note":"x"}`},
		"POST /api/v1/bookings/{id}/reschedule":          {allow: buyers, path: id + "/reschedule", body: `{"pickup_date":"2026-11-24","slot_id":"morning"}`},
		"POST /api/v1/bookings/{id}/confirm":             {allow: front, path: id + "/confirm"},
		"POST /api/v1/bookings/{id}/start-preparation":   {allow: front, path: id + "/start-preparation"},
		"POST /api/v1/bookings/{id}/handover": {allow: front, path: id + "/handover",
			body: fmt.Sprintf(`{"checklist_completed":true,"delivered":[{"asset_id":%q,"preexisting_issues":""}]}`, x.asset)},
		"POST /api/v1/bookings/{id}/returns":        {allow: front, path: id + "/returns", body: fmt.Sprintf(`{"asset_ids":[%q]}`, x.asset)},
		"POST /api/v1/bookings/{id}/report-missing": {allow: front, path: id + "/report-missing", body: fmt.Sprintf(`{"asset_id":%q}`, x.asset)},
		"POST /api/v1/bookings/{id}/confirm-lost":   {allow: []string{admin}, path: id + "/confirm-lost", body: fmt.Sprintf(`{"asset_id":%q,"reason":"mất"}`, x.asset)},
		"POST /api/v1/bookings/{id}/inspections": {allow: shelves, path: id + "/inspections",
			body: fmt.Sprintf(`{"asset_id":%q,"conditions":["NORMAL_WEAR"],"notes":"","evidence":[]}`, x.asset)},
		"POST /api/v1/bookings/{id}/fees": {allow: shelves, path: id + "/fees",
			body: fmt.Sprintf(`{"asset_id":%q,"kind":"CLEANING","amount":%s,"basis":"x","evidence":["a"],"confirmed_new_damage":false}`, x.asset, money)},
		"POST /api/v1/bookings/{id}/fees/{fee}/decision": {allow: approvers, path: "/api/v1/bookings/" + x.feeBooking + "/fees/" + x.fee + "/decision",
			body: `{"decision":"APPROVE","note":""}`},
		"POST /api/v1/bookings/{id}/payments":                 {allow: office, path: id + "/payments", body: pay("sweep", "CASH", map[string]int64{"RESERVATION": 15_000})},
		"POST /api/v1/bookings/{id}/refunds/{refund}/approve": {allow: approvers, path: id + "/refunds/no-such-refund/approve"},
		"POST /api/v1/bookings/{id}/refunds/{refund}/reject":  {allow: approvers, path: id + "/refunds/no-such-refund/reject", body: `{"note":"x"}`},
		"POST /api/v1/bookings/{id}/refunds/{refund}/payout":  {allow: office, path: id + "/refunds/no-such-refund/payout", body: counterPayout},
		"POST /api/v1/bookings/{id}/settle":                   {allow: office, path: id + "/settle"},
		"POST /api/v1/bookings/{id}/complete":                 {allow: front, path: id + "/complete"},
		// Hosted checkout answers 409 to anyone signed in until a provider is
		// connected; it reveals nothing and takes no money.
		"POST /api/v1/bookings/{id}/payment-sessions": {allow: everyPerson, path: id + "/payment-sessions", body: `{"purpose":"RESERVATION","channel":"BANK_QR","idempotency_key":"s"}`},
		"GET /api/v1/payment-sessions/{id}":           {allow: everyPerson, path: "/api/v1/payment-sessions/x"},

		"GET /api/v1/ops/overview":                  {allow: office},
		"GET /api/v1/ops/schedule":                  {allow: office, path: "/api/v1/ops/schedule?from=2026-11-01&to=2026-11-07"},
		"GET /api/v1/ops/alerts":                    {allow: []string{desk, stock, finance, admin}},
		"GET /api/v1/inventory/assets":              {allow: shelves},
		"POST /api/v1/inventory/assets":             {allow: catalogs, denyOnly: true, body: `{"code":"X","variant_id":"x"}`},
		"GET /api/v1/inventory/assets/{id}/history": {allow: shelves, path: "/api/v1/inventory/assets/" + x.asset + "/history"},
		"POST /api/v1/inventory/assets/{id}/status": {allow: catalogs, denyOnly: true, path: "/api/v1/inventory/assets/" + x.asset + "/status",
			body: `{"action":"asset.retire","reason":"x","checklist_completed":false}`},
		"GET /api/v1/support-requests":               {allow: office},
		"POST /api/v1/support-requests/{id}/replies": {allow: office, denyOnly: true, path: "/api/v1/support-requests/" + x.support + "/replies", body: `{"message":"x","resolve":false}`},

		"GET /api/v1/admin/dashboard":                  {allow: approvers},
		"GET /api/v1/admin/users":                      {allow: []string{admin}},
		"GET /api/v1/admin/users/{id}":                 {allow: []string{admin}, path: user},
		"PUT /api/v1/admin/users/{id}/roles/{role}":    {allow: []string{admin}, denyOnly: true, path: user + "/roles/admin", body: reason},
		"DELETE /api/v1/admin/users/{id}/roles/{role}": {allow: []string{admin}, denyOnly: true, path: user + "/roles/customer", body: reason},
		"POST /api/v1/admin/users/{id}/lock":           {allow: []string{admin}, denyOnly: true, path: user + "/lock", body: reason},
		"POST /api/v1/admin/users/{id}/unlock":         {allow: []string{admin}, denyOnly: true, path: user + "/unlock", body: reason},
		"POST /api/v1/admin/users/{id}/mfa-reset":      {allow: []string{admin}, denyOnly: true, path: user + "/mfa-reset", body: reason},
		"POST /api/v1/admin/users/{id}/profile-erase":  {allow: []string{admin}, denyOnly: true, path: user + "/profile-erase", body: reason},
		"GET /api/v1/admin/profile-retention":          {allow: []string{admin}},
		"GET /api/v1/admin/permission-matrix":          {allow: []string{admin}},
		"POST /api/v1/admin/permission-matrix":         {allow: []string{admin}, denyOnly: true, body: `{"version":"x","matrix":{},"reason":"x"}`},
		"GET /api/v1/admin/audit-events":               {allow: approvers},
		"GET /api/v1/admin/catalog/items":              {allow: catalogs},
		"POST /api/v1/admin/catalog/items":             {allow: catalogs, denyOnly: true, body: item},
		"PUT /api/v1/admin/catalog/items/{id}":         {allow: catalogs, denyOnly: true, path: "/api/v1/admin/catalog/items/x", body: item},
		"GET /api/v1/admin/catalog/bundles":            {allow: catalogs},
		"POST /api/v1/admin/catalog/bundles":           {allow: catalogs, denyOnly: true, body: bundle},
		"PUT /api/v1/admin/catalog/bundles/{id}":       {allow: catalogs, denyOnly: true, path: "/api/v1/admin/catalog/bundles/x", body: bundle},
		"POST /api/v1/admin/catalog/categories":        {allow: catalogs, denyOnly: true, body: category},
		"PUT /api/v1/admin/catalog/categories/{id}":    {allow: catalogs, denyOnly: true, path: "/api/v1/admin/catalog/categories/x", body: category},
		"POST /api/v1/admin/catalog/packages":          {allow: catalogs, denyOnly: true, body: pack},
		"PUT /api/v1/admin/catalog/packages/{id}":      {allow: catalogs, denyOnly: true, path: "/api/v1/admin/catalog/packages/x", body: pack},
		"POST /api/v1/admin/media/images":              {allow: catalogs, denyOnly: true, body: "not an image"},
		"GET /api/v1/admin/policies/active":            {allow: []string{admin}},
		"POST /api/v1/admin/policies":                  {allow: []string{admin}, denyOnly: true, body: `{"policy":{},"reason":"x"}`},
		"GET /api/v1/admin/tax/profiles":               {allow: approvers},
		"POST /api/v1/admin/tax/profiles": {allow: []string{admin}, denyOnly: true,
			body: `{"version":"x","annual_revenue_threshold":{"amount":1000000000,"currency":"VND"},"threshold_effective_from":"2026-01-01","warning_percents":[70],"effective_from":"2027-01-01"}`},
		"GET /api/v1/admin/revenue/year":                  {allow: approvers},
		"GET /api/v1/admin/revenue/report":                {allow: approvers, path: "/api/v1/admin/revenue/report?from=2026-11-01&to=2026-11-30"},
		"GET /api/v1/admin/revenue/entries":               {allow: approvers, path: "/api/v1/admin/revenue/entries?from=2026-11-01&to=2026-11-30"},
		"GET /api/v1/admin/revenue/entries.csv":           {allow: approvers, path: "/api/v1/admin/revenue/entries.csv?from=2026-11-01&to=2026-11-30"},
		"POST /api/v1/admin/revenue/entries":              {allow: approvers, denyOnly: true, body: `{"category":"RENTAL_REVENUE","amount":{"amount":1000,"currency":"VND"},"date":"2026-11-01","channel":"x"}`},
		"POST /api/v1/admin/revenue/entries/{id}/reverse": {allow: approvers, path: "/api/v1/admin/revenue/entries/no-such-entry/reverse", body: reason},
		"GET /api/v1/admin/finance/cash-flow":             {allow: approvers, path: "/api/v1/admin/finance/cash-flow?from=2026-11-01&to=2026-11-30"},
		"GET /api/v1/admin/finance/reconciliation":        {allow: approvers, path: "/api/v1/admin/finance/reconciliation?from=2026-11-01&to=2026-11-30"},
		"GET /api/v1/admin/finance/transactions":          {allow: approvers, path: "/api/v1/admin/finance/transactions?from=2026-11-01&to=2026-11-30"},
		"POST /api/v1/admin/finance/transactions/{kind}/{id}/reconcile": {allow: approvers, path: "/api/v1/admin/finance/transactions/payment/no-such-payment/reconcile",
			body: `{"statement_ref":"x","note":""}`},
		"POST /api/v1/admin/finance/reconciliations/{id}/revoke": {allow: approvers, path: "/api/v1/admin/finance/reconciliations/no-such-mark/revoke", body: reason},
		"GET /api/v1/admin/promotions":                           {allow: []string{admin}},
		"POST /api/v1/admin/promotions":                          {allow: []string{admin}, denyOnly: true, body: promotion},
		"PUT /api/v1/admin/promotions/{id}":                      {allow: []string{admin}, denyOnly: true, path: "/api/v1/admin/promotions/x", body: promotion},
	}
}

// ledgerFacts is everything the sweep must leave as it found it.
func (a *api) ledgerFacts() string {
	a.t.Helper()
	facts := make([]string, 0, 12)
	for _, table := range []string{"bookings", "booking_transitions", "payments", "refunds", "ledger_charges", "fee_assessments",
		"allocations", "assets", "asset_status_changes", "user_roles", "promotion_campaigns", "catalog_items"} {
		facts = append(facts, fmt.Sprintf("%s=%d", table, a.count(`SELECT count(*) FROM `+table)))
	}
	facts = append(facts, fmt.Sprintf("active=%d", a.count(`SELECT count(*) FROM allocations WHERE status = 'ACTIVE'`)),
		fmt.Sprintf("reviewed=%d", a.count(`SELECT count(*) FROM fee_assessments WHERE status = 'APPROVED'`)))
	return strings.Join(facts, " ")
}

// TestEveryEndpointAnswersOnlyTheRightPeople calls every route as every kind
// of person and compares with the permission matrix. A refused call must be
// 401 or 403, never a validation error that hides whether the check ran, and
// the whole sweep must change nothing.
func TestEveryEndpointAnswersOnlyTheRightPeople(t *testing.T) {
	s := newCrowd(t)
	_, strangerID := s.account("sweep-stranger")
	strangerEmail := s.f.Prefix + "-sweep-stranger@example.test"
	strangerCookie, _ := s.login(strangerEmail)
	cookies := map[string]*http.Cookie{
		guest: nil, owner: s.customer, stranger: strangerCookie, desk: s.staff, finance: s.money, admin: s.a,
		stock: s.signIn("stock", identity.RoleInventoryManager),
	}

	// A fee already rejected: approvers reach the state check, others stop at
	// the permission.
	feePath, feeAsset := s.rented()
	s.shop.Set(pgtest.Now.AddDate(0, 0, 11))
	s.act(s.staff, feePath+"/returns", fmt.Sprintf(`{"asset_ids":[%q]}`, feeAsset))
	s.act(s.staff, feePath+"/inspections", fmt.Sprintf(`{"asset_id":%q,"conditions":["ABNORMAL_SOILING"],"notes":"Dính sơn","evidence":["a"]}`, feeAsset))
	fee := decode[struct {
		ID string `json:"id"`
	}](t, s.send(s.staff, "POST", feePath+"/fees", fmt.Sprintf(`{"asset_id":%q,"kind":"CLEANING","amount":{"amount":40000,"currency":"VND"},"basis":"x","evidence":["a"],"confirmed_new_damage":false}`, feeAsset), http.StatusCreated))
	s.act(s.money, feePath+"/fees/"+fee.ID+"/decision", `{"decision":"REJECT","note":"Không đủ căn cứ"}`)

	// The cancelled booking every other booking route runs on.
	asset := string(s.f.Assets(s.c.JacketM, 1)[0])
	target := decode[detailBody](t, s.send(s.customer, "POST", "/bookings", s.order(s.c.JacketM, "2026-11-17", "sweep", ""), http.StatusCreated))
	s.act(s.customer, "/bookings/"+target.ID+"/cancel", `{"reason":"CUSTOMER_REQUEST","note":""}`)

	ticket := decode[struct {
		ID string `json:"id"`
	}](t, s.send(s.customer, "POST", "/me/support-requests", `{"topic":"OTHER","subject":"Hỏi","booking_code":"","message":"Hỏi"}`, http.StatusCreated))

	table := routes(s, sweepTarget{booking: target.ID, feeBooking: strings.TrimPrefix(feePath, "/bookings/"), fee: fee.ID, asset: asset, stranger: string(strangerID), support: ticket.ID})

	var missing []string
	for _, r := range registeredRoutes(t) {
		_, listed := table[r]
		if !listed && !slices.Contains(publicRoutes, r) {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("routes without a rule in this test:\n%s", strings.Join(missing, "\n"))
	}
	for r := range table {
		if !slices.Contains(registeredRoutes(t), r) {
			t.Errorf("rule for a route that does not exist: %s", r)
		}
	}

	before := s.ledgerFacts()
	for _, r := range slices.Sorted(maps.Keys(table)) {
		rule := table[r]
		method, path, _ := strings.Cut(r, " ")
		if rule.path != "" {
			path = rule.path
		}
		for _, person := range append([]string{guest}, everyPerson...) {
			allowed := slices.Contains(rule.allow, person)
			if allowed && rule.denyOnly {
				continue
			}
			w := s.do(call{method: method, path: path, body: rule.body, cookie: cookies[person]})
			switch {
			case person == guest:
				if w.Code != http.StatusUnauthorized {
					t.Errorf("%s as a guest: %d %s", r, w.Code, w.Body)
				}
			case allowed:
				if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden || w.Code >= 500 {
					t.Errorf("%s as %s: %d %s", r, person, w.Code, w.Body)
				}
			default:
				if w.Code != http.StatusForbidden {
					t.Errorf("%s as %s: %d, want 403: %s", r, person, w.Code, w.Body)
				}
			}
		}
	}
	if after := s.ledgerFacts(); after != before {
		t.Fatalf("the sweep changed the shop:\n%s\n%s", before, after)
	}
}

// Ids in a path must belong together: a refund or fee of one booking is not
// reachable through another, even for those allowed to act on both.
func TestRecordsOfOneBookingCannotBeReachedThroughAnother(t *testing.T) {
	s := newCrowd(t)
	path, asset := s.rented()
	s.shop.Set(pgtest.Now.AddDate(0, 0, 11))
	s.act(s.staff, path+"/returns", fmt.Sprintf(`{"asset_ids":[%q]}`, asset))
	s.act(s.staff, path+"/inspections", fmt.Sprintf(`{"asset_id":%q,"conditions":["ABNORMAL_SOILING"],"notes":"Dính sơn","evidence":["a"]}`, asset))
	fee := decode[struct {
		ID string `json:"id"`
	}](t, s.send(s.staff, "POST", path+"/fees", fmt.Sprintf(`{"asset_id":%q,"kind":"CLEANING","amount":{"amount":40000,"currency":"VND"},"basis":"x","evidence":["a"],"confirmed_new_damage":false}`, asset), http.StatusCreated))
	s.act(s.money, path+"/fees/"+fee.ID+"/decision", `{"decision":"APPROVE","note":""}`)
	settled := s.act(s.money, path+"/settle", "")
	refund := settled.Finance.Refunds[0].ID

	spare := string(s.f.Assets(s.c.JacketM, 1)[0])
	other := "/bookings/" + decode[detailBody](t, s.send(s.customer, "POST", "/bookings", s.order(s.c.JacketM, "2026-11-17", "k2", ""), http.StatusCreated)).ID

	for name, c := range map[string]call{
		"refund approved through another booking": {method: "POST", path: "/api/v1" + other + "/refunds/" + refund + "/approve", cookie: s.money},
		"refund rejected through another booking": {method: "POST", path: "/api/v1" + other + "/refunds/" + refund + "/reject", body: `{"note":"x"}`, cookie: s.money},
		"refund paid out through another booking": {method: "POST", path: "/api/v1" + other + "/refunds/" + refund + "/payout", body: counterPayout, cookie: s.money},
		"fee decided through another booking":     {method: "POST", path: "/api/v1" + other + "/fees/" + fee.ID + "/decision", body: `{"decision":"WAIVE","note":"x"}`, cookie: s.money},
	} {
		if w := s.do(c); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	for name, c := range map[string]call{
		"returning an asset of another booking": {method: "POST", path: "/api/v1" + other + "/returns", body: fmt.Sprintf(`{"asset_ids":[%q]}`, asset), cookie: s.staff},
		"inspecting an asset of another booking": {method: "POST", path: "/api/v1" + other + "/inspections",
			body: fmt.Sprintf(`{"asset_id":%q,"conditions":["NORMAL_WEAR"],"notes":"","evidence":[]}`, asset), cookie: s.staff},
		"charging for an asset of another booking": {method: "POST", path: "/api/v1" + other + "/fees",
			body: fmt.Sprintf(`{"asset_id":%q,"kind":"CLEANING","amount":{"amount":1000,"currency":"VND"},"basis":"x","evidence":["a"],"confirmed_new_damage":false}`, spare), cookie: s.staff},
		"handing over an asset of another booking": {method: "POST", path: "/api/v1" + other + "/handover",
			body: fmt.Sprintf(`{"checklist_completed":true,"delivered":[{"asset_id":%q,"preexisting_issues":""}]}`, asset), cookie: s.staff},
	} {
		if w := s.do(c); w.Code < 400 || w.Code >= 500 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	view := decode[detailBody](t, s.send(s.money, "GET", path, "", http.StatusOK))
	if view.Finance.Refunds[0].Status != "REQUESTED" {
		t.Fatalf("the refund moved: %+v", view.Finance.Refunds)
	}
	if got := decode[detailBody](t, s.send(s.staff, "GET", other, "", http.StatusOK)); got.Status != "PENDING_CONFIRMATION" {
		t.Fatalf("the other booking moved: %s", got.Status)
	}
}

// Only the store's reasons make a cancellation free, and a customer cannot
// claim them.
func TestACustomerCannotChooseACheaperCancellation(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 1)
	b := s.place(s.customer, "k1", "")
	path := "/bookings/" + b.ID
	s.act(s.money, path+"/payments", pay("p1", "CASH", map[string]int64{"RESERVATION": 15_000}))
	s.shop.Set(pgtest.Now.AddDate(0, 0, 9))
	for _, reason := range []string{"STAFF_DECISION", "PAYMENT_FAILED", "NO_SHOW", ""} {
		s.send(s.customer, "POST", path+"/cancel", fmt.Sprintf(`{"reason":%q,"note":"x"}`, reason), http.StatusBadRequest)
	}
	if got := decode[detailBody](t, s.send(s.customer, "GET", path, "", http.StatusOK)); got.Status != "PENDING_CONFIRMATION" {
		t.Fatalf("status = %s", got.Status)
	}
	got := s.act(s.customer, path+"/cancel", `{"reason":"CUSTOMER_REQUEST","note":""}`)
	if got.Finance.RefundPending.Amount != 0 {
		t.Fatalf("the 50%% tier must keep the deposit: %+v", got.Finance)
	}
}

func TestLargeFeesNeedTheHighApproval(t *testing.T) {
	s := newShop(t)
	path, asset := s.rented()
	s.shop.Set(pgtest.Now.AddDate(0, 0, 11))
	s.act(s.staff, path+"/returns", fmt.Sprintf(`{"asset_ids":[%q]}`, asset))
	s.act(s.staff, path+"/inspections", fmt.Sprintf(`{"asset_id":%q,"conditions":["IRREPARABLE"],"notes":"Rách","evidence":["a"]}`, asset))
	fee := decode[struct {
		ID string `json:"id"`
	}](t, s.send(s.staff, "POST", path+"/fees", fmt.Sprintf(`{"asset_id":%q,"kind":"DAMAGE_REPLACEMENT","amount":{"amount":600000,"currency":"VND"},"basis":"Giá mua lại","evidence":["a"],"confirmed_new_damage":true}`, asset), http.StatusCreated))
	decision := "/api/v1" + path + "/fees/" + fee.ID + "/decision"
	s.expect(s.do(call{method: "POST", path: decision, body: `{"decision":"APPROVE","note":""}`, cookie: s.staff}), http.StatusForbidden, "identity.forbidden")
	s.expect(s.do(call{method: "POST", path: decision, body: `{"decision":"APPROVE","note":""}`, cookie: s.money}), http.StatusForbidden, "identity.forbidden")
	s.send(s.a, "POST", path+"/fees/"+fee.ID+"/decision", `{"decision":"APPROVE","note":""}`, http.StatusOK)
}

func TestCrossSiteRequestsCannotMoveMoney(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 1)
	b := s.place(s.customer, "k1", "")
	path := "/api/v1/bookings/" + b.ID
	forged := map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}
	for _, c := range []call{
		{method: "POST", path: path + "/payments", body: pay("x", "CASH", map[string]int64{"RESERVATION": 15_000}), cookie: s.money, header: forged},
		{method: "POST", path: path + "/cancel", body: `{"reason":"CUSTOMER_REQUEST","note":""}`, cookie: s.customer, header: forged},
		{method: "POST", path: "/api/v1/bookings", body: s.order(s.c.JacketM, "2026-11-17", "k2", ""), cookie: s.customer, header: forged},
	} {
		s.expect(s.do(c), http.StatusForbidden, "request.cross_origin")
	}
	if n := s.count(`SELECT count(*) FROM payments WHERE booking_id = $1`, b.ID); n != 0 {
		t.Fatalf("%d forged payments", n)
	}
}

// Stock staff work with the shelves: an alert about a late return tells them
// which item and which bookings, not who the customers are.
func TestAlertsHideCustomersFromStockStaff(t *testing.T) {
	s := newCrowd(t)
	s.send(s.customer, "PUT", "/me/profile", `{"full_name":"Nguyễn Thử","phone":"0901234567"}`, http.StatusOK)
	asset := string(s.f.Assets(s.c.JacketM, 1)[0])
	late := "/bookings/" + s.place(s.customer, "k1", "").ID
	s.send(s.signIn("next"), "POST", "/bookings", s.order(s.c.JacketM, "2026-11-17", "k2", ""), http.StatusCreated)
	s.handOver(late, asset)
	s.shop.Set(pgtest.Now.AddDate(0, 0, 15)) // past the block, before the next booking starts

	type alerts struct {
		Overdue   []json.RawMessage `json:"overdue"`
		Conflicts []json.RawMessage `json:"conflicts"`
	}
	full := s.send(s.staff, "GET", "/ops/alerts", "", http.StatusOK)
	if got := decode[alerts](t, full); len(got.Overdue) != 1 || len(got.Conflicts) != 1 || !strings.Contains(full.Body.String(), "0901234567") {
		t.Fatalf("staff alerts = %s", full.Body)
	}
	w := s.send(s.signIn("stock", identity.RoleInventoryManager), "GET", "/ops/alerts", "", http.StatusOK)
	body := w.Body.String()
	if got := decode[alerts](t, w); len(got.Overdue) != 0 || len(got.Conflicts) != 1 {
		t.Fatalf("stock alerts = %s", body)
	}
	for _, private := range []string{"0901234567", "Nguyễn Thử", "@example.test"} {
		if strings.Contains(body, private) {
			t.Fatalf("stock staff see %q: %s", private, body)
		}
	}
}

// Inspection is allowed to stock staff, who cannot read bookings. What they
// did must not come back as a refusal: a retry would then look necessary.
func TestStockStaffGetAStraightAnswerWhenTheyInspect(t *testing.T) {
	s := newShop(t)
	path, asset := s.rented()
	s.shop.Set(pgtest.Now.AddDate(0, 0, 11))
	s.act(s.staff, path+"/returns", fmt.Sprintf(`{"asset_ids":[%q]}`, asset))
	stockPerson := s.signIn("stock", identity.RoleInventoryManager)
	w := s.do(call{method: "POST", path: "/api/v1" + path + "/inspections", cookie: stockPerson,
		body: fmt.Sprintf(`{"asset_id":%q,"conditions":["NORMAL_WEAR"],"notes":"","evidence":[]}`, asset)})
	inspected := s.count(`SELECT count(*) FROM asset_status_changes WHERE asset_id = $1 AND action = 'asset.pass_inspection'`, asset) > 1
	if inspected && w.Code >= 400 {
		t.Fatalf("the inspection was saved but the answer is %d: %s", w.Code, w.Body)
	}
	if !inspected {
		t.Fatalf("the inspection was not saved: %d %s", w.Code, w.Body)
	}
}

// Input a client can send must end in a refusal, not an internal error.
func TestOddInputIsRefusedNotAnInternalError(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 1)
	// Random text does not compress, so it cannot fit in a btree index entry.
	noise := make([]byte, 6000)
	if _, err := rand.Read(noise); err != nil {
		t.Fatal(err)
	}
	long := hex.EncodeToString(noise)
	b := s.place(s.customer, "k1", "")
	id := "/api/v1/bookings/" + b.ID
	calls := map[string]call{
		"long request key":         {method: "POST", path: "/api/v1/bookings", body: s.order(s.c.JacketM, "2026-11-17", long, ""), cookie: s.customer},
		"nul in request key":       {method: "POST", path: "/api/v1/bookings", body: s.order(s.c.JacketM, "2026-11-17", `a\u0000b`, ""), cookie: s.customer},
		"long idempotency key":     {method: "POST", path: id + "/payments", body: pay(long, "CASH", map[string]int64{"RESERVATION": 15_000}), cookie: s.money},
		"nul in idempotency key":   {method: "POST", path: id + "/payments", body: pay(`a\u0000b`, "CASH", map[string]int64{"RESERVATION": 15_000}), cookie: s.money},
		"nul in a note":            {method: "POST", path: id + "/cancel", body: `{"reason":"CUSTOMER_REQUEST","note":"a\u0000b"}`, cookie: s.customer},
		"nul in a coupon":          {method: "POST", path: "/api/v1/quotes", body: strings.Replace(s.order(s.c.JacketM, "2026-11-17", "x", `,"coupon_code":"a\u0000b"`), `"request_key":"x",`, "", 1)},
		"nul in a profile":         {method: "PUT", path: "/api/v1/me/profile", body: `{"full_name":"a\u0000b","phone":"0901234567"}`, cookie: s.customer},
		"nul in a search":          {method: "GET", path: "/api/v1/catalog/offerings?q=" + url.QueryEscape("a\x00b")},
		"broken text in a search":  {method: "GET", path: "/api/v1/bookings?q=%FF%FE", cookie: s.staff},
		"nul in a status filter":   {method: "GET", path: "/api/v1/bookings?status=" + url.QueryEscape("a\x00"), cookie: s.staff},
		"nul in an asset id":       {method: "POST", path: id + "/returns", body: `{"asset_ids":["a\u0000b"]}`, cookie: s.staff},
		"huge quantity":            {method: "POST", path: "/api/v1/quotes", body: strings.Replace(strings.Replace(s.order(s.c.JacketM, "2026-11-17", "x", ""), `"request_key":"x",`, "", 1), `"quantity":1`, `"quantity":2147483647`, 1)},
		"huge availability":        {method: "POST", path: "/api/v1/availability", body: strings.Replace(strings.Replace(s.order(s.c.JacketM, "2026-11-17", "x", ""), `"request_key":"x",`, "", 1), `"quantity":1`, `"quantity":2147483647`, 1)},
		"year 9999":                {method: "POST", path: "/api/v1/quotes", body: strings.Replace(s.order(s.c.JacketM, "9999-12-31", "x", ""), `"request_key":"x",`, "", 1)},
		"calendar of a millennium": {method: "GET", path: "/api/v1/store/calendar?from=2000-01-01&to=2999-12-31"},
		"schedule of a millennium": {method: "GET", path: "/api/v1/ops/schedule?from=2000-01-01&to=2999-12-31", cookie: s.staff},
		"dashboard of no days":     {method: "GET", path: "/api/v1/admin/dashboard?range_days=0", cookie: s.a},
		"dashboard of many days":   {method: "GET", path: "/api/v1/admin/dashboard?range_days=99999", cookie: s.a},
	}
	for _, name := range slices.Sorted(maps.Keys(calls)) {
		if w := s.do(calls[name]); w.Code >= 500 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}
