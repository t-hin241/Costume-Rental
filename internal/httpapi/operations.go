package httpapi

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/backoffice"
	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identitypg"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/inventory/inventoryapp"
	"webee_rentor/internal/inventory/inventorypg"
	"webee_rentor/internal/money"
	"webee_rentor/internal/promotions"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalsapp"
	"webee_rentor/internal/support"
	"webee_rentor/internal/support/supportapp"
)

func parseDate(s, field string) (businesstime.Date, error) {
	d, err := businesstime.ParseDate(s)
	if err != nil {
		return businesstime.Date{}, domainerr.Invalid(field, "must be a date (YYYY-MM-DD)")
	}
	return d, nil
}

func (s *Server) opsOverview(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	o, err := s.svc.Backoffice.Overview(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"date": o.Date.String(),
		"counters": map[string]int{
			"pending_confirmation": o.Counts.PendingConfirmation, "to_prepare": o.Counts.ToPrepare, "handovers_today": o.Counts.HandoversToday,
			"returns_today": o.Counts.ReturnsToday, "inspections_open": o.Counts.InspectionsOpen, "overdue": o.Overdue, "conflicts": o.Conflicts,
			"assets_blocked": o.Counts.AssetsBlocked,
		},
		"queue": mapped(o.Queue, summaryOf),
	})
}

func (s *Server) opsSchedule(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	from, err := dateParam(r, "from")
	if err != nil {
		writeError(w, r, err)
		return
	}
	to, err := dateParam(r, "to")
	if err != nil {
		writeError(w, r, err)
		return
	}
	rows, err := s.svc.Backoffice.Schedule(r.Context(), p, from, to)
	if err != nil {
		writeError(w, r, err)
		return
	}
	type eventDTO struct {
		ID      string          `json:"id"`
		Kind    string          `json:"kind"`
		At      time.Time       `json:"at"`
		Mode    rentals.LegMode `json:"mode"`
		Booking bookingSummary  `json:"booking"`
	}
	writeJSON(w, http.StatusOK, mapped(rows, func(e backoffice.ScheduleEntry) eventDTO {
		b := summaryOf(e.Booking)
		if e.Handover {
			return eventDTO{ID: string(b.ID) + ":out", Kind: "HANDOVER", At: e.At, Mode: b.Outbound, Booking: b}
		}
		return eventDTO{ID: string(b.ID) + ":in", Kind: "RETURN", At: e.At, Mode: b.Return, Booking: b}
	}))
}

func (s *Server) opsAlerts(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	a, err := s.svc.Backoffice.Alerts(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	type overdueDTO struct {
		Booking      bookingSummary `json:"booking"`
		OverdueHours int            `json:"overdue_hours"`
		ItemsOut     int            `json:"items_out"`
	}
	type conflictDTO struct {
		OverdueBooking bookingSummary   `json:"overdue_booking"`
		AssetCode      string           `json:"asset_code"`
		ItemName       string           `json:"item_name"`
		Endangered     []bookingSummary `json:"endangered"`
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"overdue": mapped(a.Overdue, func(o backoffice.OverdueAlert) overdueDTO {
			return overdueDTO{Booking: summaryOf(o.Booking), OverdueHours: o.Hours, ItemsOut: o.ItemsOut}
		}),
		"conflicts": mapped(a.Conflicts, func(c backoffice.ConflictAlert) conflictDTO {
			return conflictDTO{OverdueBooking: summaryOf(c.Overdue), AssetCode: c.AssetCode, ItemName: c.ItemName, Endangered: mapped(c.Endangered, summaryOf)}
		}),
	})
}

type assetDTO struct {
	ID          inventory.AssetID       `json:"id"`
	Code        string                  `json:"code"`
	VariantID   string                  `json:"variant_id"`
	SKU         string                  `json:"sku"`
	ItemName    string                  `json:"item_name"`
	Size        string                  `json:"size"`
	Color       string                  `json:"color"`
	Status      inventory.AssetStatus   `json:"status"`
	Note        string                  `json:"note"`
	BookingCode *string                 `json:"booking_code"`
	UpdatedAt   time.Time               `json:"updated_at"`
	Actions     []inventory.AssetAction `json:"actions"`
}

func assetOf(a inventoryapp.AssetView) assetDTO {
	return assetDTO{
		ID: a.Asset.ID, Code: a.Asset.Code, VariantID: string(a.Asset.VariantID), SKU: a.SKU, ItemName: a.ItemName, Size: a.Size, Color: a.Color,
		Status: a.Asset.Status, Note: a.Note, BookingCode: jsonNull(a.BookingCode), UpdatedAt: a.Asset.StatusChangedAt, Actions: nonNil(a.Actions),
	}
}

