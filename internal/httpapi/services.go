package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"webee_rentor/internal/backoffice"
	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/catalog/catalogapp"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory/inventoryapp"
	"webee_rentor/internal/money"
	"webee_rentor/internal/rentals/rentalsapp"
	"webee_rentor/internal/support/supportapp"
)

// StoreInfo is what the storefront says about the shop.
type StoreInfo struct {
	Name                string
	Address             string
	Phone               string
	Email               string
	Hours               string
	PaymentInstructions string
}

// Services are the use cases behind the business API. Without them the server
// only answers the authentication and account endpoints.
type Services struct {
	Catalog    *catalogapp.Service
	Rentals    *rentalsapp.Service
	Inventory  *inventoryapp.Service
	Support    *supportapp.Service
	Backoffice *backoffice.Service
	Store      StoreInfo
}

const (
	defaultPageSize = 20
	maxPageSize     = 100
	maxQueryText    = 120
	maxNote         = 500
	maxLines        = 50
)

// routes registers the business API. Handlers parse input, call one use case
// and write its result: authorization and rules stay in the use cases.
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/catalog/categories", s.categories)
	mux.HandleFunc("GET /api/v1/catalog/packages", s.packages)
	mux.HandleFunc("GET /api/v1/catalog/offerings", s.offerings)
	mux.HandleFunc("GET /api/v1/catalog/offerings/{kind}/{slug}", s.offering)
	mux.HandleFunc("GET /api/v1/store/info", s.storeInfo)
	mux.HandleFunc("GET /api/v1/store/calendar", s.storeCalendar)
	mux.HandleFunc("GET /api/v1/store/fulfillment-options", s.fulfillmentOptions)
	mux.HandleFunc("GET /api/v1/policies/public", s.publicPolicy)
	mux.HandleFunc("POST /api/v1/availability", s.availability)
	mux.HandleFunc("POST /api/v1/quotes", s.quote)

	mux.HandleFunc("POST /api/v1/bookings", s.authed(s.placeBooking))
	mux.HandleFunc("GET /api/v1/bookings", s.authed(s.listBookings))
	mux.HandleFunc("GET /api/v1/me/bookings", s.authed(s.myBookings))
	mux.HandleFunc("GET /api/v1/bookings/{id}", s.authed(s.getBooking))
	mux.HandleFunc("GET /api/v1/bookings/{id}/cancellation-preview", s.authed(s.cancellationPreview))
	mux.HandleFunc("POST /api/v1/bookings/{id}/cancel", s.authed(s.cancelBooking))
	mux.HandleFunc("POST /api/v1/bookings/{id}/reschedule", s.authed(s.rescheduleBooking))
	mux.HandleFunc("POST /api/v1/bookings/{id}/confirm", s.authed(s.confirmBooking))
	mux.HandleFunc("POST /api/v1/bookings/{id}/start-preparation", s.authed(s.startPreparation))
	mux.HandleFunc("POST /api/v1/bookings/{id}/handover", s.authed(s.handover))
	mux.HandleFunc("POST /api/v1/bookings/{id}/returns", s.authed(s.recordReturn))
	mux.HandleFunc("POST /api/v1/bookings/{id}/report-missing", s.authed(s.reportMissing))
	mux.HandleFunc("POST /api/v1/bookings/{id}/confirm-lost", s.authed(s.confirmLost))
	mux.HandleFunc("POST /api/v1/bookings/{id}/inspections", s.authed(s.inspect))
	mux.HandleFunc("POST /api/v1/bookings/{id}/fees", s.authed(s.proposeFee))
	mux.HandleFunc("POST /api/v1/bookings/{id}/fees/{fee}/decision", s.authed(s.decideFee))
	mux.HandleFunc("POST /api/v1/bookings/{id}/payments", s.authed(s.recordPayment))
	mux.HandleFunc("POST /api/v1/bookings/{id}/payment-sessions", s.authed(s.startPaymentSession))
	mux.HandleFunc("GET /api/v1/payment-sessions/{id}", s.authed(s.paymentSession))
	mux.HandleFunc("POST /api/v1/bookings/{id}/refunds/{refund}/approve", s.authed(s.approveRefund))
	mux.HandleFunc("POST /api/v1/bookings/{id}/refunds/{refund}/reject", s.authed(s.rejectRefund))
	mux.HandleFunc("POST /api/v1/bookings/{id}/refunds/{refund}/payout", s.authed(s.payoutRefund))
	mux.HandleFunc("POST /api/v1/bookings/{id}/settle", s.authed(s.settleBooking))
	mux.HandleFunc("POST /api/v1/bookings/{id}/complete", s.authed(s.completeBooking))

	mux.HandleFunc("GET /api/v1/ops/overview", s.authed(s.opsOverview))
	mux.HandleFunc("GET /api/v1/ops/schedule", s.authed(s.opsSchedule))
	mux.HandleFunc("GET /api/v1/ops/alerts", s.authed(s.opsAlerts))
	mux.HandleFunc("GET /api/v1/inventory/assets", s.authed(s.listAssets))
	mux.HandleFunc("POST /api/v1/inventory/assets", s.authed(s.registerAsset))
	mux.HandleFunc("GET /api/v1/inventory/assets/{id}/history", s.authed(s.assetHistory))
	mux.HandleFunc("POST /api/v1/inventory/assets/{id}/status", s.authed(s.changeAssetStatus))

	mux.HandleFunc("GET /api/v1/me/profile", s.authed(s.getProfile))
	mux.HandleFunc("PUT /api/v1/me/profile", s.authed(s.saveProfile))
	mux.HandleFunc("GET /api/v1/me/support-requests", s.authed(s.mySupportRequests))
	mux.HandleFunc("POST /api/v1/me/support-requests", s.authed(s.createSupportRequest))
	mux.HandleFunc("GET /api/v1/support-requests", s.authed(s.supportRequests))
	mux.HandleFunc("POST /api/v1/support-requests/{id}/replies", s.authed(s.replySupportRequest))

	mux.HandleFunc("GET /api/v1/admin/dashboard", s.authed(s.dashboard))
	mux.HandleFunc("GET /api/v1/admin/users", s.authed(s.adminUsers))
	mux.HandleFunc("GET /api/v1/admin/audit-events", s.authed(s.auditEvents))
	mux.HandleFunc("GET /api/v1/admin/catalog/items", s.authed(s.adminItems))
	mux.HandleFunc("POST /api/v1/admin/catalog/items", s.authed(s.saveItem))
	mux.HandleFunc("PUT /api/v1/admin/catalog/items/{id}", s.authed(s.saveItem))
	mux.HandleFunc("GET /api/v1/admin/catalog/bundles", s.authed(s.adminBundles))
	mux.HandleFunc("POST /api/v1/admin/catalog/bundles", s.authed(s.saveBundle))
	mux.HandleFunc("PUT /api/v1/admin/catalog/bundles/{id}", s.authed(s.saveBundle))
	mux.HandleFunc("POST /api/v1/admin/catalog/categories", s.authed(s.saveCategory))
	mux.HandleFunc("PUT /api/v1/admin/catalog/categories/{id}", s.authed(s.saveCategory))
	mux.HandleFunc("POST /api/v1/admin/catalog/packages", s.authed(s.savePackage))
	mux.HandleFunc("PUT /api/v1/admin/catalog/packages/{id}", s.authed(s.savePackage))
	mux.HandleFunc("GET /api/v1/admin/policies/active", s.authed(s.activePolicy))
	mux.HandleFunc("POST /api/v1/admin/policies", s.authed(s.publishPolicy))
	mux.HandleFunc("GET /api/v1/admin/promotions", s.authed(s.campaigns))
	mux.HandleFunc("POST /api/v1/admin/promotions", s.authed(s.saveCampaign))
	mux.HandleFunc("PUT /api/v1/admin/promotions/{id}", s.authed(s.saveCampaign))
}

