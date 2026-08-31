// This executable uses ONLY an in-process localhost mock and synthetic data.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/January-ai/january-server-sdk-go/january"
)

func main() {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-local-example" {
			http.Error(w, "missing synthetic credential", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "local-example-request")
		switch requests.Add(1) {
		case 1:
			if r.Header.Get("X-End-User-ID") != "example-user" {
				http.Error(w, "missing scoped user", 400)
				return
			}
			_, _ = io.WriteString(w, `{"total_count":0,"items":[]}`)
		case 2:
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"token":"ct-local-example","expires_in":1800,"expires_at":"2026-08-30T18:30:00Z","end_user_id":"example-user","scopes":["foods:read"]}`)
		case 3:
			if r.Method != http.MethodDelete || r.URL.Query().Get("end_user_id") != "example-user" {
				http.Error(w, "wrong revoke request", 400)
				return
			}
			w.Header().Set("X-Revoked-Count", "500")
			w.WriteHeader(http.StatusNoContent)
		case 4:
			_, _ = io.WriteString(w, `{"plan":"local","period_start":"2026-08-01","period_end":"2026-08-31","resets_at":"2026-09-01T00:00:00Z","used_credits":0}`)
		default:
			http.Error(w, "unexpected retry or revoke loop", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	c, err := january.NewClient(january.Config{SecretKey: "sk-local-example", BaseURL: server.URL, Timeout: 5 * time.Second})
	if err != nil {
		log.Fatal(err)
	}
	user, err := c.ForUser("example-user")
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	foods, _, err := user.Foods.Search(ctx, january.SearchFoodsRequest{Query: "banana"})
	if err != nil {
		log.Fatal(err)
	}
	token, _, err := c.MintClientToken(ctx, january.MintClientTokenRequest{EndUserID: "example-user", Scopes: january.Value([]string{"foods:read"})})
	if err != nil {
		log.Fatal(err)
	}
	revoked, err := c.RevokeClientTokens(ctx, january.RevokeClientTokensRequest{EndUserID: "example-user"})
	if err != nil {
		log.Fatal(err)
	}
	credits, _, err := c.Credits(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if requests.Load() != 4 || revoked.RevokedCount == nil || *revoked.RevokedCount != 500 {
		log.Fatal("expected four calls and one revoke")
	}
	fmt.Printf("Local HTTP consumer passed: %d foods, token expires in %.0fs, one DELETE revoked %d, plan %s, %d requests.\n", len(foods.Items), token.ExpiresIn, *revoked.RevokedCount, credits.Plan, requests.Load())
}