func (s *Server) listAssets(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
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
	rows, total, err := s.svc.Inventory.Assets(r.Context(), p, inventorypg.AssetFilter{
		Term: term, Status: inventory.AssetStatus(r.URL.Query().Get("status")), Page: page, PageSize: size,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pageBody[assetDTO]{Items: mapped(rows, assetOf), Page: page, PageSize: size, Total: total})
}

func (s *Server) assetHistory(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	rows, err := s.svc.Inventory.History(r.Context(), p, inventory.AssetID(id))
	if err != nil {
		writeError(w, r, err)
		return
	}
	type entryDTO struct {
		Action string                 `json:"action"`
		From   *inventory.AssetStatus `json:"from"`
		To     inventory.AssetStatus  `json:"to"`
		At     time.Time              `json:"at"`
		Actor  string                 `json:"actor"`
		Reason string                 `json:"reason"`
	}
	writeJSON(w, http.StatusOK, mapped(rows, func(h inventoryapp.HistoryView) entryDTO {
		e := entryDTO{Action: h.Action, To: h.To, At: h.At, Actor: h.ActorName, Reason: h.Reason}
		if h.From != "" {
			e.From = &h.From
		}
		return e
	}))
}

func (s *Server) changeAssetStatus(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	in, err := decodeSmall[struct {
		Action             inventory.AssetAction `json:"action"`
		Reason             string                `json:"reason"`
		ChecklistCompleted bool                  `json:"checklist_completed"`
	}](w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if utf8.RuneCountInString(in.Reason) > maxReasonLength {
		writeError(w, r, domainerr.Invalid("reason", "at most 500 characters"))
		return
	}
	a, err := s.svc.Inventory.ChangeStatus(r.Context(), p, inventory.AssetID(id), inventoryapp.StatusInput{
		Action: in.Action, Reason: in.Reason, Checklist: inventory.InspectionChecklist{Completed: in.ChecklistCompleted, Notes: in.Reason},
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, assetOf(inventoryapp.AssetView{AssetRow: inventorypg.AssetRow{Asset: a}}))
}

func (s *Server) registerAsset(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	in, err := decodeSmall[struct {
		Code      string `json:"code"`
		VariantID string `json:"variant_id"`
	}](w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if len(in.Code) > maxIDLength || len(in.VariantID) > maxIDLength {
		writeError(w, r, domainerr.Invalid("code", "too long"))
		return
	}
	a, err := s.svc.Inventory.RegisterByRef(r.Context(), p, strings.TrimSpace(in.Code), strings.TrimSpace(in.VariantID))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, assetOf(inventoryapp.AssetView{AssetRow: inventorypg.AssetRow{Asset: a}}))
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	c, err := s.identity.Profile(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"email": c.Email, "full_name": c.Name, "phone": c.Phone})
}

func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	in, err := decodeSmall[struct {
		FullName string `json:"full_name"`
		Phone    string `json:"phone"`
	}](w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	c, err := s.identity.SaveProfile(r.Context(), p, in.FullName, in.Phone)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"email": c.Email, "full_name": c.Name, "phone": c.Phone})
}

type supportMessageDTO struct {
	From   support.Sender `json:"from"`
	Author string         `json:"author"`
	Body   string         `json:"body"`
	At     time.Time      `json:"at"`
}

type supportDTO struct {
	ID           string              `json:"id"`
	Code         string              `json:"code"`
	Topic        support.Topic       `json:"topic"`
	Subject      string              `json:"subject"`
	Status       support.Status      `json:"status"`
	BookingCode  *string             `json:"booking_code"`
	CustomerName string              `json:"customer_name"`
	Messages     []supportMessageDTO `json:"messages"`
	CreatedAt    time.Time           `json:"created_at"`
}

func supportOf(v supportapp.View) supportDTO {
	messages := make([]supportMessageDTO, len(v.Messages))
	for i, m := range v.Messages {
		messages[i] = supportMessageDTO{From: m.Sender, Author: v.Authors[i], Body: m.Body, At: m.At}
	}
	return supportDTO{ID: v.ID, Code: v.Code, Topic: v.Topic, Subject: v.Subject, Status: v.Status, BookingCode: jsonNull(v.BookingCode),
		CustomerName: v.CustomerName, Messages: messages, CreatedAt: v.CreatedAt}
}

