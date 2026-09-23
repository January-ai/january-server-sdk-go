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
)

func fixtureFor(t *testing.T, operationID string) contractFixture {
	t.Helper()
	for _, fixture := range fixtures(t).Operations {
		if fixture.OperationID == operationID {
			return fixture
		}
	}
	t.Fatalf("missing fixture %s", operationID)
	return contractFixture{}
}

func TestScanCorrectionCarriesEveryField(t *testing.T) {
	var scan FoodScan
	if err := json.Unmarshal(fixtureFor(t, "scanFoodPhoto").Response.Body, &scan); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(scan.Correction())
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Analysis json.RawMessage `json:"analysis"`
	}
	if err := json.Unmarshal(fixtureFor(t, "correctPhotoScan").Request.Body, &request); err != nil {
		t.Fatal(err)
	}
	equalJSON(t, got, request.Analysis)

	unit := "cup"
	noName := FoodScan{Detections: []FoodDetection{{Food: DetectedFood{ID: "1", Quantity: 2, Serving: ServingSummary{ID: "2", Quantity: 1, Unit: &unit}}}}}
	encoded, _ := json.Marshal(noName.Correction())
	for _, want := range []string{`"meal_name":null`, `"weight_grams":null`, `"confidence":null`, `"name":null`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("null field lost: %s in %s", want, encoded)
		}
	}
}

func TestCorrectScanSendsTheScanBack(t *testing.T) {
	fixture := fixtureFor(t, "correctPhotoScan")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		equalJSON(t, body, fixture.Request.Body)
		_, _ = w.Write(fixture.Response.Body)
	}))
	defer server.Close()
	var scan FoodScan
	_ = json.Unmarshal(fixtureFor(t, "scanFoodPhoto").Response.Body, &scan)
	c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL})
	result, _, err := c.FoodAnalysis.CorrectScan(context.Background(), scan, "change oatmeal to steel-cut oats")
	if err != nil || result == nil || len(result.Detections) != 1 {
		t.Fatal(err)
	}
}

func TestEmptyFoodLogUpdateIsRejectedLocally(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL})
	_, _, err := c.FoodLogs.Update(context.Background(), UpdateFoodLogRequest{LogID: "78129823-8ba2-4183-b13b-71f0e963c606"})
	if !errors.Is(err, ErrInvalidInput) || calls.Load() != 0 {
		t.Fatalf("empty patch reached the server: %v", err)
	}
	// A partial update sends only what was set: no null placeholders for the rest.
	var captured []byte
	patch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		_, _ = w.Write(fixtureFor(t, "updateFoodLog").Response.Body)
	}))
	defer patch.Close()
	c, _ = NewClient(Config{SecretKey: "sk-test", BaseURL: patch.URL})
	if _, _, err := c.FoodLogs.Update(context.Background(), UpdateFoodLogRequest{LogID: "78129823-8ba2-4183-b13b-71f0e963c606", Name: Value("Lunch")}); err != nil {
		t.Fatal(err)
	}
	equalJSON(t, captured, []byte(`{"name":"Lunch"}`))
}

func TestWaterAndWeightWritesAreNotReplayed(t *testing.T) {
	for _, op := range []operation{opCreateWaterLog, opCreateWeightLog} {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"code":"service_unavailable","message":"later"}`)
		}))
		c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL})
		var err error
		if op.ID == "createWaterLog" {
			_, _, err = c.WaterLogs.Create(context.Background(), CreateWaterLogRequest{Amount: WaterAmount{Value: 8, Unit: VolumeUnitFlOz}})
		} else {
			_, _, err = c.WeightLogs.Create(context.Background(), CreateWeightLogRequest{Weight: Weight{Value: 150, Unit: WeightUnitLb}})
		}
		server.Close()
		if !errors.Is(err, ErrInternalServer) || calls.Load() != 1 {
			t.Fatalf("%s: %d requests, %v", op.ID, calls.Load(), err)
		}
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL})
	if _, err := c.WaterLogs.Delete(context.Background(), DeleteWaterLogRequest{LogID: "78129823-8ba2-4183-b13b-71f0e963c606"}); err != nil || calls.Load() != 2 {
		t.Fatalf("idempotent delete not retried: %d, %v", calls.Load(), err)
	}
}

func TestWaterLimitAndDateRangeErrorsAreNotRetried(t *testing.T) {
	for _, code := range []string{"daily_water_limit_exceeded", "date_range_too_large"} {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"code":"`+code+`","message":"no"}`)
		}))
		c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL})
		_, _, err := c.WaterLogs.List(context.Background(), ListWaterLogsRequest{StartDate: "2026-09-01", EndDate: "2026-09-10", Timezone: "UTC", Unit: VolumeUnitMl})
		server.Close()
		var api *APIError
		if !errors.As(err, &api) || !errors.Is(err, ErrBadRequest) || api.Code != code || calls.Load() != 1 {
			t.Fatalf("%s: %d requests, %v", code, calls.Load(), err)
		}
	}
}

func TestWaterLogsAcceptCups(t *testing.T) {
	var bodies []string
	var units []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(body))
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"id":"78129823-8ba2-4183-b13b-71f0e963c606","amount":{"value":0.1,"unit":"cup"},"created_at":"2026-09-10T14:30:00.000Z"}`)
			return
		}
		units = append(units, r.URL.Query().Get("unit"))
		_, _ = io.WriteString(w, `{"items":[{"date":"2026-09-10","total":{"value":8.5,"unit":"cup"}}]}`)
	}))
	defer server.Close()
	c, _ := NewClient(Config{SecretKey: "sk-test", BaseURL: server.URL})
	user, err := c.ForUser("user-1")
	if err != nil {
		t.Fatal(err)
	}
	log, _, err := user.WaterLogs.Create(context.Background(), CreateWaterLogRequest{Amount: WaterAmount{Value: 0.1, Unit: VolumeUnitCup}})
	if err != nil || log.Amount.Unit != VolumeUnitCup || log.Amount.Value != 0.1 || len(bodies) != 1 || !strings.Contains(bodies[0], `"unit":"cup"`) {
		t.Fatalf("cup create: %v %+v %v", err, log, bodies)
	}
	totals, _, err := user.WaterLogs.List(context.Background(), ListWaterLogsRequest{StartDate: "2026-09-10", EndDate: "2026-09-10", Timezone: "UTC", Unit: VolumeUnitCup})
	if err != nil || len(units) != 1 || units[0] != "cup" || totals.Items[0].Total.Unit != VolumeUnitCup {
		t.Fatalf("cup list: %v %v %+v", err, units, totals)
	}
}

func TestNewClientScopesAreAccepted(t *testing.T) {
	scopes := []string{ScopeWaterLogsRead, ScopeWaterLogsWrite, ScopeWeightLogsRead, ScopeWeightLogsWrite}
	if err := validateCreateInput(CreateClientTokenInput{EndUserID: "user", Scopes: scopes}); err != nil {
		t.Fatal(err)
	}
	all := append([]string{ScopeFoodsRead, ScopeFoodAnalysisWrite, ScopeFoodLogsRead, ScopeFoodLogsWrite, ScopeGlucoseRead, ScopeRestaurantsRead}, scopes...)
	if err := validateCreateInput(CreateClientTokenInput{EndUserID: "user", Scopes: all}); err != nil {
		t.Fatal(err)
	}
}
