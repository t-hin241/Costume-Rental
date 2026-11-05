package httpapi_test

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"webee_rentor/internal/httpapi"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/ratelimit"
)

const uploadPath = "/api/v1/admin/media/images"

func pngImage(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func (a *api) stored() []string {
	a.t.Helper()
	entries, err := os.ReadDir(a.mediaDir)
	if err != nil {
		a.t.Fatal(err)
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name()
	}
	return out
}

func (s *shop) upload(cookie *http.Cookie, body string) string {
	s.t.Helper()
	w := s.do(call{method: "POST", path: uploadPath, body: body, cookie: cookie, header: map[string]string{"Content-Type": "image/png"}})
	s.expect(w, http.StatusCreated, "")
	return decode[struct{ URL string }](s.t, w).URL
}

func TestAnUploadedImageReachesTheStorefront(t *testing.T) {
	s := newShop(t)
	data := pngImage(t)
	url := s.upload(s.signIn("stock", identity.RoleInventoryManager), data)
	if !regexp.MustCompile(`^/api/v1/media/[0-9a-f-]{36}\.png$`).MatchString(url) {
		t.Fatalf("url = %s", url)
	}
	name := strings.TrimPrefix(url, "/api/v1/media/")
	if n := s.count(`SELECT count(*) FROM audit_events WHERE action = 'media.image_uploaded' AND resource_id = $1`, name); n != 1 {
		t.Fatalf("%d audit records", n)
	}

	w := s.do(call{method: "GET", path: url}) // a guest
	h := w.Header()
	if w.Code != http.StatusOK || w.Body.String() != data || h.Get("Content-Type") != "image/png" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Cache-Control") != "public, max-age=31536000, immutable" || !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("served %d, %d bytes, headers %v", w.Code, w.Body.Len(), h)
	}

	item := fmt.Sprintf(`{"slug":"with-image","name":"Áo","category_ids":[],"description":"","images":[{"url":%q,"alt":"Áo"}],"status":"DRAFT","individually_rentable":true,
		"variants":[{"id":"","sku":%q,"size":"M","color":"","status":"ACTIVE"}],"prices":[],"security_deposit":{"amount":1,"currency":"VND"},
		"daily_rate":{"amount":1,"currency":"VND"},"buffer_class":"garment"}`, url, s.f.Prefix+"-IMG")
	saved := decode[struct{ Images []struct{ URL string } }](t, s.send(s.a, "POST", "/admin/catalog/items", item, http.StatusCreated))
	if len(saved.Images) != 1 || saved.Images[0].URL != url {
		t.Fatalf("saved images = %+v", saved.Images)
	}
}

// The name comes from the content, whatever the request claims.
func TestTheContentDecidesWhatIsStored(t *testing.T) {
	s := newShop(t)
	script := `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`
	cases := map[string]struct {
		body, code string
	}{
		"empty":       {"", "media.unsupported_image"},
		"svg":         {script, "media.unsupported_image"},
		"html":        {"<!doctype html><script>alert(1)</script>", "media.unsupported_image"},
		"fake png":    {"\x89PNG\r\n\x1a\n" + script, "media.unsupported_image"},
		"too big":     {pngImage(t) + strings.Repeat("x", testImageLimit), "media.file_too_large"},
		"far too big": {strings.Repeat("x", 4*testImageLimit), "media.file_too_large"},
	}
	for name, c := range cases {
		w := s.do(call{method: "POST", path: uploadPath, body: c.body, cookie: s.a, header: map[string]string{"Content-Type": "image/png"}})
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.code) {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if files := s.stored(); len(files) != 0 {
		t.Fatalf("refused uploads left %v", files)
	}
}

func TestOnlyStoredImagesCanBeRead(t *testing.T) {
	s := newShop(t)
	secret := filepath.Join(filepath.Dir(s.mediaDir), "secret.png")
	if err := os.WriteFile(secret, []byte("bí mật"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(secret) })
	for _, name := range []string{"..%2Fsecret.png", "..%5Csecret.png", "x.png", "x.svg", "00000000-0000-4000-8000-000000000000.png", "00000000-0000-4000-8000-000000000000.png.tmp"} {
		w := s.do(call{method: "GET", path: "/api/v1/media/" + name})
		if w.Code == http.StatusOK || w.Code >= 500 || strings.Contains(w.Body.String(), "bí mật") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	s.expect(s.do(call{method: "GET", path: "/api/v1/media/x.png"}), http.StatusNotFound, "media.not_found")
}

func TestUploadsAreRateLimited(t *testing.T) {
	limits := httpapi.DefaultRateLimits()
	limits.ImageUploadPerUser = ratelimit.Rate{N: 2, Window: time.Hour}
	s := shopWith(t, options{limits: limits})
	data := pngImage(t)
	s.upload(s.a, data)
	s.upload(s.a, data)
	s.expect(s.do(call{method: "POST", path: uploadPath, body: data, cookie: s.a}), http.StatusTooManyRequests, "request.rate_limited")
	if files := s.stored(); len(files) != 2 {
		t.Fatalf("stored %v", files)
	}
}