func (s *Server) mySupportRequests(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	all, err := s.svc.Support.Mine(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapped(all, supportOf))
}

func (s *Server) createSupportRequest(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	in, err := decodeSmall[struct {
		Topic       support.Topic `json:"topic"`
		Subject     string        `json:"subject"`
		BookingCode string        `json:"booking_code"`
		Message     string        `json:"message"`
	}](w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.svc.Support.Create(r.Context(), p, supportapp.CreateInput{
		Topic: in.Topic, Subject: in.Subject, BookingCode: strings.TrimSpace(in.BookingCode), Message: in.Message,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, supportOf(v))
}

func (s *Server) supportRequests(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	all, err := s.svc.Support.All(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapped(all, supportOf))
}

func (s *Server) replySupportRequest(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	in, err := decodeSmall[struct {
		Message string `json:"message"`
		Resolve bool   `json:"resolve"`
	}](w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.svc.Support.Reply(r.Context(), p, id, in.Message, in.Resolve)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, supportOf(v))
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	days := 30
	if v := r.URL.Query().Get("range_days"); v != "" {
		n, err := parsePositive(v, "range_days")
		if err != nil {
			writeError(w, r, err)
			return
		}
		days = n
	}
	d, err := s.svc.Backoffice.Dashboard(r.Context(), p, days)
	if err != nil {
		writeError(w, r, err)
		return
	}
	type kpiDTO struct {
		Key           string `json:"key"`
		Value         int64  `json:"value"`
		ChangePercent *int64 `json:"change_percent"`
	}
	type dayDTO struct {
		Date    string   `json:"date"`
		Revenue moneyDTO `json:"revenue"`
	}
	type countDTO struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
	}
	type topDTO struct {
		Name     string   `json:"name"`
		Bookings int      `json:"bookings"`
		Revenue  moneyDTO `json:"revenue"`
	}
	count := func(c backoffice.StatusCount) countDTO { return countDTO{Status: c.Status, Count: c.Count} }
	writeJSON(w, http.StatusOK, map[string]any{
		"range_days":              d.Days,
		"revenue_recognized_note": "Doanh thu ghi nhận khi đơn hoàn tất hoặc khi quyết toán hủy, không gồm tiền bảo đảm và tiền giữ lịch.",
		"kpis": mapped(d.KPIs, func(k backoffice.KPI) kpiDTO {
			return kpiDTO{Key: k.Key, Value: k.Value, ChangePercent: k.Change}
		}),
		"revenue_series": mapped(d.Revenue, func(x backoffice.DayRevenue) dayDTO {
			return dayDTO{Date: x.Date.String(), Revenue: moneyOf(x.Revenue)}
		}),
		"bookings_by_status": mapped(d.Bookings, count), "inventory_by_status": mapped(d.Inventory, count),
		"top_offerings": mapped(d.Top, func(t backoffice.TopOffering) topDTO {
			return topDTO{Name: t.Name, Bookings: t.Bookings, Revenue: moneyOf(t.Revenue)}
		}),
	})
}

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
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
	rows, total, err := s.identity.Users(r.Context(), p, identitypg.UserFilter{Term: term, Role: identity.Role(r.URL.Query().Get("role")), Page: page, PageSize: size})
	if err != nil {
		writeError(w, r, err)
		return
	}
	type userDTO struct {
		userResponse
		FullName   string     `json:"full_name"`
		Phone      string     `json:"phone"`
		MFAEnabled bool       `json:"mfa_enabled"`
		LastSeenAt *time.Time `json:"last_seen_at"`
	}
	writeJSON(w, http.StatusOK, pageBody[userDTO]{Page: page, PageSize: size, Total: total, Items: mapped(rows, func(u identitypg.UserRow) userDTO {
		return userDTO{
			userResponse: userResponse{ID: u.User.ID, Email: u.User.Email, Status: u.User.Status, Roles: nonNil(u.User.Roles), CreatedAt: u.User.CreatedAt},
			FullName:     u.Contact.Name, Phone: u.Contact.Phone, MFAEnabled: u.MFAEnabled, LastSeenAt: u.LastSeenAt,
		}
	})})
}

