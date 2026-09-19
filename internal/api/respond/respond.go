// Package respond holds the JSON response and error-envelope helpers shared
// by every HTTP handler. Errors are always `{"code": ..., "message": ...}`.
package respond

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// Error is an HTTP error with a stable machine-readable code.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

// E builds an *Error.
func E(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Common errors.
var (
	ErrNotFound     = E(http.StatusNotFound, "not_found", "resource not found")
	ErrUnauthorized = E(http.StatusUnauthorized, "unauthorized", "sign in required")
	ErrForbidden    = E(http.StatusForbidden, "forbidden", "you do not have access to this resource")
)

// JSON writes v as a JSON body with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("respond: encode", "err", err)
	}
}

// Fail writes err as an error envelope. Non-*Error values become a 500 with
// a generic message; the details are logged, not leaked.
func Fail(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		slog.Error("internal error", "err", err)
		e = E(http.StatusInternalServerError, "internal_error", "internal server error")
	}
	JSON(w, e.Status, e)
}

// Failf is shorthand for Fail(w, E(status, code, msg)).
func Failf(w http.ResponseWriter, status int, code, msg string) {
	Fail(w, E(status, code, msg))
}

// DecodeJSON reads a JSON request body (max 1 MiB) into v.
func DecodeJSON(r *http.Request, v any) error {
	body := http.MaxBytesReader(nil, r.Body, 1<<20)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return E(http.StatusBadRequest, "invalid_json", "request body is empty")
		}
		return E(http.StatusBadRequest, "invalid_json", "malformed JSON body: "+err.Error())
	}
	return nil
}
