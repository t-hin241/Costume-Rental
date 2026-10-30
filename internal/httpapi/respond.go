package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
)

// apiError is a transport-level failure (bad body, rate limit, ...) that
// has no domain equivalent.
type apiError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

var (
	errUnsupportedMedia = &apiError{Status: http.StatusUnsupportedMediaType, Code: "request.unsupported_media_type", Message: "the body must be application/json"}
	errBodyTooLarge     = &apiError{Status: http.StatusRequestEntityTooLarge, Code: "request.body_too_large", Message: "the request body is too large"}
	errInvalidBody      = &apiError{Status: http.StatusBadRequest, Code: "request.invalid_body", Message: "the request body is not valid JSON for this endpoint"}
	errCrossOrigin      = &apiError{Status: http.StatusForbidden, Code: "request.cross_origin", Message: "cross-origin request refused"}
	errNotFound         = &apiError{Status: http.StatusNotFound, Code: "request.not_found", Message: "no such endpoint"}
	errInternal         = &apiError{Status: http.StatusInternalServerError, Code: "internal", Message: "an internal error occurred"}
)

func errRateLimited(retry time.Duration) error {
	return &apiError{Status: http.StatusTooManyRequests, Code: "request.rate_limited", Message: "too many requests, try again later", RetryAfter: retry}
}

type errorBody struct {
	Error     errorDetail `json:"error"`
	RequestID string      `json:"request_id"`
}

type errorDetail struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Fields  []fieldError `json:"fields,omitempty"`
}

type fieldError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

var kindStatus = map[domainerr.Kind]int{
	domainerr.KindInvalid:      http.StatusBadRequest,
	domainerr.KindNotFound:     http.StatusNotFound,
	domainerr.KindConflict:     http.StatusConflict,
	domainerr.KindForbidden:    http.StatusForbidden,
	domainerr.KindInvalidState: http.StatusConflict,
}

// writeError maps err to the uniform error response. Only stable codes and
// the sentinel's own message reach the client; wrapped context, SQL and
// internal identifiers stay in the server log.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	detail, status := errorDetail{Code: errInternal.Code, Message: errInternal.Message}, errInternal.Status
	var api *apiError
	var field *domainerr.FieldError
	var de *domainerr.Error
	switch {
	case errors.As(err, &api):
		detail, status = errorDetail{Code: api.Code, Message: api.Message}, api.Status
		if api.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int((api.RetryAfter+time.Second-1)/time.Second)))
		}
	case errors.As(err, &field):
		detail = errorDetail{Code: domainerr.ErrValidation.Code, Message: domainerr.ErrValidation.Message,
			Fields: []fieldError{{Field: field.Field, Reason: field.Reason}}}
		status = http.StatusBadRequest
	case errors.Is(err, identity.ErrNotPrincipal), errors.Is(err, identity.ErrInvalidCredentials),
		errors.Is(err, identity.ErrInvalidChallenge):
		// These carry KindForbidden for the domain; for HTTP the client
		// must (re)authenticate.
		errors.As(err, &de)
		detail, status = errorDetail{Code: de.Code, Message: de.Message}, http.StatusUnauthorized
	case errors.As(err, &de):
		detail, status = errorDetail{Code: de.Code, Message: de.Message}, kindStatus[de.Kind]
		if status == 0 {
			status = http.StatusInternalServerError
		}
	}
	if status >= 500 {
		slog.ErrorContext(r.Context(), "request failed", "error", err)
	}
	writeJSON(w, status, errorBody{Error: detail, RequestID: requestID(r.Context())})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body) // the status is sent; nothing useful to do on a write error
	}
}

// Body size limits.
const (
	smallBody = 16 << 10 // credentials, reasons
	largeBody = 1 << 20  // permission matrix
)

// decodeJSON reads exactly one JSON object into dst. Unknown fields are
// refused, so a DTO is an allowlist and clients cannot slip in fields such
// as user_id or roles.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return errUnsupportedMedia
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errBodyTooLarge
		}
		return errInvalidBody
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errInvalidBody
	}
	return nil
}

func isRateLimited(err error) bool {
	var api *apiError
	return errors.As(err, &api) && api.Status == http.StatusTooManyRequests
}
