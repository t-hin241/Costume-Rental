package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/catalog"
	"webee_rentor/internal/catalog/catalogapp"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalsapp"
)

type imageDTO struct {
	URL string `json:"url"`
	Alt string `json:"alt"`
}

func imageOf(i catalog.Image) imageDTO { return imageDTO{URL: i.URL, Alt: i.Alt} }

func imagePtr(i *catalog.Image) *imageDTO {
	if i == nil {
		return nil
	}
	v := imageOf(*i)
	return &v
}

func imagesIn(in []imageDTO) []catalog.Image {
	if len(in) == 0 {
		return nil
	}
	return mapped(in, func(i imageDTO) catalog.Image { return catalog.Image{URL: i.URL, Alt: i.Alt} })
}

type packageDTO struct {
	ID            catalog.PackageID `json:"id"`
	Code          string            `json:"code"`
	Label         string            `json:"label"`
	DurationHours int               `json:"duration_hours"`
	Status        catalog.Status    `json:"status"`
}

func packageOf(p catalog.RentalPackage) packageDTO {
	return packageDTO{ID: p.ID, Code: p.Code, Label: p.Label, DurationHours: int(p.Duration / time.Hour), Status: p.Status}
}

type variantDTO struct {
	ID     catalog.VariantID `json:"id"`
	SKU    string            `json:"sku"`
	Size   string            `json:"size"`
	Color  string            `json:"color"`
	Status catalog.Status    `json:"status"`
}

func variantOf(v catalog.Variant) variantDTO {
	return variantDTO{ID: v.ID, SKU: v.SKU, Size: v.Size, Color: v.Color, Status: v.Status}
}

type categoryRef struct {
	ID   catalog.CategoryID `json:"id"`
	Slug string             `json:"slug"`
	Name string             `json:"name"`
}

