package notifications_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/notifications"
	"webee_rentor/internal/rentals"
)

func bookingMail(t *testing.T, s notifications.Sender, emailOf func(context.Context, identity.UserID) (string, error)) *notifications.BookingMail {
	t.Helper()
	m, err := notifications.NewBookingMail(queue(t, s, 10), "Shop", emailOf)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestBookingMailSendsTheDeadlineInVietnamTime(t *testing.T) {
	s := &fakeSender{}
	q := queue(t, s, 10)
	m, err := notifications.NewBookingMail(q, "Shop", func(context.Context, identity.UserID) (string, error) { return "khach@example.test", nil })
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Date(2026, 11, 13, 2, 0, 0, 0, time.UTC)
	if err := m.BookingChanged(context.Background(), "u1", "BK123", rentals.ActionReportMissing, deadline); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := s.messages()
	if len(got) != 1 || got[0].To != "khach@example.test" || !strings.Contains(got[0].Body, "BK123") || !strings.Contains(got[0].Body, "09:00 13/11/2026") {
		t.Fatalf("messages = %+v", got)
	}
}

func TestBookingMailReportsAnUnknownCustomer(t *testing.T) {
	missing := errors.New("no such user")
	m := bookingMail(t, &fakeSender{}, func(context.Context, identity.UserID) (string, error) { return "", missing })
	if err := m.BookingChanged(context.Background(), "u1", "BK123", rentals.ActionConfirm, time.Time{}); !errors.Is(err, missing) {
		t.Fatalf("err = %v", err)
	}
}

func TestBookingMailIgnoresEventsWithoutATemplate(t *testing.T) {
	m := bookingMail(t, &fakeSender{}, func(context.Context, identity.UserID) (string, error) {
		t.Fatal("no address is needed for an event nobody is told about")
		return "", nil
	})
	if err := m.BookingChanged(context.Background(), "u1", "BK123", rentals.ActionStartPreparation, time.Time{}); err != nil {
		t.Fatal(err)
	}
}
