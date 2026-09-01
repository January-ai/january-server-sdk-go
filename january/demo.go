package january

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type DemoClientTokenIssuerConfig struct {
	AccessToken string
	ExpiresIn   time.Duration
	Now         func() time.Time
}

type demoClientTokenIssuer struct {
	accessToken string
	expiresIn   time.Duration
	now         func() time.Time
}

func NewDemoClientTokenIssuer(config DemoClientTokenIssuerConfig) (ClientTokenIssuer, error) {
	token := strings.TrimSpace(config.AccessToken)
	if token == "" {
		return nil, fmt.Errorf("%w: demo AccessToken must be non-empty", ErrNotConfigured)
	}
	if strings.HasPrefix(token, "sk-") {
		return nil, fmt.Errorf("%w: refusing to expose an sk- secret as a demo client token", ErrNotConfigured)
	}
	expiresIn := config.ExpiresIn
	if expiresIn == 0 {
		expiresIn = time.Hour
	}
	if expiresIn < time.Second || expiresIn%time.Second != 0 {
		return nil, fmt.Errorf("%w: demo ExpiresIn must be a positive whole number of seconds", ErrNotConfigured)
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &demoClientTokenIssuer{accessToken: token, expiresIn: expiresIn, now: now}, nil
}

func (d *demoClientTokenIssuer) Create(_ context.Context, input CreateClientTokenInput) (ClientToken, error) {
	if err := validateCreateInput(input); err != nil {
		return ClientToken{}, err
	}
	expiresAt := d.now().UTC().Add(d.expiresIn)
	return ClientToken{
		Token:     d.accessToken,
		ExpiresIn: int64(d.expiresIn / time.Second),
		ExpiresAt: expiresAt.Format("2006-01-02T15:04:05.000Z"),
		EndUserID: strings.TrimSpace(input.EndUserID),
		Scopes:    append([]string(nil), input.Scopes...),
	}, nil
}