type offeringCard struct {
	ID               string               `json:"id"`
	Kind             catalog.OfferingKind `json:"kind"`
	Slug             string               `json:"slug"`
	Name             string               `json:"name"`
	Category         *categoryRef         `json:"category"`
	Images           []imageDTO           `json:"images"`
	FromPrice        moneyDTO             `json:"from_price"`
	FromPackageLabel string               `json:"from_package_label"`
	Sizes            []string             `json:"sizes"`
	Colors           []string             `json:"colors"`
	ComponentCount   int                  `json:"component_count"`
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func cardOf(o catalogapp.Offering) offeringCard {
	c := offeringCard{
		ID: o.ID, Kind: o.Kind, Slug: o.Slug, Name: o.Name, Images: mapped(o.Images, imageOf), FromPrice: moneyOf(o.Prices[0].Price),
		FromPackageLabel: o.Prices[0].Package.Label, Sizes: nonNil(o.Sizes), Colors: nonNil(o.Colors), ComponentCount: o.ComponentCount,
	}
	if o.Category != nil {
		c.Category = &categoryRef{ID: o.Category.ID, Slug: o.Category.Slug, Name: o.Category.Name}
	}
	return c
}

type componentDTO struct {
	Key      string       `json:"key"`
	ItemName string       `json:"item_name"`
	Quantity int          `json:"quantity"`
	Options  []variantDTO `json:"options"`
}

type packagePriceDTO struct {
	Package packageDTO `json:"package"`
	Price   moneyDTO   `json:"price"`
}

type offeringDetail struct {
	offeringCard
	Description     string            `json:"description"`
	Variants        []variantDTO      `json:"variants"`
	Components      []componentDTO    `json:"components"`
	Prices          []packagePriceDTO `json:"prices"`
	SecurityDeposit moneyDTO          `json:"security_deposit"`
}

func (s *Server) categories(w http.ResponseWriter, r *http.Request) {
	all, err := s.svc.Catalog.Categories(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	type categoryDTO struct {
		categoryRef
		Image         *imageDTO `json:"image"`
		OfferingCount int       `json:"offering_count"`
	}
	writeJSON(w, http.StatusOK, mapped(all, func(c catalogapp.CategoryView) categoryDTO {
		return categoryDTO{categoryRef: categoryRef{ID: c.ID, Slug: c.Slug, Name: c.Name}, Image: imagePtr(c.Image), OfferingCount: c.OfferingCount}
	}))
}

func (s *Server) packages(w http.ResponseWriter, r *http.Request) {
	all, err := s.svc.Catalog.Packages(r.Context(), s.viewer(r.Context(), r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapped(all, packageOf))
}

func (s *Server) offerings(w http.ResponseWriter, r *http.Request) {
	page, size, err := pageParams(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q := catalogapp.Query{Page: page, PageSize: size, Sort: r.URL.Query().Get("sort")}
	for name, dst := range map[string]*string{"q": &q.Term, "category": &q.Category, "size": &q.Size, "color": &q.Color} {
		if *dst, err = textParam(r, name); err != nil {
			writeError(w, r, err)
			return
		}
	}
	switch kind := r.URL.Query().Get("kind"); kind {
	case "", string(catalog.KindItem), string(catalog.KindBundle):
		q.Kind = catalog.OfferingKind(kind)
	default:
		writeError(w, r, domainerr.Invalid("kind", "must be ITEM or BUNDLE"))
		return
	}
	res, err := s.svc.Catalog.Offerings(r.Context(), q)
	if err != nil {
		writeError(w, r, err)
		return
	}
	type facets struct {
		Sizes  []string `json:"sizes"`
		Colors []string `json:"colors"`
	}
	writeJSON(w, http.StatusOK, struct {
		pageBody[offeringCard]
		Facets facets `json:"facets"`
	}{
		pageBody[offeringCard]{Items: mapped(res.Offerings, cardOf), Page: page, PageSize: size, Total: res.Total},
		facets{Sizes: nonNil(res.Sizes), Colors: nonNil(res.Colors)},
	})
}

func (s *Server) offering(w http.ResponseWriter, r *http.Request) {
	kind := catalog.OfferingKind(strings.ToUpper(r.PathValue("kind")))
	if kind != catalog.KindItem && kind != catalog.KindBundle {
		writeError(w, r, errNotFound)
		return
	}
	o, err := s.svc.Catalog.Offering(r.Context(), kind, r.PathValue("slug"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, offeringDetail{
		offeringCard: cardOf(o), Description: o.Description, Variants: mapped(o.Variants, variantOf),
		Components: mapped(o.Components, func(c catalogapp.Component) componentDTO {
			return componentDTO{Key: c.Key, ItemName: c.ItemName, Quantity: c.Quantity, Options: mapped(c.Options, variantOf)}
		}),
		Prices: mapped(o.Prices, func(p catalogapp.PackagePrice) packagePriceDTO {
			return packagePriceDTO{Package: packageOf(p.Package), Price: moneyOf(p.Price)}
		}),
		SecurityDeposit: moneyOf(o.SecurityDeposit),
	})
}

func (s *Server) storeInfo(w http.ResponseWriter, _ *http.Request) {
	info := s.svc.Store
	// No payment gateway is integrated yet, so money is taken at the counter.
	writeJSON(w, http.StatusOK, map[string]any{
		"name": info.Name, "address": info.Address, "phone": info.Phone, "email": info.Email, "hours": info.Hours,
		"online_payment_available": false, "payment_methods": []string{"CASH", "BANK_TRANSFER"}, "payment_channels": []string{},
		"payment_instructions": info.PaymentInstructions,
	})
}

func (s *Server) storeCalendar(w http.ResponseWriter, r *http.Request) {
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
	c, err := s.svc.Rentals.Calendar(r.Context(), from, to)
	if err != nil {
		writeError(w, r, err)
		return
	}
	type slotDTO struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	type dayDTO struct {
		Date   string    `json:"date"`
		Open   bool      `json:"open"`
		Reason *string   `json:"reason"`
		Slots  []slotDTO `json:"slots"`
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"timezone": businesstime.LocationName, "min_date": c.MinDate.String(), "max_date": c.MaxDate.String(),
		"days": mapped(c.Days, func(d rentalsapp.CalendarDay) dayDTO {
			return dayDTO{Date: d.Date.String(), Open: d.Open, Reason: jsonNull(d.Reason), Slots: mapped(d.Slots, func(sl rentals.Slot) slotDTO {
				return slotDTO{ID: sl.ID, Label: fmt.Sprintf("%s đến %s", sl.Start, sl.End)}
			})}
		}),
	})
}

// fulfillmentOptions offers the store counter only: courier legs need the
// delivery module, which PlaceBooking refuses until it is connected.
func (s *Server) fulfillmentOptions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"outbound": []string{"STORE"}, "return": []string{"STORE"}, "zones": []any{}})
}

type tierDTO struct {
	MinNoticeHours int `json:"min_notice_hours"`
	FeePercent     int `json:"fee_percent"`
}

type lateTierDTO struct {
	UpToHours int `json:"up_to_hours"`
	Percent   int `json:"percent"`
}

type reservationDTO struct {
	Kind        rentals.ReservationDepositKind `json:"kind"`
	Amount      *moneyDTO                      `json:"amount"`
	RentPercent *int                           `json:"rent_percent"`
	Treatment   rentals.ReservationTreatment   `json:"treatment"`
}

type cancellationDTO struct {
	Tiers            []tierDTO `json:"tiers"`
	NoShowAfterHours int       `json:"no_show_after_hours"`
	NoShowFeePercent int       `json:"no_show_fee_percent"`
}

type lateFeeDTO struct {
	GraceMinutes     int           `json:"grace_minutes"`
	Tiers            []lateTierDTO `json:"tiers"`
	ExtraPeriodHours int           `json:"extra_period_hours"`
	ExtraPercent     int           `json:"extra_percent"`
}

func hours(d time.Duration) int   { return int(d / time.Hour) }
func minutes(d time.Duration) int { return int(d / time.Minute) }

func reservationOf(p rentals.ReservationPolicy) reservationDTO {
	out := reservationDTO{Kind: p.Kind, Treatment: p.Treatment}
	switch p.Kind {
	case rentals.ReservationFixed:
		m := moneyOf(p.Amount)
		out.Amount = &m
	case rentals.ReservationRentPercent:
		out.RentPercent = &p.RentPercent
	}
	return out
}

func cancellationOf(c rentals.CancellationPolicy) cancellationDTO {
	return cancellationDTO{
		Tiers: mapped(c.Tiers, func(t rentals.FeeTier) tierDTO {
			return tierDTO{MinNoticeHours: hours(t.MinNotice), FeePercent: t.FeePercent}
		}),
		NoShowAfterHours: hours(c.NoShowAfter), NoShowFeePercent: c.NoShowFeePercent,
	}
}

func lateFeeOf(l rentals.LateFeePolicy) lateFeeDTO {
	return lateFeeDTO{
		GraceMinutes: minutes(l.Grace), ExtraPeriodHours: hours(l.ExtraPeriod), ExtraPercent: l.ExtraPercent,
		Tiers: mapped(l.Tiers, func(t rentals.LateFeeTier) lateTierDTO {
			return lateTierDTO{UpToHours: hours(t.UpTo), Percent: t.Percent}
		}),
	}
}

func (s *Server) publicPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := s.svc.Rentals.ActivePolicy(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": p.Version, "hold_minutes": minutes(p.HoldTTL), "reservation": reservationOf(p.Reservation),
		"cancellation": cancellationOf(p.Cancellation), "late_fee": lateFeeOf(p.LateFee),
		"loss_verification_hours": hours(p.Loss.VerificationWindow), "reschedule_cutoff_hours": hours(p.CustomerRescheduleCutoff),
		"modification_cutoff_hours": hours(p.Modification.SelfServiceCutoff),
	})
}