// viewer resolves the session of a public endpoint; a guest has none.
func (s *Server) viewer(ctx context.Context, r *http.Request) *identity.Principal {
	c, err := r.Cookie(s.cookieName())
	if err != nil {
		return nil
	}
	p, _, err := s.auth.Authenticate(ctx, c.Value)
	if err != nil {
		return nil
	}
	return &p
}

type moneyDTO struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

func moneyOf(m money.Money) moneyDTO {
	if !m.Currency().Valid() {
		return moneyDTO{Currency: string(money.VND)} // an amount never set
	}
	return moneyDTO{Amount: m.Amount(), Currency: string(m.Currency())}
}

func moneyPtr(m *money.Money) *moneyDTO {
	if m == nil {
		return nil
	}
	v := moneyOf(*m)
	return &v
}

func (d moneyDTO) value(field string) (money.Money, error) {
	c, err := money.ParseCurrency(d.Currency)
	if err != nil {
		return money.Money{}, domainerr.Invalid(field, "currency must be VND")
	}
	m, err := money.New(d.Amount, c)
	if err != nil {
		return money.Money{}, domainerr.Invalid(field, "amount out of range")
	}
	return m, nil
}

type pageBody[T any] struct {
	Items    []T `json:"items"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
}

func mapped[V, T any](rows []V, f func(V) T) []T {
	out := make([]T, len(rows))
	for i, r := range rows {
		out[i] = f(r)
	}
	return out
}

func pageParams(r *http.Request) (int, int, error) {
	page, size := 1, defaultPageSize
	if v := r.URL.Query().Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1_000_000 {
			return 0, 0, domainerr.Invalid("page", "must be a positive number")
		}
		page = n
	}
	if v := r.URL.Query().Get("page_size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPageSize {
			return 0, 0, domainerr.Invalid("page_size", "must be between 1 and 100")
		}
		size = n
	}
	return page, size, nil
}

func textParam(r *http.Request, name string) (string, error) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if utf8.RuneCountInString(v) > maxQueryText {
		return "", domainerr.Invalid(name, "too long")
	}
	return v, nil
}

func dateParam(r *http.Request, name string) (businesstime.Date, error) {
	d, err := businesstime.ParseDate(r.URL.Query().Get(name))
	if err != nil {
		return businesstime.Date{}, domainerr.Invalid(name, "must be a date (YYYY-MM-DD)")
	}
	return d, nil
}

func pathID(r *http.Request, name string) (string, error) {
	v := r.PathValue(name)
	if v == "" || len(v) > maxIDLength {
		return "", domainerr.Invalid(name, "invalid id")
	}
	return v, nil
}

func decodeSmall[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	var v T
	err := decodeJSON(w, r, &v, smallBody)
	return v, err
}

func timePtr(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// jsonNull keeps an empty optional string out of the response.
func jsonNull(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
