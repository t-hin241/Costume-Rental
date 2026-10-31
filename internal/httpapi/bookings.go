package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/catalog/catalogpg"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/money"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalsapp"
)

// errGatewayUnavailable answers online payment while no provider is connected.
var errGatewayUnavailable = &apiError{Status: http.StatusConflict, Code: "payments.gateway_unavailable", Message: "online payment is not available"}

type customerDTO struct {
	ID    identity.UserID `json:"id"`
	Name  string          `json:"name"`
	Email string          `json:"email"`
	Phone string          `json:"phone"`
}

type bookingSummary struct {
	ID            rentals.BookingID     `json:"id"`
	Code          string                `json:"code"`
	Status        rentals.Status        `json:"status"`
	PaymentState  payments.PaymentState `json:"payment_state"`
	Customer      customerDTO           `json:"customer"`
	PickupAt      time.Time             `json:"pickup_at"`
	DueAt         time.Time             `json:"due_at"`
	Outbound      rentals.LegMode       `json:"outbound"`
	Return        rentals.LegMode       `json:"return"`
	PackageLabel  string                `json:"package_label"`
	ItemCount     int                   `json:"item_count"`
	Headline      string                `json:"headline"`
	Image         *imageDTO             `json:"image"`
	RentTotal     moneyDTO              `json:"rent_total"`
	IsOverdue     bool                  `json:"is_overdue"`
	HoldExpiresAt *time.Time            `json:"hold_expires_at"`
	CreatedAt     time.Time             `json:"created_at"`
}

func legOr(m rentals.LegMode) rentals.LegMode {
	if m == "" {
		return rentals.LegStore
	}
	return m
}

func headline(b *rentals.Booking) string {
	if len(b.Lines) == 0 {
		return b.Code
	}
	if extra := len(b.Lines) - 1; extra > 0 {
		return fmt.Sprintf("%s và %d món khác", b.Lines[0].OfferingName, extra)
	}
	return b.Lines[0].OfferingName
}

func summaryOf(v rentalsapp.Summary) bookingSummary {
	b := v.Booking
	out := bookingSummary{
		ID: b.ID, Code: b.Code, Status: b.Status, PaymentState: v.PaymentState,
		Customer: customerDTO{ID: b.CustomerID, Name: v.Customer.Label(), Email: v.Customer.Email, Phone: v.Customer.Phone},
		PickupAt: b.Schedule.PickupAt, DueAt: b.Schedule.DueAt, Outbound: legOr(b.Fulfillment.Outbound), Return: legOr(b.Fulfillment.Return),
		PackageLabel: b.Package.Label, ItemCount: b.ItemCount(), Headline: headline(b), Image: imagePtr(v.Image), RentTotal: moneyOf(b.RentTotal),
		IsOverdue: v.Overdue, CreatedAt: b.CreatedAt,
	}
	if b.Status == rentals.StatusPendingConfirmation {
		t := b.HoldExpiresAt
		out.HoldExpiresAt = &t
	}
	return out
}

type bookingLine struct {
	ID        rentals.LineID       `json:"id"`
	Kind      catalog.OfferingKind `json:"kind"`
	Name      string               `json:"name"`
	Image     *imageDTO            `json:"image"`
	Selection string               `json:"selection"`
	Quantity  int                  `json:"quantity"`
	ListRent  moneyDTO             `json:"list_rent"`
	Discount  moneyDTO             `json:"discount"`
	Rent      moneyDTO             `json:"rent"`
	Security  moneyDTO             `json:"security"`
}

func variantLabel(v catalogpg.VariantInfo) string {
	parts := make([]string, 0, 2)
	for _, p := range []string{v.Size, v.Color} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return v.SKU
	}
	return strings.Join(parts, " · ")
}

// selection says which sizes a line is for, in the customer's words.
func selection(l rentals.Line, variants map[catalog.VariantID]catalogpg.VariantInfo) string {
	if l.Kind == catalog.KindItem && len(l.Components) == 1 {
		return "Size " + variantLabel(variants[l.Components[0].VariantID])
	}
	parts := mapped(l.Components, func(c catalog.ComponentDemand) string {
		v := variants[c.VariantID]
		return fmt.Sprintf("%s: %s", v.ItemName, variantLabel(v))
	})
	return strings.Join(parts, ", ")
}

