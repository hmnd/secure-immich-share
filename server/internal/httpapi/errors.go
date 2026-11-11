package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// apiError is a handler error rendered as the error envelope. Cause, when
// set, is logged for 5xx responses and never sent to the client.
type apiError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter int // seconds; sets Retry-After when > 0
	Cause      error
}

func (e *apiError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return e.Code + ": " + e.Message
}

func (e *apiError) StatusCode() int { return e.Status }

func (e *apiError) Unwrap() error { return e.Cause }

func newAPIError(status int, code, msg string) *apiError {
	return &apiError{Status: status, Code: code, Message: msg}
}

func errBadRequest(code, msg string) *apiError {
	return newAPIError(http.StatusBadRequest, code, msg)
}

// errNotFound is the uniform answer for anything a visitor may not see:
// unknown or disabled links, revoked shares and assets outside the share.
func errNotFound() *apiError {
	return newAPIError(http.StatusNotFound, "not_found", "not found")
}

// busyRetry is how long clients wait after a 503 busy.
const busyRetry = 2

func errBusy() *apiError {
	return &apiError{Status: http.StatusServiceUnavailable, Code: "busy", Message: "busy, try again shortly",
		RetryAfter: busyRetry}
}

func errUnavailable() *apiError {
	return &apiError{Status: http.StatusServiceUnavailable, Code: "upstream_unavailable",
		Message: "the photo server is unavailable", RetryAfter: 30}
}

func errInternal(msg string, cause error) *apiError {
	return &apiError{Status: http.StatusInternalServerError, Code: "internal_error", Message: msg, Cause: cause}
}

// handleError maps every handler error onto an apiError; anything unknown
// becomes 500 internal_error. 5xx causes are logged.
func (s *Server) handleError(ctx context.Context, err error) error {
	var ae *apiError
	if !errors.As(err, &ae) {
		ae = errInternal("internal server error", err)
	}
	if ae.Status >= 500 && ae.Cause != nil {
		s.deps.Logger.ErrorContext(ctx, "http handler error", "code", ae.Code, "error", ae.Cause.Error())
	}
	return ae
}

func sendError(w http.ResponseWriter, _ *http.Request, err error) {
	var ae *apiError
	if !errors.As(err, &ae) {
		ae = errInternal("internal server error", err)
	}
	writeAPIError(w, ae)
}

func writeAPIError(w http.ResponseWriter, ae *apiError) {
	setRetryAfter(w, ae)
	writeError(w, ae.Status, ae.Code, ae.Message)
}

func setRetryAfter(w http.ResponseWriter, ae *apiError) {
	if ae.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(ae.RetryAfter))
	}
}

// sendJSON is the only response serializer: JSON regardless of Accept.
func sendJSON(w http.ResponseWriter, _ *http.Request, ans any) error {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(ans)
}

// writeJSON writes v as a JSON response (std handlers).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeError writes the standard error envelope (std handlers).
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Del("Content-Disposition")
	w.Header().Del("ETag")
	w.Header().Del("Last-Modified")
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}

// maxBodyBytes bounds request bodies.
const maxBodyBytes = 16 << 10

// jsonContentType defaults Content-Type to application/json for responses
// with a body, so a status written before serialization still carries it.
func jsonContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(jsonHeaderWriter{w}, r)
	})
}

type jsonHeaderWriter struct{ http.ResponseWriter }

// defaultJSON sets the Content-Type unless the handler did; after the
// header is written it has no effect.
func (w jsonHeaderWriter) defaultJSON() {
	if h := w.Header(); h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/json")
	}
}

func (w jsonHeaderWriter) WriteHeader(code int) {
	if code >= 200 && code != http.StatusNoContent && code != http.StatusNotModified {
		w.defaultJSON()
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w jsonHeaderWriter) Write(b []byte) (int, error) {
	w.defaultJSON()
	return w.ResponseWriter.Write(b)
}

func (w jsonHeaderWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
