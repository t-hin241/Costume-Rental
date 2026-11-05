package httpapi_test

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"webee_rentor/internal/platform/postgres/pgtest"
)

type refBody struct {
	ID, Slug string
}

func (s *shop) category(name string) refBody {
	s.t.Helper()
	return decode[refBody](s.t, s.send(s.a, "POST", "/admin/catalog/categories", fmt.Sprintf(`{"name":%q,"slug":%q}`, name, s.f.Prefix+"-"+name), http.StatusCreated))
}

// itemBody is an active item on sale for one day, in the given categories.
func (s *shop) itemBody(slug string, categories ...string) string {
	ids := "[]"
	if len(categories) > 0 {
		ids = `["` + strings.Join(categories, `","`) + `"]`
	}
	return fmt.Sprintf(`{"slug":%q,"name":"Áo","category_ids":%s,"description":"","images":[],"status":"ACTIVE","individually_rentable":true,
		"variants":[{"id":"","sku":%q,"size":"M","color":"","status":"ACTIVE"}],"prices":[{"package_id":%q,"amount":{"amount":90000,"currency":"VND"}}],
		"security_deposit":{"amount":1,"currency":"VND"},"daily_rate":{"amount":1,"currency":"VND"},"buffer_class":"garment"}`,
		s.f.Prefix+"-"+slug, ids, s.f.Prefix+"-"+slug, s.c.OneDay.ID)
}

func (s *shop) slugsIn(category string) []string {
	s.t.Helper()
	page := decode[struct{ Items []offeringBody }](s.t, s.send(nil, "GET", "/catalog/offerings?page_size=100&category="+category, "", http.StatusOK))
	out := make([]string, len(page.Items))
	for i, o := range page.Items {
		out[i] = o.Slug
	}
	slices.Sort(out)
	return out
}

func (s *shop) offeringsCounted(id string) int {
	s.t.Helper()
	all := decode[[]struct {
		ID            string
		OfferingCount int `json:"offering_count"`
	}](s.t, s.send(nil, "GET", "/catalog/categories", "", http.StatusOK))
	for _, c := range all {
		if c.ID == id {
			return c.OfferingCount
		}
	}
	s.t.Fatalf("category %s is not listed", id)
	return 0
}

func TestAnOfferingCanSitInSeveralCategories(t *testing.T) {
	s := newShop(t)
	anime, halloween := s.category("anime"), s.category("halloween")
	slug := s.f.Prefix + "-gojo"
	saved := decode[struct {
		ID          string
		CategoryIDs []string `json:"category_ids"`
		Variants    []struct{ ID string }
	}](t, s.send(s.a, "POST", "/admin/catalog/items", s.itemBody("gojo", halloween.ID, anime.ID), http.StatusCreated))
	if !slices.Equal(saved.CategoryIDs, []string{halloween.ID, anime.ID}) {
		t.Fatalf("saved = %+v", saved)
	}

	// The order chosen is kept: the first one is what the card shows.
	card := decode[struct{ Categories []refBody }](t, s.send(nil, "GET", "/catalog/offerings/ITEM/"+slug, "", http.StatusOK))
	if len(card.Categories) != 2 || card.Categories[0].ID != halloween.ID || card.Categories[1].Slug != anime.Slug {
		t.Fatalf("card = %+v", card)
	}
	for _, c := range []refBody{anime, halloween} {
		if got := s.slugsIn(c.Slug); !slices.Equal(got, []string{slug}) {
			t.Fatalf("offerings in %s = %v", c.Slug, got)
		}
		if n := s.offeringsCounted(c.ID); n != 1 {
			t.Fatalf("%s counts %d offerings", c.Slug, n)
		}
	}

	// Saving replaces the list.
	edited := strings.Replace(s.itemBody("gojo", anime.ID), `"variants":[{"id":""`, fmt.Sprintf(`"variants":[{"id":%q`, saved.Variants[0].ID), 1)
	s.send(s.a, "PUT", "/admin/catalog/items/"+saved.ID, edited, http.StatusOK)
	if got := s.slugsIn(halloween.Slug); len(got) != 0 {
		t.Fatalf("still in the category it left: %v", got)
	}
	if got := s.slugsIn(anime.Slug); !slices.Equal(got, []string{slug}) {
		t.Fatalf("offerings in %s = %v", anime.Slug, got)
	}
}