type lineInput struct {
	Kind       catalog.OfferingKind `json:"kind"`
	OfferingID string               `json:"offering_id"`
	VariantID  catalog.VariantID    `json:"variant_id"`
	Selections map[string]string    `json:"selections"`
	Quantity   int                  `json:"quantity"`
}

type addressInput struct {
	RecipientName string `json:"recipient_name"`
	Phone         string `json:"phone"`
	Line          string `json:"line"`
	WardCode      string `json:"ward_code"`
}

type bookingInput struct {
	PackageID  catalog.PackageID `json:"package_id"`
	PickupDate string            `json:"pickup_date"`
	SlotID     string            `json:"slot_id"`
	Outbound   rentals.LegMode   `json:"outbound"`
	Return     rentals.LegMode   `json:"return"`
	Lines      []lineInput       `json:"lines"`
	// Address is accepted for courier legs, which are refused for now.
	Address    *addressInput `json:"address"`
	CouponCode string        `json:"coupon_code"`
	RequestKey string        `json:"request_key"`
}

func (b bookingInput) place() (rentalsapp.PlaceInput, error) {
	date, err := businesstime.ParseDate(b.PickupDate)
	if err != nil {
		return rentalsapp.PlaceInput{}, domainerr.Invalid("pickup_date", "must be a date (YYYY-MM-DD)")
	}
	if len(b.Lines) > maxLines {
		return rentalsapp.PlaceInput{}, domainerr.Invalid("lines", "too many lines")
	}
	if len(b.SlotID) > maxIDLength || len(b.RequestKey) > maxIDLength || len(b.CouponCode) > maxIDLength {
		return rentalsapp.PlaceInput{}, domainerr.Invalid("request", "a field is too long")
	}
	in := rentalsapp.PlaceInput{
		RequestKey: b.RequestKey, PackageID: b.PackageID, PickupDate: date, SlotID: b.SlotID, Outbound: b.Outbound, Return: b.Return, CouponCode: b.CouponCode,
		Lines: mapped(b.Lines, func(l lineInput) rentalsapp.LineInput {
			picks := make(map[string]catalog.VariantID, len(l.Selections))
			for k, v := range l.Selections {
				picks[k] = catalog.VariantID(v)
			}
			return rentalsapp.LineInput{Kind: l.Kind, OfferingID: l.OfferingID, VariantID: l.VariantID, Selections: picks, Quantity: l.Quantity}
		}),
	}
	if in.Outbound == "" {
		in.Outbound = rentals.LegStore
	}
	if in.Return == "" {
		in.Return = rentals.LegStore
	}
	return in, nil
}

