package january

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func createLog(c *Client, operationID string) error {
	var err error
	if operationID == "createWaterLog" {
		_, _, err = c.WaterLogs.Create(context.Background(), CreateWaterLogRequest{Amount: WaterAmount{Value: 8, Unit: VolumeUnitFlOz}})
	} else {
		_, _, err = c.WeightLogs.Create(context.Background(), CreateWeightLogRequest{Weight: Weight{Value: 150, Unit: WeightUnitLb}})
	}
	return err
}

// A 429 rate_limited reply is a definitive rejection: the API did not process the
// request, so nothing was recorded. The create is retried within the retry budget
// and the fake server still ends with exactly one log.
func TestRateLimitedLogCreatesAreRetriedWithoutDuplicates(t *testing.T) {
	for _, operationID := range []string{"createWaterLog", "createWeightLog"} {
		var calls, created atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) <= 2 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"code":"rate_limited","message":"slow down"}`)
				return
			}
			created.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(fixtureFor(t, operationID).Response.Body)
		}))
		c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL})
		err := createLog(c, operationID)
		server.Close()
		if err != nil || calls.Load() != 3 || created.Load() != 1 {
			t.Fatalf("%s: %d requests, %d logs, %v", operationID, calls.Load(), created.Load(), err)
		}
	}
}

func TestRateLimitedLogCreatesStopAtTheRetryBudget(t *testing.T) {
	for _, operationID := range []string{"createWaterLog", "createWeightLog"} {
		for _, tc := range []struct {
			retries    int
			retryAfter string
			calls      int32
		}{
			{retries: 2, retryAfter: "0", calls: 3},
			{retries: 0, retryAfter: "0", calls: 1},
			// A Retry-After beyond 60 seconds is never waited out.
			{retries: 2, retryAfter: "61", calls: 1},
		} {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", tc.retryAfter)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"code":"rate_limited","message":"slow down"}`)
			}))
			c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL, MaxRetries: Value(tc.retries)})
			err := createLog(c, operationID)
			server.Close()
			if !errors.Is(err, ErrRateLimit) || calls.Load() != tc.calls {
				t.Fatalf("%s %+v: %d requests, %v", operationID, tc, calls.Load(), err)
			}
		}
	}
}

// Failures that leave the outcome unknown are never replayed for the new creates:
// the server may already have recorded the log, so a second request could record
// it twice.
func TestAmbiguousLogCreateFailuresAreNotReplayed(t *testing.T) {
	for _, operationID := range []string{"createWaterLog", "createWeightLog"} {
		for _, failure := range []string{"lost response", "timeout", "502", "504"} {
			var calls, created atomic.Int32
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.ReadAll(r.Body)
				created.Add(1) // The log is recorded before the reply goes missing.
				switch failure {
				case "lost response":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
				case "timeout":
					<-release
				case "502":
					w.WriteHeader(http.StatusBadGateway)
					_, _ = io.WriteString(w, `{"code":"upstream_error","message":"try later"}`)
				case "504":
					w.WriteHeader(http.StatusGatewayTimeout)
					_, _ = io.WriteString(w, `{"code":"upstream_timeout","message":"try later"}`)
				}
			}))
			c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL, Timeout: 200 * time.Millisecond})
			err := createLog(c, operationID)
			close(release)
			server.Close()
			if err == nil || calls.Load() != 1 || created.Load() != 1 {
				t.Fatalf("%s %s: %d requests, %d logs, %v", operationID, failure, calls.Load(), created.Load(), err)
			}
		}
	}
	// The same lost response on an idempotent read is retried, so the guard above is
	// specific to the non-idempotent creates.
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		_, _ = w.Write(fixtureFor(t, "listWaterLogs").Response.Body)
	}))
	defer server.Close()
	c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL})
	if _, _, err := c.WaterLogs.List(context.Background(), ListWaterLogsRequest{StartDate: "2026-09-10", EndDate: "2026-09-10", Timezone: "UTC", Unit: VolumeUnitFlOz}); err != nil || calls.Load() != 2 {
		t.Fatalf("idempotent list not retried: %d, %v", calls.Load(), err)
	}
}