func (s *Server) auditEvents(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
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
	res, err := s.svc.Backoffice.Audit(r.Context(), p, auditpg.Filter{Term: term, Result: audit.Result(r.URL.Query().Get("result")), Page: page, PageSize: size})
	if err != nil {
		writeError(w, r, err)
		return
	}
	type eventDTO struct {
		ID           string            `json:"id"`
		OccurredAt   time.Time         `json:"occurred_at"`
		Actor        string            `json:"actor"`
		Action       string            `json:"action"`
		ResourceType string            `json:"resource_type"`
		ResourceID   string            `json:"resource_id"`
		Result       audit.Result      `json:"result"`
		Reason       string            `json:"reason"`
		Changes      map[string]string `json:"changes"`
	}
	writeJSON(w, http.StatusOK, pageBody[eventDTO]{Page: page, PageSize: size, Total: res.Total, Items: mapped(res.Events, func(e audit.Event) eventDTO {
		actor := "Hệ thống"
		if name, ok := res.Actors[e.Actor.ID]; ok {
			actor = name
		}
		changes := make(map[string]string, len(e.Changes))
		for k, c := range e.Changes {
			changes[k] = c.From + " → " + c.To
		}
		return eventDTO{ID: e.ID, OccurredAt: e.OccurredAt, Actor: actor, Action: e.Action, ResourceType: e.ResourceType, ResourceID: e.ResourceID, Result: e.Result, Reason: e.Reason, Changes: changes}
	})})
}

type byClassDTO struct {
	Class            string `json:"class"`
	PreparationHours int    `json:"preparation_hours"`
	TurnaroundHours  int    `json:"turnaround_hours"`
}

type policyDTO struct {
	Version     string    `json:"version"`
	PublishedAt time.Time `json:"published_at"`
	PublishedBy string    `json:"published_by"`
	Booking     struct {
		HoldMinutes           int `json:"hold_minutes"`
		MinLeadHours          int `json:"min_lead_hours"`
		MaxAdvanceHours       int `json:"max_advance_hours"`
		MaxLinesPerBooking    int `json:"max_lines_per_booking"`
		MaxQuantityPerLine    int `json:"max_quantity_per_line"`
		RescheduleCutoffHours int `json:"reschedule_cutoff_hours"`
	} `json:"booking"`
	Confirmation struct {
		Mode          rentals.ConfirmMode          `json:"mode"`
		Funds         rentals.FundsRequirementKind `json:"funds"`
		HandoverFunds rentals.FundsRequirementKind `json:"handover_funds"`
	} `json:"confirmation"`
	Reservation reservationDTO `json:"reservation"`
	Buffers     struct {
		DefaultPreparationHours int          `json:"default_preparation_hours"`
		DefaultTurnaroundHours  int          `json:"default_turnaround_hours"`
		ByClass                 []byClassDTO `json:"by_class"`
	} `json:"buffers"`
	Cancellation          cancellationDTO `json:"cancellation"`
	LateFee               lateFeeDTO      `json:"late_fee"`
	LossVerificationHours int             `json:"loss_verification_hours"`
	FeeApprovalAbove      moneyDTO        `json:"fee_approval_above"`
	Modification          struct {
		SelfServiceCutoffHours int `json:"self_service_cutoff_hours"`
		SelfServiceSizeChanges int `json:"self_service_size_changes"`
		HoldMinutes            int `json:"hold_minutes"`
	} `json:"modification"`
}

func policyOf(v rentalsapp.PolicyVersion) policyDTO {
	p := v.Policy
	var d policyDTO
	d.Version, d.PublishedAt, d.PublishedBy = p.Version, v.PublishedAt, v.PublishedBy
	d.Booking.HoldMinutes, d.Booking.MinLeadHours, d.Booking.MaxAdvanceHours = minutes(p.HoldTTL), hours(p.MinLeadTime), hours(p.MaxAdvance)
	d.Booking.MaxLinesPerBooking, d.Booking.MaxQuantityPerLine = p.MaxLinesPerBooking, p.MaxQuantityPerLine
	d.Booking.RescheduleCutoffHours = hours(p.CustomerRescheduleCutoff)
	d.Confirmation.Mode, d.Confirmation.Funds, d.Confirmation.HandoverFunds = p.Confirmation.Mode, p.Confirmation.Funds.Kind, p.Handover.Kind
	d.Reservation = reservationOf(p.Reservation)
	d.Buffers.DefaultPreparationHours, d.Buffers.DefaultTurnaroundHours = hours(p.Buffers.Default.Preparation), hours(p.Buffers.Default.Turnaround)
	d.Buffers.ByClass = []byClassDTO{}
	for class, b := range p.Buffers.ByClass {
		d.Buffers.ByClass = append(d.Buffers.ByClass, byClassDTO{Class: class, PreparationHours: hours(b.Preparation), TurnaroundHours: hours(b.Turnaround)})
	}
	d.Cancellation, d.LateFee = cancellationOf(p.Cancellation), lateFeeOf(p.LateFee)
	d.LossVerificationHours, d.FeeApprovalAbove = hours(p.Loss.VerificationWindow), moneyOf(p.FeeApproval.ManagerApprovalAbove)
	d.Modification.SelfServiceCutoffHours, d.Modification.SelfServiceSizeChanges = hours(p.Modification.SelfServiceCutoff), p.Modification.SelfServiceSizeChanges
	d.Modification.HoldMinutes = minutes(p.Modification.HoldTTL)
	return d
}

