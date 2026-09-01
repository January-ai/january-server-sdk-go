package january

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDemoIssuerReturnsStableTokenShape(t *testing.T) {
	issuer, err := NewDemoClientTokenIssuer(DemoClientTokenIssuerConfig{
		AccessToken: "demo-token",
		ExpiresIn:   5 * time.Minute,
		Now: func() time.Time {
			return time.Date(2026, 8, 22, 18, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Config{ClientTokenIssuer: issuer})
	if err != nil {
		t.Fatal(err)
	}

	token, err := client.ClientTokens.Create(context.Background(), CreateClientTokenInput{EndUserID: "user-123", Scopes: []string{ScopeFoodsRead}})
	if err != nil {
		t.Fatal(err)
	}
	if token.Token != "demo-token" || token.ExpiresIn != 300 || token.EndUserID != "user-123" || len(token.Scopes) != 1 {
		t.Fatalf("unexpected token: %#v", token)
	}
	if token.ExpiresAt != "2026-08-22T18:05:00.000Z" {
		t.Fatalf("unexpected expiry: %s", token.ExpiresAt)
	}
}

func TestRejectsMissingAuthenticatedUser(t *testing.T) {
	issuer, err := NewDemoClientTokenIssuer(DemoClientTokenIssuerConfig{AccessToken: "demo-token"})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Config{ClientTokenIssuer: issuer})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ClientTokens.Create(context.Background(), CreateClientTokenInput{EndUserID: " "})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestRejectsSKSecretInDemoMode(t *testing.T) {
	_, err := NewDemoClientTokenIssuer(DemoClientTokenIssuerConfig{AccessToken: "sk-do-not-expose"})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestMissingIssuerFailsClearly(t *testing.T) {
	client, err := NewClient(Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ClientTokens.Create(context.Background(), CreateClientTokenInput{EndUserID: "user-123", Scopes: []string{ScopeFoodsRead}})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}