func lineLabel(l rentals.Line, variants map[catalog.VariantID]catalogpg.VariantInfo) string {
	if l.Kind == catalog.KindItem && len(l.Components) == 1 {
		return fmt.Sprintf("%s (%s)", l.OfferingName, variantLabel(variants[l.Components[0].VariantID]))
	}
	return l.OfferingName
}

type bookingItem struct {
	AssetID     inventory.AssetID           `json:"asset_id"`
	AssetCode   string                      `json:"asset_code"`
	AssetStatus inventory.AssetStatus       `json:"asset_status"`
	LineID      rentals.LineID              `json:"line_id"`
	Name        string                      `json:"name"`
	Variant     string                      `json:"variant_label"`
	State       rentalsapp.BookingItemState `json:"state"`
	Preexisting string                      `json:"preexisting_issues"`
	ReturnedAt  *time.Time                  `json:"returned_at"`
	Conditions  []rentals.Condition         `json:"conditions"`
	Notes       string                      `json:"inspection_notes"`
}

type transitionDTO struct {
	Action string         `json:"action"`
	To     rentals.Status `json:"to"`
	At     time.Time      `json:"at"`
	Actor  string         `json:"actor"`
	Note   string         `json:"note"`
}

type chargeDTO struct {
	ID       payments.ChargeID   `json:"id"`
	Kind     payments.ChargeKind `json:"kind"`
	Amount   moneyDTO            `json:"amount"`
	Reversed bool                `json:"reversed"`
	Reason   string              `json:"reason"`
}

type paymentDTO struct {
	ID     payments.PaymentID     `json:"id"`
	Method payments.Method        `json:"method"`
	Status payments.PaymentStatus `json:"status"`
	Amount moneyDTO               `json:"amount"`
	At     time.Time              `json:"at"`
}

type refundDTO struct {
	ID      payments.RefundID         `json:"id"`
	Bucket  payments.Bucket           `json:"bucket"`
	Status  payments.RefundStatus     `json:"status"`
	Amount  moneyDTO                  `json:"amount"`
	Reason  string                    `json:"reason"`
	At      time.Time                 `json:"at"`
	Actions []rentalsapp.RefundAction `json:"actions"`
}

type financeDTO struct {
	PaymentState         payments.PaymentState         `json:"payment_state"`
	RentTotal            moneyDTO                      `json:"rent_total"`
	DiscountTotal        moneyDTO                      `json:"discount_total"`
	ReservationDeposit   moneyDTO                      `json:"reservation_deposit"`
	ReservationTreatment *rentals.ReservationTreatment `json:"reservation_treatment"`
	SecurityRequired     moneyDTO                      `json:"security_required"`
	SecurityHeld         moneyDTO                      `json:"security_held"`
	DeliveryTotal        moneyDTO                      `json:"delivery_total"`
	FeesTotal            moneyDTO                      `json:"fees_total"`
	PaidTotal            moneyDTO                      `json:"paid_total"`
	RefundPending        moneyDTO                      `json:"refund_pending"`
	RefundedTotal        moneyDTO                      `json:"refunded_total"`
	Outstanding          moneyDTO                      `json:"outstanding"`
	Charges              []chargeDTO                   `json:"charges"`
	Payments             []paymentDTO                  `json:"payments"`
	Refunds              []refundDTO                   `json:"refunds"`
}

func feeTotal(l *payments.Ledger) moneyDTO {
	var total int64
	for _, c := range l.Charges {
		switch c.Kind {
		case payments.ChargeRent, payments.ChargeSecurityDeposit, payments.ChargeReservationDeposit, payments.ChargeDeliveryFee:
			continue
		}
		if c.Reversal == nil {
			total += c.Amount.Amount()
		}
	}
	return moneyDTO{Amount: total, Currency: string(money.VND)}
}

// chargeReason shows what staff wrote. Booking and rule-based charges carry
// audit text instead, and their kind already says what they are.
func chargeReason(c payments.Charge) string {
	switch c.Kind {
	case payments.ChargeRent, payments.ChargeSecurityDeposit, payments.ChargeReservationDeposit, payments.ChargeDeliveryFee,
		payments.ChargeCancellationFee, payments.ChargeLateFee:
		return ""
	}
	return c.Reason
}