// applyPolicy copies the fields the editing screen shows onto the active
// policy, so rules it does not show are kept as they are.
func applyPolicy(p rentals.Policy, d policyDTO) (rentals.Policy, error) {
	h := func(n int) time.Duration { return time.Duration(n) * time.Hour }
	m := func(n int) time.Duration { return time.Duration(n) * time.Minute }
	p.Version = strings.TrimSpace(d.Version)
	p.HoldTTL, p.MinLeadTime, p.MaxAdvance = m(d.Booking.HoldMinutes), h(d.Booking.MinLeadHours), h(d.Booking.MaxAdvanceHours)
	p.MaxLinesPerBooking, p.MaxQuantityPerLine = d.Booking.MaxLinesPerBooking, d.Booking.MaxQuantityPerLine
	p.CustomerRescheduleCutoff = h(d.Booking.RescheduleCutoffHours)
	p.Confirmation = rentals.ConfirmationRule{Mode: d.Confirmation.Mode, Funds: rentals.FundsRequirement{Kind: d.Confirmation.Funds}}
	p.Handover = rentals.FundsRequirement{Kind: d.Confirmation.HandoverFunds}
	p.Reservation = rentals.ReservationPolicy{Kind: d.Reservation.Kind, Treatment: d.Reservation.Treatment, Amount: money.Zero(money.VND)}
	if d.Reservation.RentPercent != nil {
		p.Reservation.RentPercent = *d.Reservation.RentPercent
	}
	if d.Reservation.Amount != nil {
		v, err := d.Reservation.Amount.value("reservation.amount")
		if err != nil {
			return rentals.Policy{}, err
		}
		p.Reservation.Amount = v
	}
	p.Buffers.Default = rentals.Buffer{Preparation: h(d.Buffers.DefaultPreparationHours), Turnaround: h(d.Buffers.DefaultTurnaroundHours)}
	p.Buffers.ByClass = make(map[string]rentals.Buffer, len(d.Buffers.ByClass))
	for _, b := range d.Buffers.ByClass {
		p.Buffers.ByClass[b.Class] = rentals.Buffer{Preparation: h(b.PreparationHours), Turnaround: h(b.TurnaroundHours)}
	}
	p.Cancellation.Tiers = mapped(d.Cancellation.Tiers, func(t tierDTO) rentals.FeeTier {
		return rentals.FeeTier{MinNotice: h(t.MinNoticeHours), FeePercent: t.FeePercent}
	})
	p.Cancellation.NoShowAfter, p.Cancellation.NoShowFeePercent = h(d.Cancellation.NoShowAfterHours), d.Cancellation.NoShowFeePercent
	p.LateFee.Grace, p.LateFee.ExtraPeriod, p.LateFee.ExtraPercent = m(d.LateFee.GraceMinutes), h(d.LateFee.ExtraPeriodHours), d.LateFee.ExtraPercent
	p.LateFee.Tiers = mapped(d.LateFee.Tiers, func(t lateTierDTO) rentals.LateFeeTier {
		return rentals.LateFeeTier{UpTo: h(t.UpToHours), Percent: t.Percent}
	})
	p.Loss.VerificationWindow = h(d.LossVerificationHours)
	above, err := d.FeeApprovalAbove.value("fee_approval_above")
	if err != nil {
		return rentals.Policy{}, err
	}
	p.FeeApproval.ManagerApprovalAbove = above
	p.Modification = rentals.ModificationPolicy{
		SelfServiceCutoff: h(d.Modification.SelfServiceCutoffHours), SelfServiceSizeChanges: d.Modification.SelfServiceSizeChanges, HoldTTL: m(d.Modification.HoldMinutes),
	}
	return p, nil
}

func (s *Server) activePolicy(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	v, err := s.svc.Rentals.PolicyForEdit(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, policyOf(v))
}

