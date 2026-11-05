package httpapi

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/payments/paymentspg"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/tax"
	"webee_rentor/internal/tax/taxapp"
	"webee_rentor/internal/tax/taxpg"
)

// Decision D41: the tax profile, the revenue ledger and the finance reports.
// No endpoint computes tax.

type taxProfileDTO struct {
	Version                string     `json:"version"`
	TaxpayerType           string     `json:"taxpayer_type"`
	AnnualRevenueThreshold moneyDTO   `json:"annual_revenue_threshold"`
	ThresholdEffectiveFrom string     `json:"threshold_effective_from"`
	WarningPercents        []int      `json:"warning_percents"`
	EffectiveFrom          time.Time  `json:"effective_from"`
	EffectiveUntil         *time.Time `json:"effective_until"`
}

func taxProfileOf(p tax.Profile) taxProfileDTO {
	return taxProfileDTO{
		Version: p.Version, TaxpayerType: string(p.TaxpayerType), AnnualRevenueThreshold: moneyOf(p.AnnualRevenueThreshold),
		ThresholdEffectiveFrom: p.ThresholdEffectiveFrom.String(), WarningPercents: nonNil(p.WarningPercents),
		EffectiveFrom: p.EffectiveFrom.UTC(), EffectiveUntil: timePtr(p.EffectiveUntil),
	}
}

