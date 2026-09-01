package january

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPContract(t *testing.T) {
	var requests []map[string]json.RawMessage
	payload := `{"token":"ct-fixture","expires_in":300,"expires_at":"2026-08-30T18:30:00Z","end_user_id":"user","scopes":["foods:read"],"future_field":{"enabled":true}}`
	status := http.StatusCreated
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1.2/auth/client-tokens" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Errorf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests = append(requests, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()
	client, err := NewClient(Config{SecretKey: "fixture", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ttl := 600
	token, err := client.ClientTokens.Create(context.Background(), CreateClientTokenInput{EndUserID: " user ", Scopes: []ClientScope{ScopeFoodsRead}, TTLSeconds: &ttl})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(token)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"end_user_id":"user","expires_at":"2026-08-30T18:30:00Z","expires_in":300,"scopes":["foods:read"],"token":"ct-fixture"}` {
		t.Fatalf("wrong relay shape: %s", encoded)
	}
	if string(requests[0]["end_user_id"]) != `"user"` || string(requests[0]["ttl_seconds"]) != "600" || string(requests[0]["scopes"]) != `["foods:read"]` {
		t.Fatalf("wrong request: %s", requests[0])
	}
	_, err = client.ClientTokens.Create(context.Background(), CreateClientTokenInput{EndUserID: "user", Scopes: []string{ScopeFoodsRead}})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests[1]) != 2 {
		t.Fatalf("optional values were not omitted: %v", requests[1])
	}
	for _, invalid := range []string{
		`{"token":"ct-fixture","expires_in":"300"}`,
		`{"token":"ct-fixture","expires_in":true}`,
		`{"token":"ct-fixture","expires_in":null}`,
		`{"token":"ct-fixture","expires_in":300.5}`,
		`{"expires_in":300}`,
		`{"access_token":"old","expires_in":300}`,
	} {
		payload = invalid
		_, err := client.ClientTokens.Create(context.Background(), CreateClientTokenInput{EndUserID: "user", Scopes: []string{ScopeFoodsRead}})
		var apiError *APIError
		if !errors.As(err, &apiError) || apiError.StatusCode != 502 {
			t.Fatalf("accepted invalid payload %s: %v", invalid, err)
		}
	}
	status = http.StatusTooManyRequests
	payload = `{"message":"Try later","code":"rate_limited"}`
	count := len(requests)
	_, err = client.ClientTokens.Create(context.Background(), CreateClientTokenInput{EndUserID: "user", Scopes: []string{ScopeFoodsRead}})
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Code != "rate_limited" {
		t.Fatalf("lost API error: %v", err)
	}
	if len(requests) != count+1 {
		t.Fatal("issuance must not automatically retry")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.ClientTokens.Create(ctx, CreateClientTokenInput{EndUserID: "user", Scopes: []string{ScopeFoodsRead}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func TestRequestValidation(t *testing.T) {
	zero, high := 0, 7201
	for _, input := range []CreateClientTokenInput{
		{EndUserID: "user", Scopes: []ClientScope{}},
		{EndUserID: "user", Scopes: []ClientScope{"unknown"}},
		{EndUserID: "user", Scopes: []ClientScope{ScopeFoodsRead, ScopeFoodsRead, ScopeFoodsRead, ScopeFoodsRead, ScopeFoodsRead, ScopeFoodsRead, ScopeFoodsRead}},
		{EndUserID: "user", TTLSeconds: &zero},
		{EndUserID: "user", TTLSeconds: &high},
		{EndUserID: strings.Repeat("😀", 33)},
	} {
		if !errors.Is(validateCreateInput(input), ErrInvalidInput) {
			t.Fatalf("accepted invalid input: %#v", input)
		}
	}
	if err := validateCreateInput(CreateClientTokenInput{EndUserID: " " + strings.Repeat("😀", 32) + " ", Scopes: []string{ScopeFoodsRead}}); err != nil {
		t.Fatal(err)
	}
}
