package january

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type contractFixture struct {
	OperationID string
	Method      string
	Path        string
	Request     struct {
		Parameters map[string]map[string]json.RawMessage
		Body       json.RawMessage
	}
	Response wireFixture
}
type wireFixture struct {
	Status  int
	Headers map[string]string
	Body    json.RawMessage
}
type fixtureBundle struct {
	Operations      []contractFixture
	Errors          []wireFixture
	UncappedCredits json.RawMessage
}

func fixtures(t *testing.T) fixtureBundle {
	t.Helper()
	b, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixtureBundle
	if err = json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}
func equalJSON(t *testing.T, a, b []byte) {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(x, y) {
		t.Errorf("JSON mismatch:\ngot %s\nwant %s", a, b)
	}
}
func TestAll18ContractFixtures(t *testing.T) {
	bundle := fixtures(t)
	if len(bundle.Operations) != 18 {
		t.Fatalf("expected 18 fixtures, got %d", len(bundle.Operations))
	}
	for _, fixture := range bundle.Operations {
		t.Run(fixture.OperationID, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != fixture.Method {
					t.Errorf("method = %s", r.Method)
				}
				if r.Header.Get("Authorization") != "Bearer sk-local-fixture" {
					t.Error("missing secret-key auth")
				}
				if !strings.HasPrefix(r.Header.Get("User-Agent"), "january-go/") {
					t.Error("missing user agent")
				}
				expectedPath := fixture.Path
				for key, value := range fixture.Request.Parameters["path"] {
					parts, _ := parameterStrings(value)
					expectedPath = strings.ReplaceAll(expectedPath, "{"+key+"}", url.PathEscape(parts[0]))
				}
				if r.URL.EscapedPath() != expectedPath {
					t.Errorf("path = %s, want %s", r.URL.EscapedPath(), expectedPath)
				}
				expectedQuery := url.Values{}
				for key, value := range fixture.Request.Parameters["query"] {
					parts, _ := parameterStrings(value)
					for _, part := range parts {
						expectedQuery.Add(key, part)
					}
				}
				if r.URL.RawQuery != expectedQuery.Encode() {
					t.Errorf("query = %s, want %s", r.URL.RawQuery, expectedQuery.Encode())
				}
				for key, value := range fixture.Request.Parameters["header"] {
					parts, _ := parameterStrings(value)
					if r.Header.Get(key) != parts[0] {
						t.Errorf("header %s mismatch", key)
					}
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if len(fixture.Request.Body) > 0 {
					equalJSON(t, body, fixture.Request.Body)
					if r.Header.Get("Content-Type") != "application/json" {
						t.Error("missing JSON content type")
					}
				} else if len(body) > 0 {
					t.Error("unexpected request body")
				}
				for key, value := range fixture.Response.Headers {
					w.Header().Set(key, value)
				}
				w.WriteHeader(fixture.Response.Status)
				if fixture.Response.Status != 204 {
					_, _ = w.Write(fixture.Response.Body)
				}
			}))
			defer server.Close()
			c, err := NewClient(Config{SecretKey: "sk-local-fixture", BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			input := map[string]json.RawMessage{}
			if len(fixture.Request.Body) > 0 {
				if err = json.Unmarshal(fixture.Request.Body, &input); err != nil {
					t.Fatal(err)
				}
			}
			for _, params := range fixture.Request.Parameters {
				for key, value := range params {
					input[key] = value
				}
			}
			raw, _ := json.Marshal(input)
			result, metadata, err := invokeFixture(c, fixture.OperationID, raw)
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatalf("expected ONE HTTP request, got %d", calls.Load())
			}
			if metadata.StatusCode != fixture.Response.Status || metadata.RequestID == "" {
				t.Fatalf("missing response metadata: %#v", metadata)
			}
			if fixture.Response.Status == 204 {
				if metadata.RevokedCount == nil || *metadata.RevokedCount != 3 {
					t.Fatal("missing revoked count")
				}
			} else {
				resultJSON, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				equalJSON(t, resultJSON, fixture.Response.Body)
			}
		})
	}
}
func TestErrorFixturesAndNoRetries(t *testing.T) {
	for _, fixture := range fixtures(t).Errors {
		t.Run(fmt.Sprint(fixture.Status), func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				for k, v := range fixture.Headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(fixture.Status)
				_, _ = w.Write(fixture.Body)
			}))
			defer server.Close()
			c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL})
			_, metadata, err := c.Credits(context.Background())
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("expected APIError, got %v", err)
			}
			var wire struct {
				Code    string
				Message string
				DocsURL string `json:"docs_url"`
			}
			_ = json.Unmarshal(fixture.Body, &wire)
			if apiErr.StatusCode != fixture.Status || apiErr.Code != wire.Code || apiErr.DocsURL != wire.DocsURL || apiErr.Message != wire.Message {
				t.Fatal("structured error details lost")
			}
			if metadata == nil || count.Load() != 1 {
				t.Fatal("missing metadata or unexpected retry")
			}
		})
	}
}
func TestUnknownEnumsNullAndUnset(t *testing.T) {
	var token ClientTokenResponseDto
	if err := json.Unmarshal([]byte(`{"token":"ct-secret","expires_in":300,"expires_at":"later","end_user_id":"a","scopes":["future:scope"],"new_field":true}`), &token); err != nil {
		t.Fatal(err)
	}
	if token.Scopes[0] != "future:scope" {
		t.Fatal("unknown enum lost")
	}
	for _, printed := range []string{fmt.Sprint(token), fmt.Sprintf("%+v", token), fmt.Sprintf("%#v", token)} {
		if strings.Contains(printed, "ct-secret") {
			t.Fatal("token leaked")
		}
	}
	unset, _ := json.Marshal(UpdateFoodLogRequest{LogID: "log"})
	null, _ := json.Marshal(UpdateFoodLogRequest{LogID: "log", Name: Null[string]()})
	value, _ := json.Marshal(UpdateFoodLogRequest{LogID: "log", Name: Value("")})
	if strings.Contains(string(unset), `"name"`) || !strings.Contains(string(null), `"name":null`) || !strings.Contains(string(value), `"name":""`) {
		t.Fatal("unset/null/value distinction lost")
	}
	var roundtrip UpdateFoodLogRequest
	_ = json.Unmarshal(null, &roundtrip)
	if !roundtrip.Name.IsNull() {
		t.Fatal("explicit null not decoded")
	}
	_ = json.Unmarshal([]byte("{}"), &roundtrip)
	// Reusing json.Unmarshal targets follows normal Go merge behavior; fresh models retain omission.
	var fresh UpdateFoodLogRequest
	_ = json.Unmarshal([]byte("{}"), &fresh)
	if fresh.Name.IsSet() {
		t.Fatal("omitted field unexpectedly set")
	}
}
func TestUncappedCredits(t *testing.T) {
	fixture := fixtures(t).UncappedCredits
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fixture)
	}))
	defer server.Close()
	c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL})
	result, _, err := c.Credits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.RemainingCredits.IsSet() || result.IncludedCredits.IsSet() {
		t.Fatal("uncapped credits must be absent, not zero")
	}
}
func TestConcurrentUserIsolation(t *testing.T) {
	var seen sync.Map
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("X-End-User-ID"), true)
		_, _ = io.WriteString(w, `{"total_count":0,"items":[]}`)
	}))
	defer server.Close()
	c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("user-%d", i)
		if i == 19 {
			id = strings.Repeat("u", 100)
		}
		user, err := c.ForUser(id)
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(u *UserClient) {
			defer wg.Done()
			_, _, err := u.Foods.Search(context.Background(), SearchFoodsRequest{Query: "banana", EndUserID: "attacker"})
			if err != nil {
				t.Error(err)
			}
		}(user)
	}
	wg.Wait()
	count := 0
	seen.Range(func(k, v any) bool {
		count++
		if k == "attacker" {
			t.Error("override leaked")
		}
		return true
	})
	if count != 20 {
		t.Fatalf("expected 20 distinct identities, got %d", count)
	}
	_, _, err := c.Foods.Search(context.Background(), SearchFoodsRequest{Query: "banana"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := seen.Load(""); !ok {
		t.Fatal("root client was mutated")
	}
	typ := reflect.TypeOf(&UserClient{})
	for _, method := range []string{"MintClientToken", "RevokeClientTokens", "Credits"} {
		if _, ok := typ.MethodByName(method); ok {
			t.Fatalf("privileged method on user view: %s", method)
		}
	}
}
func TestCancellationTimeoutAndInjectedClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	injected := &http.Client{Timeout: time.Second}
	c, err := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL, HTTPClient: injected, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if injected.CheckRedirect != nil || injected.Timeout != time.Second {
		t.Fatal("injected client mutated")
	}
	_, _, err = c.Credits(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout cause lost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = c.Credits(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation cause lost: %v", err)
	}
}
func TestPathEncodingAndDescriptionParameter(t *testing.T) {
	var phase atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if phase.Load() == 0 {
			if r.URL.EscapedPath() != "/v1.2/food-logs/a%2Fb%3F%23%252F" {
				t.Errorf("unsafe path: %s", r.URL.EscapedPath())
			}
			_, _ = io.WriteString(w, `{"status":"deleted"}`)
		} else {
			b, _ := io.ReadAll(r.Body)
			equalJSON(t, b, []byte(`{"text":"two eggs"}`))
			_, _ = io.WriteString(w, `{"detections":[]}`)
		}
	}))
	defer server.Close()
	c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL})
	_, _, err := c.FoodLogs.Delete(context.Background(), DeleteFoodLogRequest{LogID: "a/b?#%2F", EndUserID: "u"})
	if err != nil {
		t.Fatal(err)
	}
	phase.Store(1)
	_, _, err = c.FoodAnalysis.AnalyzeDescription(context.Background(), SearchFoodsByNaturalLanguageRequest{Query: "two eggs"})
	if err != nil {
		t.Fatal(err)
	}
}
func TestSecretSafetyAndSingleRevoke(t *testing.T) {
	for _, config := range []Config{{SecretKey: "ct-client"}, {SecretKey: "sk-secret", BaseURL: "http://example.com"}, {SecretKey: "sk-secret", BaseURL: "https://user:pass@example.com"}} {
		if _, err := NewClient(config); err == nil {
			t.Fatal("unsafe config accepted")
		}
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("X-Revoked-Count", "500")
		w.WriteHeader(204)
	}))
	defer server.Close()
	c, _ := NewClient(Config{SecretKey: "sk-secret", BaseURL: server.URL})
	metadata, err := c.RevokeClientTokens(context.Background(), RevokeClientTokensRequest{EndUserID: "u ?&#"})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || metadata.RevokedCount == nil || *metadata.RevokedCount != 500 {
		t.Fatal("revocation must make ONE DELETE even at the server batch limit")
	}
	if strings.Contains(fmt.Sprintf("%#v", c), "sk-secret") {
		t.Fatal("client secret leaked")
	}
	if _, err := c.ForUser("bad\nheader"); err == nil {
		t.Fatal("unsafe context accepted")
	}
}
func TestRedirectDoesNotForwardSecret(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	c, _ := NewClient(Config{SecretKey: "sk-secret", BaseURL: redirect.URL})
	_, _, err := c.Credits(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 307 || targetCalls.Load() != 0 {
		t.Fatal("redirect forwarded credential")
	}
}
