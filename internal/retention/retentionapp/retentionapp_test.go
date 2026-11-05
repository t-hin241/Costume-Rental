package retentionapp_test

import (
	"context"
	"testing"
	"time"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/retention/retentionapp"
	"webee_rentor/internal/support"
	"webee_rentor/internal/support/supportapp"
)

func TestMain(m *testing.M) { pgtest.Main(m, nil) }

// Decision D39: a support conversation is kept 24 months after it was
// resolved, then deleted with its messages. Open ones are never touched.
func TestResolvedConversationsAreDeletedTwoYearsAfterTheyClose(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	// A year before the shared clock, so this purge never reaches what other
	// tests resolve at pgtest.Now.
	start := pgtest.Now.AddDate(-1, 0, 0)
	clock := pgtest.NewClock(start)
	help := supportapp.New(f.Pool, supportapp.WithClock(clock.Now))
	purge := retentionapp.New(f.Pool, retentionapp.WithClock(clock.Now))
	customer := f.User("customer", identity.RoleCustomer)
	staff := f.User("staff", identity.RoleRentalStaff)
	ask := func() string {
		t.Helper()
		v, err := help.Create(ctx, customer, supportapp.CreateInput{Topic: support.TopicOther, Subject: "Hỏi", Message: "Cho hỏi"})
		if err != nil {
			t.Fatal(err)
		}
		return v.ID
	}
	answer := func(id string, resolve bool) {
		t.Helper()
		if _, err := help.Reply(ctx, staff, id, "Trả lời", resolve); err != nil {
			t.Fatal(err)
		}
	}
	old, open, later := ask(), ask(), ask()
	answer(open, false)
	closedAt := start.Add(time.Hour)
	clock.Set(closedAt)
	answer(old, true)
	clock.Set(closedAt.AddDate(0, 1, 0))
	answer(later, true)

	exists := func(id string) bool {
		t.Helper()
		var requests, messages int
		if err := f.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM support_requests WHERE id = $1),
			(SELECT count(*) FROM support_messages WHERE request_id = $1)`, id).Scan(&requests, &messages); err != nil {
			t.Fatal(err)
		}
		if (requests == 0) != (messages == 0) {
			t.Fatalf("support request %s: %d rows, %d messages", id, requests, messages)
		}
		return requests == 1
	}
	run := func() {
		t.Helper()
		if _, err := purge.PurgeSupport(ctx, 10); err != nil {
			t.Fatal(err)
		}
	}

	clock.Set(closedAt.AddDate(0, 24, 0))
	run()
	if !exists(old) {
		t.Fatal("a conversation is deleted on the day its 24 months end")
	}
	clock.Set(closedAt.AddDate(0, 24, 0).Add(time.Minute))
	run()
	run() // a second pass finds nothing more
	if exists(old) {
		t.Fatal("a conversation resolved 24 months ago is still kept")
	}
	if !exists(open) || !exists(later) {
		t.Fatal("an open or recent conversation was deleted")
	}
	var audits int
	if err := f.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'support.request_purged' AND resource_id = $1
		AND actor_kind = 'system' AND refs ? 'code'`, old).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("%d audit records of the purge", audits)
	}
	// The resolution time is what counts, whenever the request was opened.
	clock.Set(closedAt.AddDate(0, 25, 0).Add(time.Minute))
	run()
	if exists(later) || !exists(open) {
		t.Fatal("the later conversation must go a month after the first, the open one never")
	}
}
