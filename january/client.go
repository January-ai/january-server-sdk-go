// Package january provides the January server SDK for trusted backends.
package january

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NewClient creates a reusable, concurrency-safe client. Configuration is copied.
// A legacy demo issuer can be used without a key but cannot call HTTP operations.
func NewClient(config Config) (*Client, error) {
	key := strings.TrimSpace(config.SecretKey)
	if (config.SecretKey != "" && key == "") || strings.HasPrefix(key, "ct-") {
		return nil, fmt.Errorf("%w: provide a server secret, not a client token", ErrNotConfigured)
	}
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: invalid BaseURL", ErrNotConfigured)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return nil, fmt.Errorf("%w: HTTPS is required except on localhost", ErrNotConfigured)
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if timeout < 0 {
		return nil, fmt.Errorf("%w: Timeout must be positive", ErrNotConfigured)
	}
	hc := http.Client{}
	if config.HTTPClient != nil {
		hc = *config.HTTPClient
	}
	// Do not forward credentials through redirects. Never mutate an injected client.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	t := &transport{secretKey: key, baseURL: strings.TrimRight(baseURL, "/"), httpClient: &hc, timeout: timeout}
	c := newGeneratedClient(t)
	c.ClientTokens = &ClientTokensService{client: c, issuer: config.ClientTokenIssuer}
	if config.ClientTokenPath != "" {
		if !strings.HasPrefix(config.ClientTokenPath, "/") || strings.ContainsAny(config.ClientTokenPath, "?#") {
			return nil, fmt.Errorf("%w: ClientTokenPath must be an absolute path", ErrNotConfigured)
		}
		c.ClientTokens.pathOverride = config.ClientTokenPath
	}
	return c, nil
}

// ForUser returns an immutable user-scoped view without privileged root methods.
// The optional timezone applies only where declared by the contract.
func (c *Client) ForUser(endUserID string, timezone ...string) (*UserClient, error) {
	if strings.TrimSpace(endUserID) == "" || strings.ContainsAny(endUserID, "\r\n") || len(timezone) > 1 {
		return nil, fmt.Errorf("%w: a nonblank, header-safe end-user ID is required", ErrInvalidInput)
	}
	u := userContext{id: endUserID}
	if len(timezone) == 1 {
		if _, err := time.LoadLocation(timezone[0]); err != nil {
			return nil, fmt.Errorf("%w: invalid end-user timezone", ErrInvalidInput)
		}
		u.timezone = timezone[0]
	}
	return newGeneratedUserClient(c.transport, u), nil
}

// ClientTokensService preserves the prototype API. Prefer Client.MintClientToken.
type ClientTokensService struct {
	client       *Client
	issuer       ClientTokenIssuer
	pathOverride string
}

// Create is the deprecated compatibility alias for MintClientToken.
func (s *ClientTokensService) Create(ctx context.Context, input CreateClientTokenInput) (ClientToken, error) {
	if err := validateCreateInput(input); err != nil {
		return ClientToken{}, err
	}
	if s.issuer != nil {
		return s.issuer.Create(ctx, input)
	}
	return s.client.mintLegacy(ctx, input, s.pathOverride)
}
func (c *Client) String() string   { return "january.Client{credentials:[REDACTED]}" }
func (c *Client) GoString() string { return c.String() }