// Each water unit has its own accepted range; a value outside it is rejected
// before any request is sent.
func TestWaterAmountRangeDependsOnUnit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(fixtureFor(t, "createWaterLog").Response.Body)
	}))
	defer server.Close()
	c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL, MaxRetries: Value(0)})
	create := func(value float64, unit VolumeUnit) error {
		_, _, err := c.WaterLogs.Create(context.Background(), CreateWaterLogRequest{Amount: WaterAmount{Value: value, Unit: unit}})
		return err
	}
	for _, tc := range []struct {
		unit              VolumeUnit
		accepted, refused []float64
	}{
		{VolumeUnitFlOz, []float64{1, 8, 811.5}, []float64{0.5, 0.999, 811.51, 1000}},
		{VolumeUnitMl, []float64{30, 250, 24000}, []float64{1, 29.9, 24000.01}},
		{VolumeUnitCup, []float64{0.125, 1, 101.4}, []float64{0.124, 101.41, 811.5}},
	} {
		for _, value := range tc.accepted {
			before := calls.Load()
			if err := create(value, tc.unit); err != nil || calls.Load() != before+1 {
				t.Errorf("%v %s refused: %v", value, tc.unit, err)
			}
		}
		for _, value := range tc.refused {
			before := calls.Load()
			err := create(value, tc.unit)
			if !errors.Is(err, ErrInvalidInput) || calls.Load() != before {
				t.Errorf("%v %s accepted or sent: %v", value, tc.unit, err)
			}
		}
	}
	if err := create(0.5, VolumeUnitFlOz); err == nil || !strings.Contains(err.Error(), "body.amount.value must be from 1 through 811.5 fl_oz") {
		t.Fatalf("unit range not named: %v", err)
	}
}

// OpenAPI 3.0 exclusive bounds: a quantity must be greater than zero, not merely
// at least zero.
func TestExclusiveMinimumRejectsZeroQuantity(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(fixtureFor(t, "createFoodLog").Response.Body)
	}))
	defer server.Close()
	c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL, MaxRetries: Value(0)})
	create := func(quantity float64) error {
		_, _, err := c.FoodLogs.Create(context.Background(), CreateFoodLogRequest{Foods: []FoodLogInputFood{{FoodID: "84222716", ServingID: "67943292", Quantity: quantity}}})
		return err
	}
	for _, quantity := range []float64{0, -1} {
		if err := create(quantity); !errors.Is(err, ErrInvalidInput) || calls.Load() != 0 {
			t.Fatalf("quantity %v accepted: %v", quantity, err)
		}
	}
	if err := create(0.001); err != nil || calls.Load() != 1 {
		t.Fatalf("positive quantity refused: %v", err)
	}
	for _, tc := range []struct {
		rule  string
		value string
		ok    bool
	}{
		{`{"type":"number","minimum":0,"exclusiveMinimum":true}`, "0", false},
		{`{"type":"number","minimum":0,"exclusiveMinimum":false}`, "0", true},
		{`{"type":"number","maximum":5,"exclusiveMaximum":true}`, "5", false},
		{`{"type":"number","maximum":5,"exclusiveMaximum":true}`, "4.99", true},
	} {
		if err := validateRaw(json.RawMessage(tc.value), json.RawMessage(tc.rule), "value"); (err == nil) != tc.ok {
			t.Errorf("%s with %s: %v", tc.rule, tc.value, err)
		}
	}
}

// A calendar date that does not exist is rejected, not rolled into the next month.
func TestImpossibleCalendarDatesAreRejected(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write(fixtureFor(t, "listWaterLogs").Response.Body)
	}))
	defer server.Close()
	c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL, MaxRetries: Value(0)})
	list := func(day string) error {
		_, _, err := c.WaterLogs.List(context.Background(), ListWaterLogsRequest{StartDate: day, EndDate: day, Timezone: "UTC", Unit: VolumeUnitMl})
		return err
	}
	for _, day := range []string{"2026-02-31", "2026-02-29", "2026-04-31", "2026-13-01", "2026-00-10"} {
		if err := list(day); !errors.Is(err, ErrInvalidInput) || calls.Load() != 0 {
			t.Fatalf("%s accepted: %v", day, err)
		}
	}
	if err := list("2028-02-29"); err != nil || calls.Load() != 1 {
		t.Fatalf("leap day refused: %v", err)
	}
}

// The client-token facade accepts every scope the contract lists, including the
// water and weight log scopes, and sends them unchanged.
func TestClientTokenFacadeAcceptsLogScopes(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(fixtureFor(t, "createClientToken").Response.Body)
	}))
	defer server.Close()
	c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL})
	scopes := []string{ScopeWaterLogsRead, ScopeWaterLogsWrite, ScopeWeightLogsRead, ScopeWeightLogsWrite}
	if _, err := c.ClientTokens.Create(context.Background(), CreateClientTokenInput{EndUserID: "user", Scopes: scopes}); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal(body, &sent); err != nil || strings.Join(sent.Scopes, ",") != strings.Join(scopes, ",") {
		t.Fatalf("scopes not sent: %s", body)
	}
}
