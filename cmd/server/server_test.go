package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m, pgtest.SeedConfig) }

// logBuffer collects the server's output, which arrives on its own goroutine.
type logBuffer struct {
	mu  sync.Mutex
	out bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.out.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.out.String()
}

// startServer runs this command on a local port with its real wiring: default
// pool, PostgreSQL rate limits, the hold expiry job. Mail goes nowhere; a failed
// send only logs. Ending the test shuts it down the way a signal does.
func startServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"DATABASE_URL": pgtest.URL(t), "HTTP_ADDR": addr, "HTTP_COOKIE_SECURE": "false", "HTTP_TRUSTED_PROXIES": "127.0.0.1/32",
		"AUTH_MFA_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString(key), "APP_BASE_URL": "http://localhost:5173",
		"SMTP_HOST": "127.0.0.1", "SMTP_PORT": "1", "SMTP_TLS": "none", "SMTP_FROM": "shop@example.test",
		"MEDIA_DIR": t.TempDir(),
	} {
		t.Setenv(name, value)
	}
	logs := &logBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-stopped; err != nil {
			t.Errorf("shutdown: %v", err)
		}
		slog.SetDefault(previous)
		if t.Failed() {
			t.Logf("server log:\n%s", logs.String())
		}
	})
	base := "http://" + addr
	deadline := time.Now().Add(30 * time.Second)
	for {
		res, err := http.Get(base + "/readyz")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return base
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server did not get ready: %v\n%s", err, logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// person is one browser: its own address, behind the proxy the server trusts,
// and its own session.
type person struct {
	t       *testing.T
	base    string
	address string
	cookie  string
}

func (p person) do(method, path, body string) (int, []byte) {
	req, err := http.NewRequest(method, p.base+"/api/v1"+path, strings.NewReader(body))
	if err != nil {
		p.t.Error(err)
		return 0, nil
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Forwarded-For", p.address)
	if p.cookie != "" {
		req.Header.Set("Cookie", "session="+p.cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Error(err)
		return 0, nil
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, data
}

func (p person) want(status int, method, path, body string) []byte {
	p.t.Helper()
	got, data := p.do(method, path, body)
	if got != status {
		p.t.Fatalf("%s %s = %d, want %d: %s", method, path, got, status, data)
	}
	return data
}

const password = "correct horse battery"

// signUp registers through the API; only the emailed link is replaced.
func signUp(t *testing.T, f *pgtest.Fixture, base string, n int, roles ...identity.Role) person {
	t.Helper()
	p := person{t: t, base: base, address: fmt.Sprintf("198.51.%d.%d", n/250, n%250+1)}
	email := fmt.Sprintf("%s-%d@example.test", f.Prefix, n)
	credentials := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)
	p.want(http.StatusAccepted, "POST", "/auth/register", credentials)
	ctx := context.Background()
	if _, err := f.Pool.Exec(ctx, `UPDATE users SET email_verified_at = now() WHERE email = $1`, email); err != nil {
		t.Fatal(err)
	}
	for _, r := range roles {
		if _, err := f.Pool.Exec(ctx, `INSERT INTO user_roles (user_id, role, granted_at, granted_by_kind, granted_by_id)
			SELECT id, $2, now(), 'system', 'test' FROM users WHERE email = $1`, email, r); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest("POST", base+"/api/v1/auth/login", strings.NewReader(credentials))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", p.address)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	for _, c := range res.Cookies() {
		if c.Name == "session" {
			p.cookie = c.Value
		}
	}
	if p.cookie == "" {
		data, _ := io.ReadAll(res.Body)
		t.Fatalf("login = %d: %s", res.StatusCode, data)
	}
	return p
}

// bookableDate is a pickup day a few days ahead whose next day is open too.
func bookableDate(t *testing.T, after int) string {
	t.Helper()
	loc, err := businesstime.LoadLocation()
	if err != nil {
		t.Fatal(err)
	}
	open := func(d time.Time) bool { return d.Weekday() != time.Sunday && d.Format(time.DateOnly) != "2026-11-20" }
	d := time.Now().In(loc).AddDate(0, 0, after)
	for !open(d) || !open(d.AddDate(0, 0, 1)) {
		d = d.AddDate(0, 0, 1)
	}
	return d.Format(time.DateOnly)
}

// raceAll runs the requests at once and counts the answers by status.
func raceAll(t *testing.T, calls []func() (int, []byte)) (map[int]int, [][]byte) {
	t.Helper()
	statuses := make([]int, len(calls))
	bodies := make([][]byte, len(calls))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Go(func() {
			<-start
			statuses[i], bodies[i] = call()
		})
	}
	close(start)
	wg.Wait()
	out := map[int]int{}
	for i, s := range statuses {
		out[s]++
		if s == 0 || s >= 500 {
			t.Errorf("server error: %d %s", s, bodies[i])
		}
	}
	return out, bodies
}

// TestTheRealServerKeepsItsPromises drives the server over TCP through the
// three things that must hold before release: no double booking, no access
// across people, no duplicated money.
func TestTheRealServerKeepsItsPromises(t *testing.T) {
	base := startServer(t)
	f := pgtest.New(t)
	c := f.Catalog()
	f.Assets(c.JacketM, 1)
	order := func(date, key string) string {
		return fmt.Sprintf(`{"package_id":%q,"pickup_date":%q,"slot_id":"morning","outbound":"STORE","return":"STORE",
			"lines":[{"kind":"ITEM","offering_id":%q,"variant_id":%q,"quantity":1}],"request_key":%q}`, c.OneDay.ID, date, c.Jacket.ID, c.JacketM, key)
	}
	crowd := make([]person, 24)
	for i := range crowd {
		crowd[i] = signUp(t, f, base, i)
	}
	staff := signUp(t, f, base, 100, identity.RoleRentalStaff)
	guest := person{t: t, base: base, address: "198.51.200.1"}

	date := bookableDate(t, 3)
	calls := make([]func() (int, []byte), len(crowd))
	for i, p := range crowd {
		calls[i] = func() (int, []byte) { return p.do("POST", "/bookings", order(date, fmt.Sprint("k", i))) }
	}
	counts, bodies := raceAll(t, calls)
	if counts[http.StatusCreated] != 1 || counts[http.StatusConflict] != len(crowd)-1 {
		t.Fatalf("the crowd got %v", counts)
	}
	var booking struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Customer struct{ ID string }
		Finance  struct {
			PaidTotal struct{ Amount int64 } `json:"paid_total"`
			Payments  []any
		}
	}
	winner := -1
	for i, body := range bodies {
		if err := json.Unmarshal(body, &booking); err == nil && booking.ID != "" {
			winner = i
			break
		}
	}
	if winner < 0 {
		t.Fatal("no booking in the answers")
	}
	path := "/bookings/" + booking.ID
	var held int
	if err := f.Pool.QueryRow(context.Background(), `SELECT count(*) FROM allocations WHERE variant_id = $1 AND status = 'ACTIVE'`, c.JacketM).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 1 {
		t.Fatalf("%d allocations for one asset", held)
	}

	// Across people.
	stranger := crowd[(winner+1)%len(crowd)]
	stranger.want(http.StatusForbidden, "GET", path, "")
	stranger.want(http.StatusForbidden, "POST", path+"/cancel", `{"reason":"CUSTOMER_REQUEST","note":""}`)
	crowd[winner].want(http.StatusForbidden, "POST", path+"/confirm", "")
	crowd[winner].want(http.StatusForbidden, "GET", "/bookings", "")
	guest.want(http.StatusUnauthorized, "GET", path, "")
	guest.want(http.StatusUnauthorized, "POST", path+"/payments", "{}")

	// The same payment sent ten times at once.
	payment := `{"idempotency_key":"desk-1","method":"CASH","portions":[{"bucket":"RESERVATION","amount":{"amount":15000,"currency":"VND"}}]}`
	calls = make([]func() (int, []byte), 10)
	for i := range calls {
		calls[i] = func() (int, []byte) { return staff.do("POST", path+"/payments", payment) }
	}
	if counts, _ := raceAll(t, calls); counts[http.StatusOK] != len(calls) {
		t.Fatalf("payments got %v", counts)
	}
	if err := json.Unmarshal(staff.want(http.StatusOK, "GET", path, ""), &booking); err != nil {
		t.Fatal(err)
	}
	if booking.Finance.PaidTotal.Amount != 15_000 || len(booking.Finance.Payments) != 1 {
		t.Fatalf("finance = %+v", booking.Finance)
	}

	// Limits are shared through the database.
	attacker := person{t: t, base: base, address: "198.51.201.1"}
	limited := 0
	for i := range 25 {
		status, _ := attacker.do("POST", "/auth/login", fmt.Sprintf(`{"email":"nobody-%d@example.test","password":"wrong wrong wrong"}`, i))
		if status == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("25 failed logins from one address were never limited")
	}

	if testing.Short() {
		return
	}
	// The job runs once a minute in the server itself: an unpaid hold that ran
	// out gives the item back, without anyone asking.
	later := bookableDate(t, 9)
	left := crowd[(winner+2)%len(crowd)]
	var hold struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(left.want(http.StatusCreated, "POST", "/bookings", order(later, "hold")), &hold); err != nil {
		t.Fatal(err)
	}
	next := crowd[(winner+3)%len(crowd)]
	next.want(http.StatusConflict, "POST", "/bookings", order(later, "next-1"))
	if _, err := f.Pool.Exec(context.Background(), `UPDATE bookings SET hold_expires_at = now() - interval '1 minute' WHERE id = $1`, hold.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(80 * time.Second)
	for {
		var view struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(left.want(http.StatusOK, "GET", "/bookings/"+hold.ID, ""), &view); err != nil {
			t.Fatal(err)
		}
		if view.Status == "EXPIRED" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the hold is still %s", view.Status)
		}
		time.Sleep(time.Second)
	}
	next.want(http.StatusCreated, "POST", "/bookings", order(later, "next-2"))
}
