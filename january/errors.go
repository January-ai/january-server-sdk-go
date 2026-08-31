package january

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	ErrNotConfigured       = errors.New("january: server credential is not configured")
	ErrInvalidInput        = errors.New("january: invalid input")
	ErrBadRequest          = errors.New("january: bad request")
	ErrAuthentication      = errors.New("january: authentication failed")
	ErrPermissionDenied    = errors.New("january: permission denied")
	ErrNotFound            = errors.New("january: resource not found")
	ErrPayloadTooLarge     = errors.New("january: payload too large")
	ErrRateLimit           = errors.New("january: rate limited")
	ErrCreditLimitExceeded = errors.New("january: credit limit exceeded")
	ErrInternalServer      = errors.New("january: server failure")
)

// APIError preserves server details. Error() deliberately excludes body text and identifiers.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	DocsURL    string
	RequestID  string
	Response   *Response
	// Body is redacted diagnostic text; Message is bounded to 200 bytes plus a marker.
	Body string
	// RetryNote explains why a server-requested wait could not be honored.
	RetryNote string
}

// Unwrap supports errors.Is without losing APIError details through errors.As.
// Only rate_limited and credit_limit_exceeded override the HTTP classification.
func (e *APIError) Unwrap() error {
	switch e.Code {
	case "rate_limited":
		return ErrRateLimit
	case "credit_limit_exceeded":
		return ErrCreditLimitExceeded
	}
	switch e.StatusCode {
	case 400:
		return ErrBadRequest
	case 401:
		return ErrAuthentication
	case 403:
		return ErrPermissionDenied
	case 404:
		return ErrNotFound
	case 413:
		return ErrPayloadTooLarge
	case 429:
		return ErrRateLimit
	}
	if e.StatusCode >= 500 {
		return ErrInternalServer
	}
	return nil
}

var credentialPattern = regexp.MustCompile(`\b(?:sk|ct)-[A-Za-z0-9_-]+`)

func redactDiagnostic(value, secret string) string {
	if secret != "" {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return credentialPattern.ReplaceAllString(value, "[REDACTED]")
}

func (e *APIError) Error() string {
	return fmt.Sprintf("january: API request failed (HTTP %d)", e.StatusCode)
}
func (e *APIError) GoString() string { return e.Error() }

// TransportError wraps network, decoding, and context errors for errors.Is/As.
type TransportError struct {
	Kind  string
	Cause error
}

func (e *TransportError) Error() string    { return "january: " + e.Kind }
func (e *TransportError) GoString() string { return e.Error() }
func (e *TransportError) Unwrap() error    { return e.Cause }
