// Package audit defines the append-only record of sensitive changes. Events
// are never edited or deleted through normal operations.
package audit

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
)

// Result is the outcome of the audited action.
type Result string

const (
	ResultSuccess Result = "SUCCESS"
	ResultDenied  Result = "DENIED"
	ResultFailed  Result = "FAILED"
)

var ErrSensitiveField = domainerr.New(domainerr.KindInvalid, "audit.sensitive_field", "audit payload must not contain secrets or payment credentials")

// sensitiveTokens blocks payload keys whose words suggest secrets or card data.
var sensitiveTokens = map[string]bool{
	"password": true, "passwd": true, "secret": true, "token": true, "cvv": true, "cvc": true,
	"pan": true, "otp": true, "credential": true, "credentials": true, "cardnumber": true,
}

// Event is a single audit record.
type Event struct {
	ID           string
	OccurredAt   time.Time
	Actor        identity.Actor
	Action       string
	ResourceType string
	ResourceID   string
	Result       Result
	// Reason is required for adjustments and exceptions; the producing use
	// case decides when.
	Reason string
	// References links related records, e.g. booking_id, payment_id,
	// provider_event_id.
	References map[string]string
	// Changes holds non-sensitive before/after values, e.g. "status".
	Changes map[string]Change
}

// Change is a before/after pair.
type Change struct {
	From string
	To   string
}

// NewEvent validates and builds an event.
func NewEvent(e Event) (Event, error) {
	switch {
	case e.ID == "":
		return Event{}, domainerr.Invalid("id", "required")
	case e.OccurredAt.IsZero():
		return Event{}, domainerr.Invalid("occurred_at", "required")
	case !e.Actor.Valid():
		return Event{}, domainerr.Invalid("actor", "required")
	case e.Action == "":
		return Event{}, domainerr.Invalid("action", "required")
	case e.ResourceType == "" || e.ResourceID == "":
		return Event{}, domainerr.Invalid("resource", "type and id are required")
	}
	switch e.Result {
	case ResultSuccess, ResultDenied, ResultFailed:
	default:
		return Event{}, domainerr.Invalid("result", "unknown result")
	}
	for k := range e.References {
		if isSensitive(k) {
			return Event{}, fmt.Errorf("reference %q: %w", k, ErrSensitiveField)
		}
	}
	for k := range e.Changes {
		if isSensitive(k) {
			return Event{}, fmt.Errorf("change %q: %w", k, ErrSensitiveField)
		}
	}
	e.OccurredAt = e.OccurredAt.UTC()
	e.References = maps.Clone(e.References)
	e.Changes = maps.Clone(e.Changes)
	return e, nil
}

func isSensitive(key string) bool {
	k := strings.ToLower(key)
	if strings.Contains(k, "card_number") {
		return true
	}
	words := strings.FieldsFunc(k, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	for _, w := range words {
		if sensitiveTokens[w] {
			return true
		}
	}
	return false
}