func TestABundleCanSitInSeveralCategories(t *testing.T) {
	s := newShop(t)
	s.f.Assets(s.c.JacketM, 1)
	anime, halloween := s.category("anime"), s.category("halloween")
	bundle := func(categories string) string {
		return fmt.Sprintf(`{"slug":%q,"name":"Bộ","category_ids":%s,"description":"","images":[],"status":"ACTIVE",
			"components":[{"key":"jacket","item_id":%q,"item_name":"","quantity":1,"options":[%q]}],
			"prices":[{"package_id":%q,"amount":{"amount":90000,"currency":"VND"}}],"deposit_override":null}`,
			s.f.Prefix+"-bo", categories, s.c.Jacket.ID, s.c.JacketM, s.c.OneDay.ID)
	}
	saved := decode[struct {
		ID          string
		CategoryIDs []string `json:"category_ids"`
	}](t, s.send(s.a, "POST", "/admin/catalog/bundles", bundle(fmt.Sprintf(`[%q,%q]`, anime.ID, halloween.ID)), http.StatusCreated))
	if !slices.Equal(saved.CategoryIDs, []string{anime.ID, halloween.ID}) {
		t.Fatalf("saved = %+v", saved)
	}
	for _, c := range []refBody{anime, halloween} {
		if got := s.slugsIn(c.Slug); !slices.Equal(got, []string{s.f.Prefix + "-bo"}) {
			t.Fatalf("offerings in %s = %v", c.Slug, got)
		}
	}
	s.send(s.a, "PUT", "/admin/catalog/bundles/"+saved.ID, bundle(`[]`), http.StatusOK)
	if got := s.slugsIn(anime.Slug); len(got) != 0 {
		t.Fatalf("still in the category it left: %v", got)
	}
}

func TestBadCategoriesAreRefused(t *testing.T) {
	s := newShop(t)
	anime := s.category("anime")
	many := make([]string, 11)
	for i := range many {
		many[i] = s.category(fmt.Sprint("c", i)).ID
	}
	for name, body := range map[string]string{
		"unknown":  s.itemBody("a", "no-such-category"),
		"repeated": s.itemBody("b", anime.ID, anime.ID),
		"empty":    s.itemBody("c", ""),
		"too many": s.itemBody("d", many...),
	} {
		t.Run(name, func(t *testing.T) {
			s.expect(s.do(call{method: "POST", path: "/api/v1/admin/catalog/items", body: body, cookie: s.a}), http.StatusBadRequest, "validation_failed")
		})
	}
	s.send(s.a, "POST", "/admin/catalog/items", s.itemBody("e", many[:10]...), http.StatusCreated)
	if n := s.count(`SELECT count(*) FROM catalog_items WHERE slug = ANY($1)`, []string{s.f.Prefix + "-a", s.f.Prefix + "-b", s.f.Prefix + "-c", s.f.Prefix + "-d"}); n != 0 {
		t.Fatalf("%d refused items were saved", n)
	}
}

// D37: revenue on the dashboard is what the shop keeps for good, on the day a
// booking ended. Rent of a cancelled booking, deposits and open bookings are not.
func TestDashboardRevenueCountsWhatTheShopKeeps(t *testing.T) {
	s := newShop(t)
	// Tests share a database, so each step is measured against the day before it.
	earned := func(date string) int64 {
		d := decode[struct {
			Series []struct {
				Date    string
				Revenue moneyBody
			} `json:"revenue_series"`
		}](t, s.send(s.a, "GET", "/admin/dashboard?range_days=30", "", http.StatusOK))
		for _, day := range d.Series {
			if day.Date == date {
				return day.Revenue.Amount
			}
		}
		t.Fatalf("%s is not on the dashboard", date)
		return 0
	}

	// Cancelled two days before pickup: the 30% fee stays, the rent does not.
	asset := string(s.f.Assets(s.c.JacketM, 1)[0])
	b := decode[detailBody](t, s.send(s.customer, "POST", "/bookings", s.order(s.c.JacketM, "2026-11-03", "kept", ""), http.StatusCreated))
	path := "/bookings/" + b.ID
	before := earned("2026-11-01")
	s.act(s.money, path+"/payments", pay("p1", "CASH", map[string]int64{"RESERVATION": 15_000}))
	s.act(s.staff, path+"/confirm", "")
	if got := earned("2026-11-01"); got != before {
		t.Fatalf("a payment moved revenue from %d to %d", before, got)
	}
	s.act(s.customer, path+"/cancel", `{"reason":"CUSTOMER_REQUEST","note":"Đổi kế hoạch"}`)
	if got := earned("2026-11-01"); got != before+15_000 {
		t.Fatalf("after the cancellation: %d, want %d", got, before+15_000)
	}

	// Nothing is earned while the jacket is out or being checked.
	rented := "/bookings/" + s.place(s.customer, "k1", "").ID
	s.handOver(rented, asset)
	s.shop.Set(pgtest.Now.AddDate(0, 0, 11))
	before = earned("2026-11-12")
	s.act(s.staff, rented+"/returns", fmt.Sprintf(`{"asset_ids":[%q]}`, asset))
	s.act(s.staff, rented+"/inspections", fmt.Sprintf(`{"asset_id":%q,"conditions":["NORMAL_WEAR"],"notes":"","evidence":[]}`, asset))
	settled := s.act(s.money, rented+"/settle", "")
	s.act(s.money, rented+"/refunds/"+settled.Finance.Refunds[0].ID+"/payout", `{"method":"CASH","reference":"","paid_at":"2026-11-12T01:00:00Z"}`)
	if got := earned("2026-11-12"); got != before {
		t.Fatalf("revenue moved from %d to %d before completion", before, got)
	}

	// Completed: the whole rent, never the 300,000đ deposit.
	if got := s.act(s.staff, rented+"/complete", ""); got.Status != "COMPLETED" {
		t.Fatalf("complete = %s", got.Status)
	}
	if got := earned("2026-11-12"); got != before+50_000 {
		t.Fatalf("after completion: %d, want %d", got, before+50_000)
	}
}