func (s *Server) publishPolicy(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	in, err := decodeSmall[struct {
		Policy policyDTO `json:"policy"`
		Reason string    `json:"reason"`
	}](w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if utf8.RuneCountInString(in.Reason) > maxReasonLength {
		writeError(w, r, domainerr.Invalid("reason", "at most 500 characters"))
		return
	}
	current, err := s.svc.Rentals.PolicyForEdit(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	next, err := applyPolicy(current.Policy, in.Policy)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.svc.Rentals.PublishPolicy(r.Context(), p, next, strings.TrimSpace(in.Reason)); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.svc.Rentals.PolicyForEdit(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, policyOf(v))
}

type campaignDTO struct {
	ID         promotions.CampaignID `json:"id"`
	Code       string                `json:"code"`
	Name       string                `json:"name"`
	Type       promotions.Type       `json:"type"`
	Status     promotions.Status     `json:"status"`
	ValueType  promotions.ValueType  `json:"value_type"`
	Percent    *int                  `json:"percent"`
	Amount     *moneyDTO             `json:"amount"`
	ValidFrom  time.Time             `json:"valid_from"`
	ValidUntil time.Time             `json:"valid_until"`
	Combinable bool                  `json:"combinable"`
	Used       int                   `json:"used"`
	UsageLimit *int                  `json:"usage_limit"`
}

func campaignOf(c backoffice.CampaignView) campaignDTO {
	// The stored end is exclusive; the last valid day is what people think in.
	out := campaignDTO{ID: c.ID, Code: c.Code, Name: c.Name, Type: c.Type, Status: c.Status, ValueType: c.ValueType, ValidFrom: c.ValidFrom,
		ValidUntil: c.ValidUntil.Add(-time.Second), Combinable: c.Combinable, Used: c.Used}
	if c.ValueType == promotions.ValuePercent {
		out.Percent = &c.Percent
	} else {
		m := moneyOf(c.Amount)
		out.Amount = &m
	}
	if c.UsageLimitTotal > 0 {
		out.UsageLimit = &c.UsageLimitTotal
	}
	return out
}

func (s *Server) campaigns(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	all, err := s.svc.Backoffice.Campaigns(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapped(all, campaignOf))
}

func (s *Server) saveCampaign(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	id, err := optionalPath(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	in, err := decodeSmall[struct {
		Code       string               `json:"code"`
		Name       string               `json:"name"`
		Type       promotions.Type      `json:"type"`
		Status     promotions.Status    `json:"status"`
		ValueType  promotions.ValueType `json:"value_type"`
		Percent    *int                 `json:"percent"`
		Amount     *moneyDTO            `json:"amount"`
		ValidFrom  string               `json:"valid_from"`
		ValidUntil string               `json:"valid_until"`
		Combinable bool                 `json:"combinable"`
		UsageLimit *int                 `json:"usage_limit"`
	}](w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	from, err := s.dayStart(in.ValidFrom, "valid_from")
	if err != nil {
		writeError(w, r, err)
		return
	}
	last, err := s.dayStart(in.ValidUntil, "valid_until")
	if err != nil {
		writeError(w, r, err)
		return
	}
	c := promotions.Campaign{
		Code: in.Code, Name: in.Name, Type: in.Type, Status: in.Status, ValueType: in.ValueType, ValidFrom: from, ValidUntil: last.AddDate(0, 0, 1), Combinable: in.Combinable,
	}
	if in.Type != promotions.TypeCoupon {
		c.Code = ""
	}
	if in.Percent != nil {
		c.Percent = *in.Percent
	}
	if in.Amount != nil {
		if c.Amount, err = in.Amount.value("amount"); err != nil {
			writeError(w, r, err)
			return
		}
	}
	if in.UsageLimit != nil {
		c.UsageLimitTotal = *in.UsageLimit
	}
	saved, err := s.svc.Backoffice.SaveCampaign(r.Context(), p, promotions.CampaignID(id), c)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, savedStatus(id), campaignOf(saved))
}

// dayStart reads a business date as its first instant.
func (s *Server) dayStart(v, field string) (time.Time, error) {
	d, err := parseDate(v, field)
	if err != nil {
		return time.Time{}, err
	}
	return businesstime.At(d, businesstime.TimeOfDay{}, s.svc.Rentals.Location()), nil
}

func parsePositive(v, field string) (int, error) {
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' || n > 100_000 {
			return 0, domainerr.Invalid(field, "must be a positive number")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
