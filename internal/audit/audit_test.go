package audit_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
)

func validEvent() audit.Event {
	return audit.Event{
		ID: "ev1", OccurredAt: time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC),
		Actor:  identity.UserActor("staff1"),
		Action: "booking.confirm", ResourceType: "booking", ResourceID: "b1",
		Result:     audit.ResultSuccess,
		References: map[string]string{"payment_id": "p1", "company": "x"},
		Changes:    map[string]audit.Change{"status": {From: "PENDING_CONFIRMATION", To: "CONFIRMED"}},
	}
}

func TestNewEventValidates(t *testing.T) {
	if _, err := audit.NewEvent(validEvent()); err != nil {
		t.Fatalf("valid event: %v", err)
	}
	e := validEvent()
	e.Actor = identity.Actor{}
	if _, err := audit.NewEvent(e); !errors.Is(err, domainerr.ErrValidation) {
		t.Fatalf("missing actor err = %v", err)
	}
}

func TestNewEventRejectsSensitiveKeys(t *testing.T) {
	for _, key := range []string{"password", "access_token", "card_number", "cvv", "provider.secret"} {
		e := validEvent()
		e.References = map[string]string{key: "x"}
		if _, err := audit.NewEvent(e); !errors.Is(err, audit.ErrSensitiveField) {
			t.Errorf("key %q err = %v", key, err)
		}
	}
}
