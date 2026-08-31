package january

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Config configures an immutable HTTP client. Environment variables are not read implicitly.
type Config struct {
	SecretKey  string
	BaseURL    string
	HTTPClient *http.Client
	// Timeout bounds an entire call. Defaults to 30 seconds; context deadlines can shorten it.
	Timeout time.Duration
	// Deprecated: override used only by the prototype ClientTokens.Create alias.
	ClientTokenPath string
	// Explicit demo issuer, used only by the prototype alias.
	ClientTokenIssuer ClientTokenIssuer
}

func (c Config) String() string   { return "january.Config{credentials:[REDACTED]}" }
func (c Config) GoString() string { return c.String() }

// Optional preserves omitted, explicit null, and value states. Its zero value is omitted.
type Optional[T any] struct {
	value T
	set   bool
	null  bool
}

func Value[T any](v T) Optional[T]   { return Optional[T]{value: v, set: true} }
func Null[T any]() Optional[T]       { return Optional[T]{set: true, null: true} }
func (o Optional[T]) IsSet() bool    { return o.set }
func (o Optional[T]) IsNull() bool   { return o.set && o.null }
func (o Optional[T]) Get() (T, bool) { return o.value, o.set && !o.null }
func (o Optional[T]) MarshalJSON() ([]byte, error) {
	if !o.set || o.null {
		return []byte("null"), nil
	}
	return json.Marshal(o.value)
}
func (o *Optional[T]) UnmarshalJSON(b []byte) error {
	var zero T
	o.value = zero
	o.set = true
	o.null = string(b) == "null"
	if o.null {
		return nil
	}
	return json.Unmarshal(b, &o.value)
}
func putOptional[T any](m map[string]any, key string, v Optional[T]) {
	if v.set {
		m[key] = v
	}
}

// Response is per-call metadata, never mutable client state.
type Response struct {
	StatusCode int
	Headers    http.Header
	RequestID  string
	// RevokedCount is the count for this ONE DELETE, not an accumulated result.
	RevokedCount *int
	RetryAfter   string
}

// CreateClientTokenInput is retained for prototype compatibility.
type CreateClientTokenInput struct {
	EndUserID  string
	Scopes     []string
	TTLSeconds *int
}
type ClientTokenIssuer interface {
	Create(context.Context, CreateClientTokenInput) (ClientToken, error)
}

// ClientToken retains the prototype JSON shape; canonical operations use generated types.
type ClientToken struct {
	Token       string `json:"token"`
	AccessToken string `json:"-"`
	TokenType   string `json:"-"`
	ExpiresIn   int    `json:"expiresIn"`
	ExpiresAt   string `json:"-"`
}

func (t ClientToken) String() string   { return "january.ClientToken{token:[REDACTED]}" }
func (t ClientToken) GoString() string { return t.String() }
