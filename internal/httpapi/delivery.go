package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/delivery"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalsapp"
)

// Delivery runs in manual mode (decision D43): staff book shipments in the
// carrier's app and record here what happens. There is no carrier webhook yet.

type addressDTO struct {
	RecipientName string `json:"recipient_name"`
	Phone         string `json:"phone"`
	Line          string `json:"line"`
	WardCode      string `json:"ward_code"`
	WardName      string `json:"ward_name"`
}

type attemptDTO struct {
	ID         string          `json:"id"`
	Status     delivery.Status `json:"status"`
	At         time.Time       `json:"at"`
	Fault      delivery.Fault  `json:"fault"`
	Reason     string          `json:"reason"`
	Classified bool            `json:"classified"`
}

type deliveryTaskDTO struct {
	ID             delivery.TaskID       `json:"id"`
	BookingID      string                `json:"booking_id"`
	BookingCode    string                `json:"booking_code"`
	CustomerName   string                `json:"customer_name"`
	Direction      delivery.Direction    `json:"direction"`
	Status         delivery.Status       `json:"status"`
	Provider       string                `json:"provider"`
	ShipmentCode   *string               `json:"shipment_code"`
	WindowFrom     time.Time             `json:"window_from"`
	WindowUntil    time.Time             `json:"window_until"`
	ExpectedAt     *time.Time            `json:"expected_at"`
	PickedUpAt     *time.Time            `json:"picked_up_at"`
	DeliveredAt    *time.Time            `json:"delivered_at"`
	ReceivedAt     *time.Time            `json:"received_at"`
	CustomerFee    moneyDTO              `json:"customer_fee"`
	Address        addressDTO            `json:"address"`
	Attempts       []attemptDTO          `json:"attempts"`
	GaveUpAt       *time.Time            `json:"gave_up_at"`
	FollowUp       *delivery.FollowUp    `json:"follow_up"`
	ClosedReason   string                `json:"closed_reason"`
	Replaces       *delivery.TaskID      `json:"replaces"`
	ReplacedBy     *delivery.TaskID      `json:"replaced_by"`
	FailureReason  string                `json:"failure_reason"`
	Actions        []delivery.TaskAction `json:"actions"`
	CanCollect     bool                  `json:"can_collect_at_store"`
	CanEndBooking  bool                  `json:"can_end_booking"`
	AwaitingAction bool                  `json:"awaiting_follow_up"`
}

