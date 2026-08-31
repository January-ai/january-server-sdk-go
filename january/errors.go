package january

import (
	"errors"
	"fmt"
)

var (
	ErrNotConfigured = errors.New("january: server credential is not configured")
	ErrInvalidInput  = errors.New("january: invalid input")
)

// APIError preserves server details. Error() deliberately excludes body text and identifiers.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	DocsURL    string
	RequestID  string
	Response   *Response
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
