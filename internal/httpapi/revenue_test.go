package httpapi_test

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"strings"
	"testing"
)

// Decision D41 over HTTP: the tax profile, revenue from another channel, the
// threshold watch and the CSV export. The profile history is shared by the
// package database, so the first test to need one adds it.
func TestRevenueLedgerOverHTTP(t *testing.T) {
	s := newShop(t)
	profiles := decode[struct {
		Items []struct {
			Version string `json:"version"`
		} `json:"items"`
	}](t, s.send(s.money, "GET", "/admin/tax/profiles", "", http.StatusOK))
	if len(profiles.Items) == 0 {
		profile := `{"version":"tax-2026","annual_revenue_threshold":{"amount":1000000000,"currency":"VND"},
			"threshold_effective_from":"2026-01-01","warning_percents":[70,85,95],"effective_from":"2026-01-01"}`
		s.send(s.money, "POST", "/admin/tax/profiles", profile, http.StatusForbidden)
		s.send(s.a, "POST", "/admin/tax/profiles", profile, http.StatusCreated)
	}
	s.send(s.a, "POST", "/admin/tax/profiles", `{"version":"bad","annual_revenue_threshold":{"amount":1000000000,"currency":"VND"},
		"threshold_effective_from":"2026-01-01","warning_percents":[95,70],"effective_from":"2027-01-01"}`, http.StatusBadRequest)

	before := decode[struct {
		HasProfile bool `json:"has_profile"`
		Revenue    struct {
			Amount int64 `json:"amount"`
		} `json:"revenue"`
	}](t, s.send(s.money, "GET", "/admin/revenue/year?year=2026", "", http.StatusOK))
	if !before.HasProfile {
		t.Fatal("the year has no profile")
	}
	note := "=HYPERLINK(\"x\") " + s.f.Prefix
	entry := decode[struct {
		ID     string `json:"id"`
		Source string `json:"source"`
	}](t, s.send(s.money, "POST", "/admin/revenue/entries", `{"category":"RENTAL_REVENUE","amount":{"amount":2500000,"currency":"VND"},
		"date":"2026-10-15","channel":"Hội chợ","note":"`+strings.ReplaceAll(note, `"`, `\"`)+`","document_ref":"PT-9"}`, http.StatusCreated))
	if entry.Source != "MANUAL" {
		t.Fatalf("entry = %+v", entry)
	}
	s.send(s.staff, "POST", "/admin/revenue/entries", `{"category":"RENTAL_REVENUE","amount":{"amount":1,"currency":"VND"},"date":"2026-10-15","channel":"x"}`, http.StatusForbidden)
	s.send(s.money, "POST", "/admin/revenue/entries", `{"category":"RENTAL_SECURITY_DEPOSIT","amount":{"amount":1,"currency":"VND"},"date":"2026-10-15","channel":"x"}`, http.StatusBadRequest)

	after := decode[struct {
		Revenue struct {
			Amount int64 `json:"amount"`
		} `json:"revenue"`
	}](t, s.send(s.money, "GET", "/admin/revenue/year?year=2026", "", http.StatusOK))
	if after.Revenue.Amount-before.Revenue.Amount != 2_500_000 {
		t.Fatalf("revenue grew by %d", after.Revenue.Amount-before.Revenue.Amount)
	}

	w := s.send(s.money, "GET", "/admin/revenue/entries.csv?from=2026-10-15&to=2026-10-15&source=MANUAL", "", http.StatusOK)
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("content type %q", ct)
	}
	raw := w.Body.Bytes()
	if !bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("the export starts without a byte order mark")
	}
	rows, err := csv.NewReader(bytes.NewReader(raw[3:])).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows[1:] {
		if r[2] == entry.ID {
			found = true
			if r[8] != "2500000" || r[14] != "'"+note {
				t.Fatalf("exported row = %q", r)
			}
		}
	}
	if !found {
		t.Fatalf("the entry is not exported: %q", rows)
	}

	s.send(s.money, "POST", "/admin/revenue/entries/"+entry.ID+"/reverse", `{"reason":"nhập nhầm"}`, http.StatusCreated)
	s.send(s.money, "POST", "/admin/revenue/entries/"+entry.ID+"/reverse", `{"reason":"lần nữa"}`, http.StatusConflict)
	s.send(s.money, "GET", "/admin/revenue/report?from=2025-01-01&to=2026-12-31&group=day", "", http.StatusBadRequest)
	s.send(s.money, "GET", "/admin/revenue/report?from=2026-01-01&to=2026-12-31&group=quarter", "", http.StatusOK)
}