func ptrIf[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// deliveryTaskOf maps a view for staff (staff) or for the customer. The
// customer sees their shipment and its progress, not the staff workflow.
func deliveryTaskOf(v rentalsapp.DeliveryView, wards map[string]string, staff bool) deliveryTaskDTO {
	t, b := v.Task, v.Booking.Booking
	addr := t.To
	if t.Direction == delivery.Return {
		addr = t.From
	}
	d := deliveryTaskDTO{
		ID: t.ID, BookingID: t.BookingRef, BookingCode: b.Code, CustomerName: v.Booking.Customer.Label(), Direction: t.Direction,
		Status: t.Status, Provider: t.ProviderCode, ShipmentCode: ptrIf(t.ProviderShipmentCode),
		WindowFrom: t.Window.Start(), WindowUntil: t.Window.End(), ExpectedAt: t.ExpectedAt, PickedUpAt: t.PickedUpAt,
		DeliveredAt: t.DeliveredAt, ReceivedAt: t.ReceivedAt, CustomerFee: moneyOf(t.CustomerFee),
		Address:  addressDTO{RecipientName: addr.RecipientName, Phone: addr.Phone, Line: addr.Line, WardCode: addr.WardCode, WardName: wards[addr.WardCode]},
		Attempts: []attemptDTO{}, Actions: []delivery.TaskAction{},
		GaveUpAt: t.GaveUpAt, FollowUp: ptrIf(t.FollowUp), Replaces: ptrIf(t.Replaces), ReplacedBy: ptrIf(t.ReplacedBy),
	}
	if !staff {
		return d
	}
	d.ClosedReason, d.FailureReason, d.AwaitingAction = t.ClosedReason, t.FailureReason, t.AwaitingFollowUp()
	d.Attempts = mapped(t.Attempts, func(a delivery.Attempt) attemptDTO {
		return attemptDTO{ID: a.ID, Status: a.Status, At: a.At, Fault: a.Fault, Reason: a.Reason, Classified: a.Fault != delivery.FaultUnknown}
	})
	d.Actions = t.Actions()
	notDelivered := b.Status == rentals.StatusRented && b.Fulfillment.DeliveredAt == nil
	closedFor := func(f delivery.FollowUp) bool {
		return t.Direction == delivery.Outbound && t.Status == delivery.StatusClosedUnfulfilled && t.FollowUp == f && notDelivered
	}
	d.CanCollect, d.CanEndBooking = closedFor(delivery.FollowUpAtStore), closedFor(delivery.FollowUpEndBooking) && b.Fulfillment.Undelivered == nil
	return d
}

// wardNames maps ward codes of the active area to their names.
func (s *Server) wardNames(r *http.Request) map[string]string {
	out := map[string]string{}
	opts, err := s.svc.Rentals.FulfillmentOptions(r.Context())
	if err != nil {
		return out
	}
	for _, z := range opts.Zones {
		for _, w := range z.Wards {
			out[w.Code] = w.Name
		}
	}
	return out
}

func (s *Server) deliveryBoard(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	page, size, err := pageParams(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	open := true
	if v := r.URL.Query().Get("open"); v != "" {
		if open, err = strconv.ParseBool(v); err != nil {
			writeError(w, r, domainerr.Invalid("open", "must be true or false"))
			return
		}
	}
	views, total, err := s.svc.Rentals.DeliveryBoard(r.Context(), p, open, page, size)
	if err != nil {
		writeError(w, r, err)
		return
	}
	wards := s.wardNames(r)
	writeJSON(w, http.StatusOK, pageBody[deliveryTaskDTO]{
		Items: mapped(views, func(v rentalsapp.DeliveryView) deliveryTaskDTO { return deliveryTaskOf(v, wards, true) }),
		Page:  page, PageSize: size, Total: total,
	})
}

func (s *Server) bookingDeliveries(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	id, err := bookingID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	views, err := s.svc.Rentals.BookingDeliveries(r.Context(), p, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	wards, staff := s.wardNames(r), p.Can(identity.PermDeliveryReadAll)
	writeJSON(w, http.StatusOK, map[string]any{
		"items": mapped(views, func(v rentalsapp.DeliveryView) deliveryTaskDTO { return deliveryTaskOf(v, wards, staff) }),
	})
}

func taskID(r *http.Request) (delivery.TaskID, error) {
	id, err := pathID(r, "id")
	return delivery.TaskID(id), err
}

// withTaskBody runs a staff delivery action and answers with the task as it
// now stands.
func withTaskBody[T any](s *Server, w http.ResponseWriter, r *http.Request, p identity.Principal, run func(delivery.TaskID, T) (*delivery.Task, error)) {
	id, err := taskID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	in, err := decodeSmall[T](w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	t, err := run(id, in)
	s.respondTask(w, r, p, t, err)
}

// onTask runs a staff delivery action that takes no body.
func onTask(s *Server, w http.ResponseWriter, r *http.Request, p identity.Principal, run func(delivery.TaskID) (*delivery.Task, error)) {
	id, err := taskID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	t, err := run(id)
	s.respondTask(w, r, p, t, err)
}

// respondTask answers with the task as the caller may now see it.
func (s *Server) respondTask(w http.ResponseWriter, r *http.Request, p identity.Principal, t *delivery.Task, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	views, err := s.svc.Rentals.BookingDeliveries(r.Context(), p, rentals.BookingID(t.BookingRef))
	if err != nil {
		writeError(w, r, err)
		return
	}
	wards := s.wardNames(r)
	for _, v := range views {
		if v.Task.ID == t.ID {
			writeJSON(w, http.StatusOK, deliveryTaskOf(v, wards, true))
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) recordShipment(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	type body struct {
		ShipmentCode string     `json:"shipment_code"`
		ExpectedAt   *time.Time `json:"expected_at"`
		EstimatedFee *moneyDTO  `json:"estimated_fee"`
	}
	withTaskBody(s, w, r, p, func(id delivery.TaskID, in body) (*delivery.Task, error) {
		if len(in.ShipmentCode) > maxIDLength {
			return nil, domainerr.Invalid("shipment_code", "too long")
		}
		sh := rentalsapp.ShipmentInput{Code: in.ShipmentCode, ExpectedAt: in.ExpectedAt}
		if in.EstimatedFee != nil {
			fee, err := in.EstimatedFee.value("estimated_fee")
			if err != nil {
				return nil, err
			}
			sh.EstimatedFee = &fee
		}
		return s.svc.Rentals.RecordShipment(r.Context(), p, id, sh)
	})
}

func (s *Server) markReadyForPickup(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	onTask(s, w, r, p, func(id delivery.TaskID) (*delivery.Task, error) {
		return s.svc.Rentals.MarkReadyForPickup(r.Context(), p, id)
	})
}

func (s *Server) recordDeliveryEvent(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	type body struct {
		Status   delivery.Status `json:"status"`
		Fault    delivery.Fault  `json:"fault"`
		GaveUp   bool            `json:"gave_up"`
		Reason   string          `json:"reason"`
		Evidence string          `json:"evidence"`
		At       *time.Time      `json:"at"`
	}
	withTaskBody(s, w, r, p, func(id delivery.TaskID, in body) (*delivery.Task, error) {
		if len(in.Reason) > maxNote || len(in.Evidence) > maxNote {
			return nil, domainerr.Invalid("reason", "too long")
		}
		return s.svc.Rentals.RecordDeliveryEvent(r.Context(), p, id, rentalsapp.DeliveryEventInput{
			Status: in.Status, Fault: in.Fault, GaveUp: in.GaveUp, Reason: in.Reason, Evidence: in.Evidence, At: in.At,
		})
	})
}

func (s *Server) classifyDeliveryAttempt(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	type body struct {
		AttemptID string         `json:"attempt_id"`
		Fault     delivery.Fault `json:"fault"`
		Reason    string         `json:"reason"`
	}
	withTaskBody(s, w, r, p, func(id delivery.TaskID, in body) (*delivery.Task, error) {
		if len(in.Reason) > maxNote {
			return nil, domainerr.Invalid("reason", "too long")
		}
		return s.svc.Rentals.ClassifyDeliveryAttempt(r.Context(), p, id, in.AttemptID, in.Fault, in.Reason)
	})
}

func (s *Server) confirmShipmentCancelled(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	onTask(s, w, r, p, func(id delivery.TaskID) (*delivery.Task, error) {
		return s.svc.Rentals.ConfirmShipmentCancelled(r.Context(), p, id)
	})
}

func (s *Server) receiveReturnParcel(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	onTask(s, w, r, p, func(id delivery.TaskID) (*delivery.Task, error) {
		return s.svc.Rentals.ReceiveReturnParcel(r.Context(), p, id)
	})
}

func (s *Server) closeShipment(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	type body struct {
		FollowUp delivery.FollowUp `json:"follow_up"`
		Reason   string            `json:"reason"`
		Date     string            `json:"date"`
		WindowID string            `json:"window_id"`
	}
	withTaskBody(s, w, r, p, func(id delivery.TaskID, in body) (*delivery.Task, error) {
		if len(in.Reason) > maxNote || len(in.WindowID) > maxIDLength {
			return nil, domainerr.Invalid("reason", "too long")
		}
		c := rentalsapp.CloseInput{FollowUp: in.FollowUp, Reason: in.Reason, WindowID: in.WindowID}
		if in.FollowUp == delivery.FollowUpNewShipment {
			d, err := businesstime.ParseDate(in.Date)
			if err != nil {
				return nil, domainerr.Invalid("date", "must be a date (YYYY-MM-DD)")
			}
			c.Date = d
		}
		return s.svc.Rentals.CloseShipment(r.Context(), p, id, c)
	})
}

func (s *Server) collectAtStore(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	onTask(s, w, r, p, func(id delivery.TaskID) (*delivery.Task, error) {
		return s.svc.Rentals.CollectAtStore(r.Context(), p, id)
	})
}
