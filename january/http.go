package january

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 16 << 20

type transport struct {
	secretKey       string
	baseURL         string
	httpClient      *http.Client
	timeout         time.Duration
	explicitTimeout bool
	maxRetries      int
}
type userContext struct {
	id       string
	timezone string
}
type service struct {
	transport *transport
	user      userContext
}
type parameter struct {
	Name     string
	In       string
	Required bool
	Explode  bool
	Schema   json.RawMessage
}
type operation struct {
	ID              string
	Method          string
	Path            string
	Parameters      []parameter
	BodyFields      []string
	RequiredBody    bool
	BodySchema      json.RawMessage
	ResponseSchemas map[int]json.RawMessage
	RetryNever      bool
	RetryAmbiguous  bool
}

// executeOnce performs one HTTP request; execute owns the bounded retry loop.
func executeOnce(ctx context.Context, s service, op operation, input any, output any) (*Response, error) {
	if s.transport.secretKey == "" {
		return nil, ErrNotConfigured
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, &TransportError{Kind: "request encoding failed", Cause: err}
	}
	values := map[string]json.RawMessage{}
	if err = json.Unmarshal(encoded, &values); err != nil {
		return nil, &TransportError{Kind: "request encoding failed", Cause: err}
	}
	path := op.Path
	query := url.Values{}
	headers := http.Header{}
	for _, p := range op.Parameters {
		raw, exists := values[p.Name]
		if p.In == "header" && s.user.id != "" {
			if p.Name == "x-end-user-id" {
				raw, _ = json.Marshal(s.user.id)
				exists = true
			}
			if p.Name == "x-end-user-timezone" {
				exists = s.user.timezone != ""
				raw, _ = json.Marshal(s.user.timezone)
			}
		}
		if !exists || string(raw) == "null" || ((p.In == "header" || p.In == "path") && string(raw) == `""`) {
			if p.Required {
				return nil, fmt.Errorf("%w: %s is required", ErrInvalidInput, p.Name)
			}
			continue
		}
		if err := validateRaw(raw, p.Schema, p.Name); err != nil {
			return nil, err
		}
		parts, err := parameterStrings(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid %s", ErrInvalidInput, p.Name)
		}
		switch p.In {
		case "path":
			value := url.PathEscape(strings.Join(parts, ","))
			if value == "." || value == ".." {
				value = strings.ReplaceAll(value, ".", "%2E")
			}
			path = strings.ReplaceAll(path, "{"+p.Name+"}", value)
		case "query":
			if p.Explode {
				for _, part := range parts {
					query.Add(p.Name, part)
				}
			} else {
				query.Set(p.Name, strings.Join(parts, ","))
			}
		case "header":
			headers.Set(p.Name, strings.Join(parts, ","))
		}
	}
	body := map[string]json.RawMessage{}
	for _, key := range op.BodyFields {
		if v, ok := values[key]; ok {
			body[key] = v
		}
	}
	var reader io.Reader
	if len(op.BodyFields) > 0 || op.RequiredBody {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, &TransportError{Kind: "request encoding failed", Cause: err}
		}
		if err := validateRaw(b, op.BodySchema, "body"); err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
		headers.Set("Content-Type", "application/json")
	}
	u := s.transport.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, op.Method, u, reader)
	if err != nil {
		return nil, &TransportError{Kind: "request creation failed", Cause: err}
	}
	request.Header = headers
	request.Header.Set("Authorization", "Bearer "+s.transport.secretKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "january-go/0.0.0")
	response, err := s.transport.httpClient.Do(request)
	if err != nil {
		return nil, &TransportError{Kind: "HTTP request failed", Cause: err}
	}
	defer response.Body.Close()
	metadata := &Response{StatusCode: response.StatusCode, Headers: response.Header.Clone(), RequestID: response.Header.Get("X-Request-ID"), RetryAfter: response.Header.Get("Retry-After")}
	if metadata.RequestID == "" {
		metadata.RequestID = response.Header.Get("Request-ID")
	}
	metadata.Headers.Del("Authorization")
	metadata.Headers.Del("Set-Cookie")
	for key, values := range metadata.Headers {
		for i, value := range values {
			values[i] = redactDiagnostic(value, s.transport.secretKey)
		}
		metadata.Headers[key] = values
	}
	metadata.RequestID = redactDiagnostic(metadata.RequestID, s.transport.secretKey)
	metadata.RetryAfter = redactDiagnostic(metadata.RetryAfter, s.transport.secretKey)
	if v := response.Header.Get("X-Revoked-Count"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			metadata.RevokedCount = &n
		}
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return metadata, &TransportError{Kind: "response read failed", Cause: err}
	}
	if len(b) > maxResponseBytes {
		return metadata, &TransportError{Kind: "response exceeds size limit"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var wire struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			DocsURL   string `json:"docs_url"`
			RequestID string `json:"request_id"`
		}
		_ = json.Unmarshal(b, &wire)
		if metadata.RequestID == "" {
			metadata.RequestID = wire.RequestID
		}
		redact := func(v string) string { return redactDiagnostic(v, s.transport.secretKey) }
		metadata.RequestID = redact(metadata.RequestID)
		message := redact(wire.Message)
		if len(message) > 200 {
			message = message[:200] + "... (truncated; see Body)"
		}
		return metadata, &APIError{StatusCode: response.StatusCode, Code: redact(wire.Code), Message: message, DocsURL: redact(wire.DocsURL), RequestID: metadata.RequestID, Response: metadata, Body: redact(string(b))}
	}
	if output != nil && response.StatusCode != http.StatusNoContent {
		if err = validateResponseRequired(b, op.ResponseSchemas[response.StatusCode]); err != nil {
			return metadata, &TransportError{Kind: "invalid JSON response", Cause: err}
		}
		if err = json.Unmarshal(b, output); err != nil {
			return metadata, &TransportError{Kind: "invalid JSON response", Cause: err}
		}
	}
	return metadata, nil
}

func parameterStrings(raw json.RawMessage) ([]string, error) {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	scalar := func(x any) (string, error) {
		switch n := x.(type) {
		case string:
			return n, nil
		case json.Number:
			return string(n), nil
		case bool:
			return strconv.FormatBool(n), nil
		default:
			return "", ErrInvalidInput
		}
	}
	if a, ok := v.([]any); ok {
		r := make([]string, len(a))
		for i, x := range a {
			var err error
			r[i], err = scalar(x)
			if err != nil {
				return nil, err
			}
		}
		return r, nil
	}
	s, err := scalar(v)
	return []string{s}, err
}

// HTTPClientTokenIssuerConfig retains the prototype constructor.
type HTTPClientTokenIssuerConfig struct {
	SecretKey       string
	BaseURL         string
	ClientTokenPath string
	HTTPClient      *http.Client
}

func NewHTTPClientTokenIssuer(config HTTPClientTokenIssuerConfig) (ClientTokenIssuer, error) {
	if strings.TrimSpace(config.SecretKey) == "" {
		return nil, ErrNotConfigured
	}
	c, err := NewClient(Config{SecretKey: config.SecretKey, BaseURL: config.BaseURL, ClientTokenPath: config.ClientTokenPath, HTTPClient: config.HTTPClient})
	if err != nil {
		return nil, err
	}
	return c.ClientTokens, nil
}