func financeOf(d rentalsapp.Detail) financeDTO {
	b := d.Booking
	var treatment *rentals.ReservationTreatment
	if b.ReservationDeposit.IsPositive() {
		treatment = &b.Policy.Reservation.Treatment
	}
	out := financeDTO{
		PaymentState: d.PaymentState, RentTotal: moneyOf(b.RentTotal), DiscountTotal: moneyOf(b.DiscountTotal), ReservationDeposit: moneyOf(b.ReservationDeposit),
		ReservationTreatment: treatment, SecurityRequired: moneyOf(b.SecurityDepositTotal), SecurityHeld: moneyOf(money.Zero(money.VND)),
		DeliveryTotal: moneyOf(b.Fulfillment.DeliveryFees(b.Currency)), FeesTotal: moneyOf(money.Zero(money.VND)), PaidTotal: moneyOf(money.Zero(money.VND)),
		RefundPending: moneyOf(money.Zero(money.VND)), RefundedTotal: moneyOf(money.Zero(money.VND)), Outstanding: moneyOf(money.Zero(money.VND)),
		Charges: []chargeDTO{}, Payments: []paymentDTO{}, Refunds: []refundDTO{},
	}
	l := d.Ledger
	if l == nil {
		return out
	}
	s := l.Summary()
	out.SecurityRequired, out.SecurityHeld = moneyOf(s.SecurityRequired), moneyOf(s.SecurityHeld)
	out.FeesTotal, out.PaidTotal = feeTotal(l), moneyOf(s.TotalReceived)
	out.RefundPending, out.RefundedTotal, out.Outstanding = moneyOf(s.PendingRefunds), moneyOf(s.TotalRefunded), moneyOf(l.Collectable())
	out.Charges = mapped(l.Charges, func(c payments.Charge) chargeDTO {
		return chargeDTO{ID: c.ID, Kind: c.Kind, Amount: moneyOf(c.Amount), Reversed: c.Reversal != nil, Reason: chargeReason(c)}
	})
	out.Payments = mapped(l.Payments, func(p payments.Payment) paymentDTO {
		at := p.CreatedAt
		if p.SettledAt != nil {
			at = *p.SettledAt
		}
		return paymentDTO{ID: p.ID, Method: p.Method, Status: p.Status, Amount: moneyOf(p.Amount), At: at}
	})
	out.Refunds = mapped(l.Refunds, func(r payments.Refund) refundDTO {
		return refundDTO{ID: r.ID, Bucket: r.Bucket, Status: r.Status, Amount: moneyOf(r.Amount), Reason: r.Reason, At: r.RequestedAt, Actions: nonNil(d.RefundActions[r.ID])}
	})
	return out
}

type feeDTO struct {
	ID        rentals.AssessmentID     `json:"id"`
	Kind      rentals.FeeKind          `json:"kind"`
	Status    rentals.AssessmentStatus `json:"status"`
	Amount    moneyDTO                 `json:"amount"`
	AssetCode string                   `json:"asset_code"`
	Basis     string                   `json:"basis"`
	CreatedAt time.Time                `json:"created_at"`
	Actions   []rentals.FeeAction      `json:"actions"`
}

type bookingDetail struct {
	bookingSummary
	HandedOverAt   *time.Time              `json:"handed_over_at"`
	ReturnedAt     *time.Time              `json:"returned_at"`
	PolicyVersion  string                  `json:"policy_version"`
	Lines          []bookingLine           `json:"lines"`
	Items          []bookingItem           `json:"items"`
	Transitions    []transitionDTO         `json:"transitions"`
	Finance        financeDTO              `json:"finance"`
	Fees           []feeDTO                `json:"fees"`
	Actions        []rentals.BookingAction `json:"actions"`
	PaymentOptions []any                   `json:"payment_options"`
}

func detailOf(d rentalsapp.Detail) bookingDetail {
	b := d.Booking
	return bookingDetail{
		bookingSummary: summaryOf(d.Summary), HandedOverAt: timePtr(b.HandedOverAt), ReturnedAt: timePtr(b.ReturnedAt), PolicyVersion: b.Policy.Version,
		Lines: mapped(b.Lines, func(l rentals.Line) bookingLine {
			return bookingLine{ID: l.ID, Kind: l.Kind, Name: l.OfferingName, Image: imagePtr(d.LineImages[l.ID]), Selection: selection(l, d.Variants), Quantity: l.Quantity,
				ListRent: moneyOf(l.ListRent), Discount: moneyOf(l.Discount), Rent: moneyOf(l.RentAmount), Security: moneyOf(l.DepositAmount)}
		}),
		Items: mapped(d.Items, func(i rentalsapp.ItemView) bookingItem {
			return bookingItem{AssetID: i.AssetID, AssetCode: i.AssetCode, AssetStatus: i.AssetStatus, LineID: i.LineID, Name: i.Variant.ItemName, Variant: variantLabel(i.Variant),
				State: i.State, Preexisting: i.Preexisting, ReturnedAt: timePtr(i.ReturnedAt), Conditions: nonNil(i.Conditions), Notes: i.Notes}
		}),
		Transitions: mapped(d.Transitions, func(t rentalsapp.TransitionView) transitionDTO {
			return transitionDTO{Action: t.Action, To: t.To, At: t.At, Actor: t.Actor, Note: t.Note}
		}),
		Finance: financeOf(d),
		Fees: mapped(d.Fees, func(f rentalsapp.FeeView) feeDTO {
			return feeDTO{ID: f.ID, Kind: f.Kind, Status: f.Status, Amount: moneyOf(f.Amount), AssetCode: f.AssetCode, Basis: f.Basis, CreatedAt: f.ProposedAt, Actions: nonNil(f.Actions)}
		}),
		Actions: nonNil(d.Actions), PaymentOptions: []any{},
	}
}