func (s *Server) parseBooking(w http.ResponseWriter, r *http.Request) (rentalsapp.PlaceInput, error) {
	body, err := decodeSmall[bookingInput](w, r)
	if err != nil {
		return rentalsapp.PlaceInput{}, err
	}
	return body.place()
}

func (s *Server) availability(w http.ResponseWriter, r *http.Request) {
	if err := s.limit(r, "quote-ip", s.clientIP(r), s.cfg.RateLimits.QuotePerIP); err != nil {
		writeError(w, r, err)
		return
	}
	in, err := s.parseBooking(w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a, err := s.svc.Rentals.CheckAvailability(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.availabilityOf(r.Context(), a))
}

type shortageDTO struct {
	LineRef   string `json:"line_ref"`
	Label     string `json:"label"`
	Requested int    `json:"requested"`
	Available int    `json:"available"`
}

func (s *Server) availabilityOf(ctx context.Context, a rentalsapp.Availability) map[string]any {
	ids := mapped(a.Shortages, func(x inventory.Shortage) catalog.VariantID { return x.VariantID })
	names := s.svc.Rentals.VariantNames(ctx, ids)
	return map[string]any{
		"available": a.Available, "pickup_at": a.Schedule.PickupAt, "due_at": a.Schedule.DueAt,
		"shortages": mapped(a.Shortages, func(x inventory.Shortage) shortageDTO {
			return shortageDTO{LineRef: x.LineRef, Label: names[x.VariantID], Requested: x.Requested, Available: x.Available}
		}),
	}
}

type quoteLineDTO struct {
	Label    string   `json:"label"`
	Quantity int      `json:"quantity"`
	ListRent moneyDTO `json:"list_rent"`
	Discount moneyDTO `json:"discount"`
	Rent     moneyDTO `json:"rent"`
	Security moneyDTO `json:"security"`
}

func (s *Server) quote(w http.ResponseWriter, r *http.Request) {
	if err := s.limit(r, "quote-ip", s.clientIP(r), s.cfg.RateLimits.QuotePerIP); err != nil {
		writeError(w, r, err)
		return
	}
	in, err := s.parseBooking(w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q, err := s.svc.Rentals.Quote(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	b := q.Booking
	terms := b.PaymentTerms()
	var treatment *rentals.ReservationTreatment
	if b.ReservationDeposit.IsPositive() {
		treatment = &b.Policy.Reservation.Treatment
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"lines": mapped(b.Lines, func(l rentals.Line) quoteLineDTO {
			return quoteLineDTO{Label: lineLabel(l, q.Variants), Quantity: l.Quantity, ListRent: moneyOf(l.ListRent), Discount: moneyOf(l.Discount),
				Rent: moneyOf(l.RentAmount), Security: moneyOf(l.DepositAmount)}
		}),
		"pickup_at": b.Schedule.PickupAt, "due_at": b.Schedule.DueAt, "gross_rent": moneyOf(b.GrossRentTotal), "discount_total": moneyOf(b.DiscountTotal),
		"rent_total": moneyOf(b.RentTotal), "reservation_deposit": moneyOf(b.ReservationDeposit), "reservation_treatment": treatment,
		"security_total": moneyOf(b.SecurityDepositTotal), "delivery_outbound_fee": moneyOf(b.Fulfillment.OutboundFee),
		"delivery_return_fee": moneyOf(b.Fulfillment.ReturnFee), "due_to_confirm": moneyOf(terms.DueToConfirm),
		"due_before_handover": moneyOf(terms.DueBeforeHandover), "refundable_total": moneyOf(terms.Refundable),
		"hold_minutes": minutes(b.Policy.HoldTTL), "policy_version": b.Policy.Version,
	})
}

type adminPriceDTO struct {
	PackageID catalog.PackageID `json:"package_id"`
	Amount    moneyDTO          `json:"amount"`
}

func pricesOf(ps catalog.Prices) []adminPriceDTO {
	out := make([]adminPriceDTO, 0, len(ps))
	for id, m := range ps {
		out = append(out, adminPriceDTO{PackageID: id, Amount: moneyOf(m)})
	}
	return out
}

func pricesIn(in []adminPriceDTO) (catalog.Prices, error) {
	out := make(catalog.Prices, len(in))
	for _, p := range in {
		m, err := p.Amount.value("prices")
		if err != nil {
			return nil, err
		}
		out[p.PackageID] = m
	}
	return out, nil
}

func categoryPtr(id catalog.CategoryID) *catalog.CategoryID {
	if id == "" {
		return nil
	}
	return &id
}

type adminItemDTO struct {
	ID                   catalog.ItemID      `json:"id"`
	Slug                 string              `json:"slug"`
	Name                 string              `json:"name"`
	CategoryID           *catalog.CategoryID `json:"category_id"`
	Description          string              `json:"description"`
	Images               []imageDTO          `json:"images"`
	Status               catalog.Status      `json:"status"`
	IndividuallyRentable bool                `json:"individually_rentable"`
	Variants             []variantDTO        `json:"variants"`
	Prices               []adminPriceDTO     `json:"prices"`
	SecurityDeposit      moneyDTO            `json:"security_deposit"`
	DailyRate            moneyDTO            `json:"daily_rate"`
	BufferClass          string              `json:"buffer_class"`
	Version              int                 `json:"version"`
	AssetCount           int                 `json:"asset_count"`
}

func adminItemOf(i catalogapp.AdminItem) adminItemDTO {
	return adminItemDTO{
		ID: i.ID, Slug: i.Slug, Name: i.Name, CategoryID: categoryPtr(i.CategoryID), Description: i.Description, Images: mapped(i.Images, imageOf),
		Status: i.Status, IndividuallyRentable: i.IndividuallyRentable, Variants: mapped(i.Variants, variantOf), Prices: pricesOf(i.Prices),
		SecurityDeposit: moneyOf(i.SecurityDeposit), DailyRate: moneyOf(i.DailyRate), BufferClass: i.BufferClass, Version: i.Version, AssetCount: i.AssetCount,
	}
}

func (s *Server) adminItems(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	all, err := s.svc.Catalog.AdminItems(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapped(all, adminItemOf))
}

type itemInput struct {
	Slug                 string              `json:"slug"`
	Name                 string              `json:"name"`
	CategoryID           *catalog.CategoryID `json:"category_id"`
	Description          string              `json:"description"`
	Images               []imageDTO          `json:"images"`
	Status               catalog.Status      `json:"status"`
	IndividuallyRentable bool                `json:"individually_rentable"`
	Variants             []variantDTO        `json:"variants"`
	Prices               []adminPriceDTO     `json:"prices"`
	SecurityDeposit      moneyDTO            `json:"security_deposit"`
	DailyRate            moneyDTO            `json:"daily_rate"`
	BufferClass          string              `json:"buffer_class"`
}

func deref(id *catalog.CategoryID) catalog.CategoryID {
	if id == nil {
		return ""
	}
	return *id
}

func optionalPath(r *http.Request, name string) (string, error) {
	if r.PathValue(name) == "" {
		return "", nil
	}
	return pathID(r, name)
}

func (s *Server) saveItem(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	id, err := optionalPath(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var in itemInput
	if err := decodeJSON(w, r, &in, largeBody); err != nil {
		writeError(w, r, err)
		return
	}
	prices, err := pricesIn(in.Prices)
	if err != nil {
		writeError(w, r, err)
		return
	}
	deposit, err := in.SecurityDeposit.value("security_deposit")
	if err != nil {
		writeError(w, r, err)
		return
	}
	daily, err := in.DailyRate.value("daily_rate")
	if err != nil {
		writeError(w, r, err)
		return
	}
	it, err := s.svc.Catalog.SaveItem(r.Context(), p, catalog.ItemID(id), catalog.Item{
		Slug: in.Slug, Name: in.Name, CategoryID: deref(in.CategoryID), Description: in.Description, Images: imagesIn(in.Images), Status: in.Status,
		IndividuallyRentable: in.IndividuallyRentable, Prices: prices, SecurityDeposit: deposit, DailyRate: daily, BufferClass: in.BufferClass,
		Variants: mapped(in.Variants, func(v variantDTO) catalog.Variant {
			return catalog.Variant{ID: v.ID, SKU: v.SKU, Size: v.Size, Color: v.Color, Status: v.Status}
		}),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, savedStatus(id), adminItemOf(catalogapp.AdminItem{Item: it}))
}

func savedStatus(id string) int {
	if id == "" {
		return http.StatusCreated
	}
	return http.StatusOK
}

type adminComponentDTO struct {
	Key      string              `json:"key"`
	ItemID   catalog.ItemID      `json:"item_id"`
	ItemName string              `json:"item_name"`
	Quantity int                 `json:"quantity"`
	Options  []catalog.VariantID `json:"options"`
}

type adminBundleDTO struct {
	ID              catalog.BundleID    `json:"id"`
	Slug            string              `json:"slug"`
	Name            string              `json:"name"`
	CategoryID      *catalog.CategoryID `json:"category_id"`
	Description     string              `json:"description"`
	Images          []imageDTO          `json:"images"`
	Status          catalog.Status      `json:"status"`
	Components      []adminComponentDTO `json:"components"`
	Prices          []adminPriceDTO     `json:"prices"`
	DepositOverride *moneyDTO           `json:"deposit_override"`
	Version         int                 `json:"version"`
}

func (s *Server) adminBundleOf(ctx context.Context, b catalog.Bundle, names map[catalog.ItemID]string) adminBundleDTO {
	return adminBundleDTO{
		ID: b.ID, Slug: b.Slug, Name: b.Name, CategoryID: categoryPtr(b.CategoryID), Description: b.Description, Images: mapped(b.Images, imageOf),
		Status: b.Status, Prices: pricesOf(b.Prices), DepositOverride: moneyPtr(b.DepositOverride), Version: b.Version,
		Components: mapped(b.Components, func(c catalog.BundleComponent) adminComponentDTO {
			return adminComponentDTO{Key: c.Key, ItemID: c.ItemID, ItemName: names[c.ItemID], Quantity: c.Quantity, Options: nonNil(c.Options)}
		}),
	}
}

func (s *Server) itemNames(ctx context.Context, p identity.Principal) (map[catalog.ItemID]string, error) {
	items, err := s.svc.Catalog.AdminItems(ctx, p)
	if err != nil {
		return nil, err
	}
	names := make(map[catalog.ItemID]string, len(items))
	for _, it := range items {
		names[it.ID] = it.Name
	}
	return names, nil
}

func (s *Server) adminBundles(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	all, err := s.svc.Catalog.AdminBundles(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	names, err := s.itemNames(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapped(all, func(b catalog.Bundle) adminBundleDTO { return s.adminBundleOf(r.Context(), b, names) }))
}

type bundleInput struct {
	Slug            string              `json:"slug"`
	Name            string              `json:"name"`
	CategoryID      *catalog.CategoryID `json:"category_id"`
	Description     string              `json:"description"`
	Images          []imageDTO          `json:"images"`
	Status          catalog.Status      `json:"status"`
	Components      []adminComponentDTO `json:"components"`
	Prices          []adminPriceDTO     `json:"prices"`
	DepositOverride *moneyDTO           `json:"deposit_override"`
}

func (s *Server) saveBundle(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	id, err := optionalPath(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var in bundleInput
	if err := decodeJSON(w, r, &in, largeBody); err != nil {
		writeError(w, r, err)
		return
	}
	prices, err := pricesIn(in.Prices)
	if err != nil {
		writeError(w, r, err)
		return
	}
	b := catalog.Bundle{
		Slug: in.Slug, Name: in.Name, CategoryID: deref(in.CategoryID), Description: in.Description, Images: imagesIn(in.Images), Status: in.Status, Prices: prices,
		Components: mapped(in.Components, func(c adminComponentDTO) catalog.BundleComponent {
			return catalog.BundleComponent{Key: c.Key, ItemID: c.ItemID, Options: c.Options, Quantity: c.Quantity}
		}),
	}
	if in.DepositOverride != nil {
		m, err := in.DepositOverride.value("deposit_override")
		if err != nil {
			writeError(w, r, err)
			return
		}
		b.DepositOverride = &m
	}
	saved, err := s.svc.Catalog.SaveBundle(r.Context(), p, catalog.BundleID(id), b)
	if err != nil {
		writeError(w, r, err)
		return
	}
	names, err := s.itemNames(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, savedStatus(id), s.adminBundleOf(r.Context(), saved, names))
}

func (s *Server) saveCategory(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	id, err := optionalPath(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	in, err := decodeSmall[struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}](w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	c, err := s.svc.Catalog.SaveCategory(r.Context(), p, catalog.CategoryID(id), catalog.Category{Name: in.Name, Slug: in.Slug})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, savedStatus(id), map[string]any{"id": c.ID, "slug": c.Slug, "name": c.Name, "image": imagePtr(c.Image), "offering_count": 0})
}

func (s *Server) savePackage(w http.ResponseWriter, r *http.Request, p identity.Principal, _ identity.Session) {
	id, err := optionalPath(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	in, err := decodeSmall[struct {
		Code          string         `json:"code"`
		Label         string         `json:"label"`
		DurationHours int            `json:"duration_hours"`
		Status        catalog.Status `json:"status"`
	}](w, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if in.DurationHours < 1 || in.DurationHours > 24*366 {
		writeError(w, r, domainerr.Invalid("duration_hours", "must be between 1 hour and 1 year"))
		return
	}
	saved, err := s.svc.Catalog.SavePackage(r.Context(), p, catalog.PackageID(id), catalog.RentalPackage{
		Code: in.Code, Label: in.Label, Duration: time.Duration(in.DurationHours) * time.Hour, Status: in.Status,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, savedStatus(id), packageOf(saved))
}