func (s *Server) taxProfiles(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	h, err := s.svc.Tax.Profiles(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": mapped(h.Profiles, taxProfileOf)})
}

type taxProfileRequest struct {
	Version                string   `json:"version"`
	AnnualRevenueThreshold moneyDTO `json:"annual_revenue_threshold"`
	ThresholdEffectiveFrom string   `json:"threshold_effective_from"`
	WarningPercents        []int    `json:"warning_percents"`
	EffectiveFrom          string   `json:"effective_from"`
}

func (s *Server) addTaxProfile(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	req, err := decodeSmall[taxProfileRequest](w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	threshold, err := req.AnnualRevenueThreshold.value("annual_revenue_threshold")
	if err != nil {
		writeError(w, r, err)
		return
	}
	thresholdFrom, err := businesstime.ParseDate(req.ThresholdEffectiveFrom)
	if err != nil {
		writeError(w, r, domainerr.Invalid("threshold_effective_from", "must be a date (YYYY-MM-DD)"))
		return
	}
	from, err := businesstime.ParseDate(req.EffectiveFrom)
	if err != nil {
		writeError(w, r, domainerr.Invalid("effective_from", "must be a date (YYYY-MM-DD)"))
		return
	}
	prof, err := s.svc.Tax.AddProfile(r.Context(), p, taxapp.ProfileInput{
		Version: strings.TrimSpace(req.Version), AnnualRevenueThreshold: threshold, ThresholdEffectiveFrom: thresholdFrom,
		WarningPercents: req.WarningPercents, EffectiveFrom: from,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, taxProfileOf(prof))
}

type categoryAmountDTO struct {
	Category tax.Category `json:"category"`
	Amount   moneyDTO     `json:"amount"`
}

type yearDTO struct {
	Year            int                 `json:"year"`
	HasProfile      bool                `json:"has_profile"`
	PendingBookings int                 `json:"pending_bookings"`
	ProfileVersion  *string             `json:"profile_version"`
	List            *moneyDTO           `json:"list"`
	Discount        *moneyDTO           `json:"discount"`
	Revenue         *moneyDTO           `json:"revenue"`
	Threshold       *moneyDTO           `json:"threshold"`
	Percent         int                 `json:"percent"`
	Level           tax.Level           `json:"level"`
	Mark            int                 `json:"mark"`
	Marks           []int               `json:"marks"`
	ByCategory      []categoryAmountDTO `json:"by_category"`
}

func (s *Server) revenueYear(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	year := 0
	if v := r.URL.Query().Get("year"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, r, domainerr.Invalid("year", "must be a year"))
			return
		}
		year = n
	}
	v, err := s.svc.Tax.Year(r.Context(), p, year)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := yearDTO{Year: v.Year, PendingBookings: v.Pending, Level: tax.LevelBelow, Marks: []int{}, ByCategory: []categoryAmountDTO{}}
	if sum := v.Summary; sum != nil {
		list, discount, revenue, threshold := moneyOf(sum.List), moneyOf(sum.Discount), moneyOf(sum.Revenue), moneyOf(sum.Threshold)
		out.HasProfile, out.ProfileVersion = true, &sum.ProfileVersion
		out.List, out.Discount, out.Revenue, out.Threshold = &list, &discount, &revenue, &threshold
		out.Percent, out.Level, out.Mark, out.Marks = sum.Percent, sum.Level, sum.Mark, nonNil(sum.Marks)
		for _, c := range tax.RevenueCategories {
			if m, ok := sum.ByCategory[c]; ok {
				out.ByCategory = append(out.ByCategory, categoryAmountDTO{Category: c, Amount: moneyOf(m)})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func periodParams(r *http.Request) (taxapp.Period, error) {
	from, err := dateParam(r, "from")
	if err != nil {
		return taxapp.Period{}, err
	}
	to, err := dateParam(r, "to")
	if err != nil {
		return taxapp.Period{}, err
	}
	return taxapp.Period{From: from, To: to}, nil
}

type reportRowDTO struct {
	Period   string       `json:"period"`
	Category tax.Category `json:"category"`
	Entries  int          `json:"entries"`
	List     moneyDTO     `json:"list"`
	Discount moneyDTO     `json:"discount"`
	Net      moneyDTO     `json:"net"`
}

func (s *Server) revenueReport(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	per, err := periodParams(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	group := taxpg.Group(r.URL.Query().Get("group"))
	if group == "" {
		group = taxpg.GroupMonth
	}
	rows, err := s.svc.Tax.Report(r.Context(), p, per, group)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group": group, "rows": mapped(rows, func(v taxpg.ReportRow) reportRowDTO {
		return reportRowDTO{Period: v.Period.String(), Category: v.Category, Entries: v.Entries,
			List: moneyOf(v.List), Discount: moneyOf(v.Discount), Net: moneyOf(v.Net)}
	})})
}

func entryQuery(r *http.Request) (taxapp.EntryQuery, error) {
	per, err := periodParams(r)
	if err != nil {
		return taxapp.EntryQuery{}, err
	}
	booking, err := textParam(r, "booking")
	if err != nil {
		return taxapp.EntryQuery{}, err
	}
	q := taxapp.EntryQuery{Period: per, Booking: booking, Source: tax.Source(r.URL.Query().Get("source"))}
	if c := r.URL.Query().Get("category"); c != "" {
		if !validCategory(tax.Category(c)) {
			return taxapp.EntryQuery{}, domainerr.Invalid("category", "unknown revenue category")
		}
		q.Category = tax.Category(c)
	}
	return q, nil
}

func validCategory(c tax.Category) bool {
	for _, v := range tax.RevenueCategories {
		if v == c {
			return true
		}
	}
	return false
}

type revenueEntryDTO struct {
	ID             tax.EntryID      `json:"id"`
	Source         tax.Source       `json:"source"`
	BookingID      *string          `json:"booking_id"`
	BookingCode    *string          `json:"booking_code"`
	Category       tax.Category     `json:"category"`
	List           moneyDTO         `json:"list"`
	Discount       moneyDTO         `json:"discount"`
	Net            moneyDTO         `json:"net"`
	RecognizedAt   time.Time        `json:"recognized_at"`
	ProfileVersion string           `json:"profile_version"`
	Reverses       *string          `json:"reverses"`
	Reason         *string          `json:"reason"`
	Channel        *string          `json:"channel"`
	Note           *string          `json:"note"`
	DocumentRef    *string          `json:"document_ref"`
	RecordedBy     *identity.UserID `json:"recorded_by"`
}

func revenueEntryOf(e tax.RevenueEntry, code string) revenueEntryDTO {
	d := revenueEntryDTO{
		ID: e.ID, Source: e.Source, BookingID: jsonNull(e.BookingRef), BookingCode: jsonNull(code), Category: e.Category,
		List: moneyOf(e.List), Discount: moneyOf(e.Discount), Net: moneyOf(e.Net), RecognizedAt: e.RecognizedAt.UTC(),
		ProfileVersion: e.ProfileVersion, Reverses: jsonNull(string(e.Reverses)), Reason: jsonNull(e.Reason),
		Channel: jsonNull(e.Channel), Note: jsonNull(e.Note), DocumentRef: jsonNull(e.DocumentRef),
	}
	if e.RecordedBy != nil && e.RecordedBy.IsUser() {
		id := identity.UserID(e.RecordedBy.ID)
		d.RecordedBy = &id
	}
	return d
}

func (s *Server) revenueEntries(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	q, err := entryQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, size, err := pageParams(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rows, total, err := s.svc.Tax.Entries(r.Context(), p, q, page, size)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pageBody[revenueEntryDTO]{Page: page, PageSize: size, Total: total,
		Items: mapped(rows, func(v taxpg.EntryRow) revenueEntryDTO { return revenueEntryOf(v.RevenueEntry, v.BookingCode) })})
}

// csvText keeps a spreadsheet from running text that looks like a formula.
func csvText(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

// exportRevenue sends the entries of the query as CSV (UTF-8 with a byte
// order mark, so spreadsheets read Vietnamese text). It is built in memory,
// bounded by the export limit, so an error still gets a proper response.
func (s *Server) exportRevenue(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	q, err := entryQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	loc := s.svc.Tax.Location()
	var buf bytes.Buffer
	buf.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(&buf)
	_ = cw.Write([]string{"recognized_date", "recognized_at", "entry_id", "source", "booking_code", "category",
		"list_amount", "discount", "net", "currency", "profile_version", "reverses", "reason", "channel", "note", "document_ref"})
	err = s.svc.Tax.ExportEntries(r.Context(), p, q, func(v taxpg.EntryRow) error {
		e := v.RevenueEntry
		return cw.Write([]string{
			businesstime.DateOf(e.RecognizedAt, loc).String(), e.RecognizedAt.In(loc).Format(time.RFC3339), string(e.ID),
			string(e.Source), v.BookingCode, string(e.Category), strconv.FormatInt(e.List.Amount(), 10),
			strconv.FormatInt(e.Discount.Amount(), 10), strconv.FormatInt(e.Net.Amount(), 10), string(e.Net.Currency()),
			e.ProfileVersion, string(e.Reverses), csvText(e.Reason), csvText(e.Channel), csvText(e.Note), csvText(e.DocumentRef),
		})
	})
	if err == nil {
		cw.Flush()
		err = cw.Error()
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="doanh-thu_%s_%s.csv"`, q.From, q.To))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

type manualEntryRequest struct {
	Category    tax.Category `json:"category"`
	Amount      moneyDTO     `json:"amount"`
	Date        string       `json:"date"`
	Channel     string       `json:"channel"`
	Note        string       `json:"note"`
	DocumentRef string       `json:"document_ref"`
}

func (s *Server) addRevenueEntry(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	req, err := decodeSmall[manualEntryRequest](w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	amount, err := req.Amount.value("amount")
	if err != nil {
		writeError(w, r, err)
		return
	}
	date, err := businesstime.ParseDate(req.Date)
	if err != nil {
		writeError(w, r, domainerr.Invalid("date", "must be a date (YYYY-MM-DD)"))
		return
	}
	e, err := s.svc.Tax.AddManualEntry(r.Context(), p, tax.ManualInput{
		Category: req.Category, Amount: amount, Date: date, Channel: req.Channel, Note: req.Note, DocumentRef: req.DocumentRef,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, revenueEntryOf(e, ""))
}

func (s *Server) reverseRevenueEntry(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	reason, err := decodeReason(w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	e, err := s.svc.Tax.ReverseEntry(r.Context(), p, tax.EntryID(id), reason)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, revenueEntryOf(e, ""))
}

type movementDTO struct {
	Method payments.Method `json:"method"`
	Bucket payments.Bucket `json:"bucket"`
	Count  int             `json:"count"`
	Amount moneyDTO        `json:"amount"`
}

func movementsOf(ms []paymentspg.Movement) []movementDTO {
	return nonNil(mapped(ms, func(m paymentspg.Movement) movementDTO {
		return movementDTO{Method: m.Method, Bucket: m.Bucket, Count: m.Count, Amount: moneyOf(m.Amount)}
	}))
}

func (s *Server) cashFlow(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	per, err := periodParams(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	c, err := s.svc.Tax.CashFlow(r.Context(), p, per)
	if err != nil {
		writeError(w, r, err)
		return
	}
	pos := c.Position
	writeJSON(w, http.StatusOK, map[string]any{
		"received": movementsOf(c.Received), "refunded": movementsOf(c.Refunded),
		"position": map[string]any{
			"security_held": moneyOf(pos.SecurityHeld), "reservation_held": moneyOf(pos.ReservationHeld),
			"owed_ended": moneyOf(pos.OwedEnded), "owed_ended_bookings": pos.OwedEndedCount, "due_open": moneyOf(pos.DueOpen),
			"pending_refunds": moneyOf(pos.PendingRefunds), "pending_payments": moneyOf(pos.PendingPayments),
			"overpaid": moneyOf(pos.Overpaid),
		},
	})
}

type reconciliationDTO struct {
	ID           string          `json:"id"`
	StatementRef string          `json:"statement_ref"`
	Note         *string         `json:"note"`
	By           identity.UserID `json:"by"`
	At           time.Time       `json:"at"`
}

type txnDTO struct {
	Kind           payments.TxnKind   `json:"kind"`
	ID             string             `json:"id"`
	BookingID      string             `json:"booking_id"`
	BookingCode    string             `json:"booking_code"`
	Method         payments.Method    `json:"method"`
	Reference      *string            `json:"reference"`
	Amount         moneyDTO           `json:"amount"`
	Buckets        []payments.Bucket  `json:"buckets"`
	At             time.Time          `json:"at"`
	Reconciliation *reconciliationDTO `json:"reconciliation"`
}

func txnOf(t paymentspg.Txn) txnDTO {
	d := txnDTO{Kind: t.Kind, ID: t.ID, BookingID: t.BookingRef, BookingCode: t.BookingCode, Method: t.Method,
		Reference: jsonNull(t.Reference), Amount: moneyOf(t.Amount), Buckets: nonNil(t.Buckets), At: t.At}
	if c := t.Reconciliation; c != nil {
		d.Reconciliation = &reconciliationDTO{ID: c.ID, StatementRef: c.StatementRef, Note: jsonNull(c.Note), By: identity.UserID(c.By.ID), At: c.At}
	}
	return d
}

func txnKind(v string) (payments.TxnKind, error) {
	switch strings.ToUpper(v) {
	case "":
		return "", nil
	case string(payments.TxnPayment):
		return payments.TxnPayment, nil
	case string(payments.TxnRefund):
		return payments.TxnRefund, nil
	}
	return "", domainerr.Invalid("kind", "must be payment or refund")
}

func (s *Server) transactions(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	per, err := periodParams(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	kind, err := txnKind(r.URL.Query().Get("kind"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	var reconciled *bool
	switch v := r.URL.Query().Get("reconciled"); v {
	case "":
	case "true", "false":
		b := v == "true"
		reconciled = &b
	default:
		writeError(w, r, domainerr.Invalid("reconciled", "must be true or false"))
		return
	}
	page, size, err := pageParams(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rows, total, err := s.svc.Tax.Transactions(r.Context(), p, per, kind, reconciled, page, size)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pageBody[txnDTO]{Page: page, PageSize: size, Total: total, Items: mapped(rows, txnOf)})
}

type reconcileRequest struct {
	StatementRef string `json:"statement_ref"`
	Note         string `json:"note"`
}

func (s *Server) reconcileTransaction(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	kind, err := txnKind(r.PathValue("kind"))
	if err != nil || kind == "" {
		writeError(w, r, domainerr.Invalid("kind", "must be payment or refund"))
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	req, err := decodeSmall[reconcileRequest](w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	c, err := s.svc.Tax.Reconcile(r.Context(), p, kind, id, req.StatementRef, req.Note)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, reconciliationDTO{ID: c.ID, StatementRef: c.StatementRef, Note: jsonNull(c.Note), By: identity.UserID(c.By.ID), At: c.At})
}

func (s *Server) revokeReconciliation(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	reason, err := decodeReason(w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.svc.Tax.RevokeReconciliation(r.Context(), p, id, reason); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type bookingCheckDTO struct {
	BookingID   rentals.BookingID `json:"booking_id"`
	BookingCode string            `json:"booking_code"`
	Status      rentals.Status    `json:"status"`
	EndedAt     time.Time         `json:"ended_at"`
	Charged     moneyDTO          `json:"charged"`
	Recognised  moneyDTO          `json:"recognised"`
	Covered     moneyDTO          `json:"covered"`
	Mismatches  []taxapp.Mismatch `json:"mismatches"`
}

type attentionDTO struct {
	Kind        payments.TxnKind `json:"kind"`
	ID          string           `json:"id"`
	BookingID   string           `json:"booking_id"`
	BookingCode string           `json:"booking_code"`
	Amount      moneyDTO         `json:"amount"`
	Status      string           `json:"status"`
	Reason      *string          `json:"reason"`
	At          time.Time        `json:"at"`
}

type pendingDTO struct {
	BookingID   rentals.BookingID `json:"booking_id"`
	BookingCode string            `json:"booking_code"`
	EndedAt     time.Time         `json:"ended_at"`
}

type sectionDTO[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

func sectionOf[V, T any](s taxapp.Section[V], f func(V) T) sectionDTO[T] {
	return sectionDTO[T]{Items: nonNil(mapped(s.Items, f)), Total: s.Total}
}

func (s *Server) reconciliation(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	per, err := periodParams(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rep, err := s.svc.Tax.Reconciliation(r.Context(), p, per)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recognised": moneyOf(rep.Recognised), "received": moneyOf(rep.Received), "refunded": moneyOf(rep.Refunded),
		"ended_bookings": rep.EndedBookings,
		"mismatched": sectionOf(rep.Mismatched, func(c taxapp.BookingCheck) bookingCheckDTO {
			return bookingCheckDTO{BookingID: c.ID, BookingCode: c.Code, Status: c.Status, EndedAt: c.EndedAt,
				Charged: moneyOf(c.Charged), Recognised: moneyOf(c.Recognised), Covered: moneyOf(c.Covered), Mismatches: c.Mismatches}
		}),
		"unreconciled": sectionOf(rep.Unreconciled, txnOf),
		"attention": sectionOf(rep.Attention, func(a paymentspg.Attention) attentionDTO {
			return attentionDTO{Kind: a.Kind, ID: a.ID, BookingID: a.BookingRef, BookingCode: a.BookingCode, Amount: moneyOf(a.Amount),
				Status: a.Status, Reason: jsonNull(a.Reason), At: a.At}
		}),
		"unrecognised": sectionOf(rep.Unrecognised, func(v taxpg.Pending) pendingDTO {
			return pendingDTO{BookingID: v.ID, BookingCode: v.Code, EndedAt: v.EndedAt}
		}),
		"other_charges": sectionOf(rep.OtherCharges, func(v taxpg.EntryRow) revenueEntryDTO { return revenueEntryOf(v.RevenueEntry, v.BookingCode) }),
	})
}