func bookingID(r *http.Request) (rentals.BookingID, error) {
	id, err := pathID(r, "id")
	return rentals.BookingID(id), err
}

// respondBooking answers a booking action with the booking as it now stands.
func (s *Server) respondBooking(w http.ResponseWriter, r *http.Request, p identity.Principal, id rentals.BookingID, status int, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	d, err := s.svc.Rentals.View(r.Context(), p, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, status, detailOf(d))
}

// onBooking runs a bodyless action on the booking in the path.
func (s *Server) onBooking(w http.ResponseWriter, r *http.Request, p identity.Principal, run func(rentals.BookingID) error) {
	id, err := bookingID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.respondBooking(w, r, p, id, http.StatusOK, run(id))
}

func withBody[T any](s *Server, w http.ResponseWriter, r *http.Request, p identity.Principal, run func(rentals.BookingID, T) error) {
	id, err := bookingID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	in, err := decodeSmall[T](w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.respondBooking(w, r, p, id, http.StatusOK, run(id, in))
}

func (s *Server) placeBooking(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	in, err := s.parseBooking(w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if strings.TrimSpace(in.RequestKey) == "" {
		writeError(w, r, domainerr.Invalid("request_key", "required"))
		return
	}
	b, err := s.svc.Rentals.PlaceBooking(r.Context(), p, in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.respondBooking(w, r, p, b.ID, http.StatusCreated, nil)
}

func (s *Server) listBookings(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	page, size, err := pageParams(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	term, err := textParam(r, "q")
	if err != nil {
		writeError(w, r, err)
		return
	}
	in := rentalsapp.ListInput{Term: term, Status: rentals.Status(r.URL.Query().Get("status")), Page: page, PageSize: size}
	if v := r.URL.Query().Get("from"); v != "" {
		from, err := s.dayStart(v, "from")
		if err != nil {
			writeError(w, r, err)
			return
		}
		in.From = &from
	}
	if v := r.URL.Query().Get("to"); v != "" {
		last, err := s.dayStart(v, "to")
		if err != nil {
			writeError(w, r, err)
			return
		}
		end := last.AddDate(0, 0, 1)
		in.To = &end
	}
	rows, total, err := s.svc.Rentals.Bookings(r.Context(), p, in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pageBody[bookingSummary]{Items: mapped(rows, summaryOf), Page: page, PageSize: size, Total: total})
}

func (s *Server) myBookings(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	page, size, err := pageParams(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var finished bool
	switch r.URL.Query().Get("scope") {
	case "", "active":
	case "history":
		finished = true
	default:
		writeError(w, r, domainerr.Invalid("scope", "must be active or history"))
		return
	}
	rows, total, err := s.svc.Rentals.MyBookings(r.Context(), p, finished, page, size)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pageBody[bookingSummary]{Items: mapped(rows, summaryOf), Page: page, PageSize: size, Total: total})
}

func (s *Server) getBooking(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	id, err := bookingID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.respondBooking(w, r, p, id, http.StatusOK, nil)
}

func (s *Server) cancellationPreview(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	id, err := bookingID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	c, err := s.svc.Rentals.PreviewCancellation(r.Context(), p, id, rentals.CancelReason(r.URL.Query().Get("reason")))
	if err != nil {
		writeError(w, r, err)
		return
	}
	refund, err := money.Sum(money.VND, c.Result.RefundableRental, c.Result.RefundableReservation, c.Result.RefundableSecurity)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"fee_percent": c.FeePercent, "fee": moneyOf(c.Result.Fee), "refund": moneyOf(refund), "owed": moneyOf(c.Result.Owed), "basis": c.Basis,
	})
}

func (s *Server) cancelBooking(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	type body struct {
		Reason rentals.CancelReason `json:"reason"`
		Note   string               `json:"note"`
	}
	withBody(s, w, r, p, func(id rentals.BookingID, in body) error {
		if len(in.Note) > maxNote*4 {
			return domainerr.Invalid("note", "too long")
		}
		_, err := s.svc.Rentals.Cancel(r.Context(), p, id, rentalsapp.CancelInput{Reason: in.Reason, Note: in.Note})
		return err
	})
}

func (s *Server) rescheduleBooking(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	type body struct {
		PickupDate string `json:"pickup_date"`
		SlotID     string `json:"slot_id"`
	}
	withBody(s, w, r, p, func(id rentals.BookingID, in body) error {
		date, err := parseDate(in.PickupDate, "pickup_date")
		if err != nil {
			return err
		}
		_, err = s.svc.Rentals.Reschedule(r.Context(), p, id, rentalsapp.RescheduleInput{PickupDate: date, SlotID: in.SlotID})
		return err
	})
}

func (s *Server) confirmBooking(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	s.onBooking(w, r, p, func(id rentals.BookingID) error {
		_, err := s.svc.Rentals.Confirm(r.Context(), p, id)
		return err
	})
}

func (s *Server) startPreparation(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	s.onBooking(w, r, p, func(id rentals.BookingID) error {
		_, err := s.svc.Rentals.StartPreparation(r.Context(), p, id)
		return err
	})
}

func (s *Server) handover(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	type delivered struct {
		AssetID           inventory.AssetID `json:"asset_id"`
		PreexistingIssues string            `json:"preexisting_issues"`
	}
	type body struct {
		ChecklistCompleted bool        `json:"checklist_completed"`
		Delivered          []delivered `json:"delivered"`
	}
	withBody(s, w, r, p, func(id rentals.BookingID, in body) error {
		_, err := s.svc.Rentals.RecordHandover(r.Context(), p, id, rentalsapp.HandoverInput{
			ChecklistCompleted: in.ChecklistCompleted,
			Delivered: mapped(in.Delivered, func(d delivered) rentals.DeliveredItem {
				return rentals.DeliveredItem{AssetID: d.AssetID, PreexistingIssues: d.PreexistingIssues}
			}),
		})
		return err
	})
}

func (s *Server) recordReturn(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	type body struct {
		AssetIDs []inventory.AssetID `json:"asset_ids"`
	}
	withBody(s, w, r, p, func(id rentals.BookingID, in body) error {
		_, err := s.svc.Rentals.RecordReturn(r.Context(), p, id, in.AssetIDs)
		return err
	})
}

func (s *Server) reportMissing(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	type body struct {
		AssetID inventory.AssetID `json:"asset_id"`
	}
	withBody(s, w, r, p, func(id rentals.BookingID, in body) error {
		_, err := s.svc.Rentals.ReportMissing(r.Context(), p, id, in.AssetID)
		return err
	})
}

func (s *Server) confirmLost(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	type body struct {
		AssetID inventory.AssetID `json:"asset_id"`
		Reason  string            `json:"reason"`
	}
	withBody(s, w, r, p, func(id rentals.BookingID, in body) error {
		_, err := s.svc.Rentals.ConfirmLost(r.Context(), p, id, in.AssetID, in.Reason)
		return err
	})
}

func (s *Server) inspect(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	type body struct {
		AssetID    inventory.AssetID   `json:"asset_id"`
		Conditions []rentals.Condition `json:"conditions"`
		Notes      string              `json:"notes"`
		Evidence   []string            `json:"evidence"`
	}
	withBody(s, w, r, p, func(id rentals.BookingID, in body) error {
		_, err := s.svc.Rentals.RecordInspection(r.Context(), p, id, rentalsapp.InspectionInput{
			Asset: in.AssetID, Conditions: in.Conditions, Notes: in.Notes, Evidence: in.Evidence,
		})
		return err
	})
}

func (s *Server) proposeFee(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	type body struct {
		AssetID            inventory.AssetID `json:"asset_id"`
		Kind               rentals.FeeKind   `json:"kind"`
		Amount             moneyDTO          `json:"amount"`
		Basis              string            `json:"basis"`
		Evidence           []string          `json:"evidence"`
		ConfirmedNewDamage bool              `json:"confirmed_new_damage"`
	}
	id, err := bookingID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	in, err := decodeSmall[body](w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	amount, err := in.Amount.value("amount")
	if err != nil {
		writeError(w, r, err)
		return
	}
	a, err := s.svc.Rentals.ProposeFee(r.Context(), p, id, rentalsapp.FeeInput{
		Asset: in.AssetID, Kind: in.Kind, Amount: amount, Basis: in.Basis, Evidence: in.Evidence, ConfirmedNewDamage: in.ConfirmedNewDamage,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, feeDTO{ID: a.ID, Kind: a.Kind, Status: a.Status, Amount: moneyOf(a.Amount), Basis: a.Basis, CreatedAt: a.ProposedAt, Actions: []rentals.FeeAction{}})
}

func (s *Server) decideFee(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	fee, err := pathID(r, "fee")
	if err != nil {
		writeError(w, r, err)
		return
	}
	type body struct {
		Decision rentalsapp.FeeDecision `json:"decision"`
		Note     string                 `json:"note"`
	}
	withBody(s, w, r, p, func(id rentals.BookingID, in body) error {
		return s.svc.Rentals.DecideBookingFee(r.Context(), p, id, rentals.AssessmentID(fee), in.Decision, in.Note)
	})
}

func (s *Server) recordPayment(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	type portion struct {
		Bucket payments.Bucket `json:"bucket"`
		Amount moneyDTO        `json:"amount"`
	}
	type body struct {
		IdempotencyKey string          `json:"idempotency_key"`
		Method         payments.Method `json:"method"`
		Portions       []portion       `json:"portions"`
	}
	withBody(s, w, r, p, func(id rentals.BookingID, in body) error {
		portions := make([]payments.Portion, 0, len(in.Portions))
		for _, pt := range in.Portions {
			m, err := pt.Amount.value("portions")
			if err != nil {
				return err
			}
			portions = append(portions, payments.Portion{Bucket: pt.Bucket, Amount: m})
		}
		_, err := s.svc.Rentals.RecordPayment(r.Context(), p, id, rentalsapp.PaymentInput{
			IdempotencyKey: in.IdempotencyKey, Method: in.Method, Portions: portions,
		})
		return err
	})
}

// startPaymentSession and paymentSession are the hosted checkout of the API
// contract. They answer plainly until a payment provider is connected.
func (s *Server) startPaymentSession(w http.ResponseWriter, r *http.Request, _ identity.Principal, _ identity.Session) {
	writeError(w, r, errGatewayUnavailable)
}

func (s *Server) paymentSession(w http.ResponseWriter, r *http.Request, _ identity.Principal, _ identity.Session) {
	writeError(w, r, errGatewayUnavailable)
}

func (s *Server) approveRefund(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	refund, err := pathID(r, "refund")
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.onBooking(w, r, p, func(id rentals.BookingID) error {
		_, err := s.svc.Rentals.ApproveRefund(r.Context(), p, id, payments.RefundID(refund))
		return err
	})
}

func (s *Server) rejectRefund(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	refund, err := pathID(r, "refund")
	if err != nil {
		writeError(w, r, err)
		return
	}
	type body struct {
		Note string `json:"note"`
	}
	withBody(s, w, r, p, func(id rentals.BookingID, in body) error {
		_, err := s.svc.Rentals.RejectRefund(r.Context(), p, id, payments.RefundID(refund), in.Note)
		return err
	})
}

func (s *Server) payoutRefund(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	refund, err := pathID(r, "refund")
	if err != nil {
		writeError(w, r, err)
		return
	}
	type body struct {
		Method    payments.Method `json:"method"`
		Reference string          `json:"reference"`
		PaidAt    time.Time       `json:"paid_at"`
	}
	withBody(s, w, r, p, func(id rentals.BookingID, in body) error {
		_, err := s.svc.Rentals.RecordCounterRefund(r.Context(), p, id, payments.RefundID(refund), payments.RefundPayout{
			Method: in.Method, Reference: in.Reference, PaidAt: in.PaidAt,
		})
		return err
	})
}

func (s *Server) settleBooking(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	s.onBooking(w, r, p, func(id rentals.BookingID) error {
		_, err := s.svc.Rentals.SettleRental(r.Context(), p, id)
		return err
	})
}

func (s *Server) completeBooking(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	s.onBooking(w, r, p, func(id rentals.BookingID) error {
		_, err := s.svc.Rentals.Complete(r.Context(), p, id)
		return err
	})
}
