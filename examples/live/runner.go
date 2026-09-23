package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/January-ai/january-server-sdk-go/january"
)

var operationLabels = []string{
	"credits", "foods.search", "foods.autocomplete", "foods.get", "foods.lookupBarcode", "foods.suggestAlternatives",
	"restaurants.search", "restaurants.getMenuItems", "restaurants.searchMenuItems", "foodAnalysis.analyzePhoto", "foodAnalysis.analyzeDescription", "foodAnalysis.correct",
	"foodLogs.create", "foodLogs.list", "foodLogs.getSummary", "foodLogs.get", "foodLogs.update", "foodLogs.delete",
	"waterLogs.create", "waterLogs.list", "waterLogs.delete", "weightLogs.create", "weightLogs.list", "glucose.predict", "createClientToken", "revokeClientTokens",
}

type result struct {
	Operation  string `json:"operation"`
	Status     string `json:"status"`
	Code       string `json:"code,omitempty"`
	RequestID  string `json:"requestId,omitempty"`
	Reason     string `json:"reason,omitempty"`
	DurationMS int64  `json:"durationMs"`
	// EndUserID and At name the run's synthetic end user and the logged time,
	// only on a write the runner could not clean up, so it can be found and
	// removed server-side.
	EndUserID string `json:"endUserId,omitempty"`
	At        string `json:"at,omitempty"`
}
type counts struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Blocked int `json:"blocked"`
}
type runReport struct {
	Language      string   `json:"language"`
	Status        string   `json:"status"`
	StartedAt     string   `json:"startedAt"`
	DurationMS    int64    `json:"durationMs"`
	Operations    []result `json:"operations"`
	Cleanup       []result `json:"cleanup"`
	Retained      []result `json:"retained"`
	Checks        []result `json:"checks"`
	Counts        counts   `json:"counts"`
	CleanupFailed int      `json:"cleanupFailed"`
}

func newReport() runReport {
	r := runReport{Language: "go", Status: "FAIL", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Cleanup: []result{}, Retained: []result{}, Checks: []result{}}
	for _, label := range operationLabels {
		r.Operations = append(r.Operations, result{Operation: label, Status: "BLOCKED", Reason: "not_executed"})
	}
	return r
}
func (r *runReport) finish() {
	r.Counts = counts{}
	r.CleanupFailed = 0
	for _, v := range r.Operations {
		switch v.Status {
		case "PASS":
			r.Counts.Passed++
		case "FAIL":
			r.Counts.Failed++
		default:
			r.Counts.Blocked++
		}
	}
	for _, v := range r.Cleanup {
		if v.Status != "PASS" {
			r.CleanupFailed++
		}
	}
	ready := r.Counts.Passed == len(operationLabels) && r.Counts.Failed == 0 && r.Counts.Blocked == 0 && r.CleanupFailed == 0
	for _, v := range r.Checks {
		if v.Status != "PASS" {
			ready = false
		}
	}
	if r.Status != "NOT_RUN" {
		if ready {
			r.Status = "PASS"
		} else {
			r.Status = "FAIL"
		}
	}
	if start, err := time.Parse(time.RFC3339Nano, r.StartedAt); err == nil {
		r.DurationMS = time.Since(start).Milliseconds()
	}
}

// runUserPrefix marks the synthetic end user each run creates for itself. The
// runner never writes logs for any other end user.
const runUserPrefix = "sdk-e2e-go-"

func freshUserID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", safeError("random_id_failed")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf(runUserPrefix+"%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

var safeIdentifier = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func safeField(value string, secrets ...string) string {
	if !safeIdentifier.MatchString(value) {
		return ""
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(value, s) {
			return ""
		}
	}
	if strings.Contains(value, "ct-") || strings.Contains(value, "sk-") {
		return ""
	}
	return value
}
func errorCode(err error) string {
	var safe safeError
	if errors.As(err, &safe) {
		return string(safe)
	}
	var api *january.APIError
	if errors.As(err, &api) {
		if c := safeField(api.Code); c != "" {
			return c
		}
		return "api_error"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, january.ErrInvalidInput) {
		return "invalid_input"
	}
	if errors.Is(err, january.ErrNotConfigured) {
		return "invalid_configuration"
	}
	return "request_failed"
}

type runner struct {
	ctx                           context.Context
	cfg                           config
	client                        *january.Client
	user                          *january.UserClient
	userID, marker, day, token    string
	started                       time.Time
	report                        runReport
	emit                          func(result)
	owned                         map[string]bool
	ownedWater                    map[string]bool
	unconfirmed                   []result
	createUnresolved, mintAttempt bool
}

func (r *runner) step(label, blocked string, fn func(context.Context) (*january.Response, error)) bool {
	v := result{Operation: label, Status: "BLOCKED", Reason: blocked}
	if blocked == "" {
		start := time.Now()
		meta, err := fn(r.ctx)
		v.DurationMS = time.Since(start).Milliseconds()
		if meta != nil {
			v.RequestID = safeField(meta.RequestID, r.cfg.key, r.token, r.userID)
		}
		if err != nil {
			v.Status = "FAIL"
			v.Code = safeField(errorCode(err), r.cfg.key, r.token, r.userID)
			if v.Code == "" {
				v.Code = "request_failed"
			}
			var api *january.APIError
			if v.RequestID == "" && errors.As(err, &api) {
				v.RequestID = safeField(api.RequestID, r.cfg.key, r.token, r.userID)
			}
		} else {
			v.Status = "PASS"
		}
	}
	for i := range r.report.Operations {
		if r.report.Operations[i].Operation == label {
			r.report.Operations[i] = v
		}
	}
	if r.emit != nil {
		r.emit(v)
	}
	return v.Status == "PASS"
}
func (r *runner) cleanupStep(label string, fn func(context.Context) (*january.Response, error)) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.timeout)
	defer cancel()
	meta, err := fn(ctx)
	v := result{Operation: label, Status: "PASS", DurationMS: time.Since(start).Milliseconds()}
	if meta != nil {
		v.RequestID = safeField(meta.RequestID, r.cfg.key, r.token, r.userID)
	}
	if err != nil {
		v.Status = "FAIL"
		v.Code = safeField(errorCode(err), r.cfg.key, r.token, r.userID)
		if v.Code == "" {
			v.Code = "cleanup_failed"
		}
	}
	r.report.Cleanup = append(r.report.Cleanup, v)
	if r.emit != nil {
		r.emit(v)
	}
}
func assert(ok bool) error {
	if !ok {
		return safeError("response_assertion_failed")
	}
	return nil
}
func dependency(ok bool, reason string) string {
	if ok {
		return ""
	}
	return reason
}

// createOutcomeUnknown reports whether a failed create may still have been
// recorded. Local validation errors and 4xx replies are definitive rejections;
// transport errors, timeouts, and 5xx replies leave the outcome unknown.
func createOutcomeUnknown(err error) bool {
	if err == nil || errors.Is(err, january.ErrInvalidInput) {
		return false
	}
	var api *january.APIError
	if errors.As(err, &api) {
		return api.StatusCode >= 500
	}
	return true
}

// recordUnconfirmed keeps a write the runner cannot clean up. A water log can
// only be deleted by the ID its create returns (the list endpoint returns daily
// totals), and a weight log cannot be deleted at all, so the report names the
// end user and time for server-side cleanup and the run fails.
func (r *runner) recordUnconfirmed(label, code, at string) {
	r.unconfirmed = append(r.unconfirmed, result{Operation: label, Status: "FAIL", Code: code, EndUserID: r.userID, At: at})
}

func (r *runner) rememberLogs(logs []january.FoodLog) {
	if !r.createUnresolved {
		return
	}
	for _, log := range logs {
		if log.Name != nil && *log.Name == r.marker && log.ID != nil && *log.ID != "" {
			r.owned[*log.ID] = true
			r.createUnresolved = false
		}
	}
}
func (r *runner) cleanup() {
	if r.createUnresolved {
		r.cleanupStep("cleanup.findOwnLog", func(ctx context.Context) (*january.Response, error) {
			logs, meta, err := r.user.FoodLogs.List(ctx, january.ListFoodLogsRequest{StartDate: r.day, EndDate: time.Now().UTC().Format("2006-01-02"), Timezone: "UTC"})
			if err != nil {
				return meta, err
			}
			if logs != nil {
				r.rememberLogs(logs.Items)
			}
			if r.createUnresolved {
				return meta, safeError("ambiguous_create_unresolved")
			}
			return meta, nil
		})
	}
	ids := make([]string, 0, len(r.owned))
	for id := range r.owned {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r.cleanupStep("cleanup.foodLogs.delete", func(ctx context.Context) (*january.Response, error) {
			meta, err := r.user.FoodLogs.Delete(ctx, january.DeleteFoodLogRequest{LogID: id})
			if err != nil {
				return meta, err
			}
			if err = assert(meta != nil && meta.StatusCode == http.StatusNoContent); err == nil {
				delete(r.owned, id)
			}
			return meta, err
		})
	}
	if len(ids) == 0 && !r.createUnresolved {
		r.cleanupStep("cleanup.foodLogs", func(context.Context) (*january.Response, error) { return nil, nil })
	}
	waterIDs := make([]string, 0, len(r.ownedWater))
	for id := range r.ownedWater {
		waterIDs = append(waterIDs, id)
	}
	sort.Strings(waterIDs)
	for _, id := range waterIDs {
		r.cleanupStep("cleanup.waterLogs.delete", func(ctx context.Context) (*january.Response, error) {
			meta, err := r.user.WaterLogs.Delete(ctx, january.DeleteWaterLogRequest{LogID: id})
			if err != nil {
				return meta, err
			}
			if err = assert(meta != nil && meta.StatusCode == http.StatusNoContent); err == nil {
				delete(r.ownedWater, id)
			}
			return meta, err
		})
	}
	for _, v := range r.unconfirmed {
		r.report.Cleanup = append(r.report.Cleanup, v)
		if r.emit != nil {
			r.emit(v)
		}
	}
	// The canonical revoke operation is also the final token cleanup: ONE call total, never a loop.
	previous := r.ctx
	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.timeout)
	defer cancel()
	r.ctx = ctx
	r.step("revokeClientTokens", dependency(r.mintAttempt, "mint_not_attempted"), func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.client.RevokeClientTokens(ctx, january.RevokeClientTokensRequest{EndUserID: r.userID})
		if err != nil {
			return meta, err
		}
		return meta, assert(meta != nil && meta.StatusCode == http.StatusOK && value != nil && value.RevokedCount >= 0)
	})
	r.ctx = previous
	if r.mintAttempt {
		for _, v := range r.report.Operations {
			if v.Operation == "revokeClientTokens" {
				v.Operation = "cleanup.clientTokens"
				r.report.Cleanup = append(r.report.Cleanup, v)
				if r.emit != nil {
					r.emit(v)
				}
			}
		}
	}
}

func runWorkflow(ctx context.Context, c config, emit func(result), newClient func(january.Config) (*january.Client, error)) (report runReport) {
	r := runner{ctx: ctx, cfg: c, report: newReport(), emit: emit, owned: map[string]bool{}, ownedWater: map[string]bool{}, started: time.Now().UTC()}
	id, err := freshUserID()
	if err == nil {
		r.client, err = newClient(january.Config{SecretKey: c.key, Timeout: c.timeout, MaxRetries: january.Value(0)})
	}
	if err == nil {
		r.user, err = r.client.ForUser(id, "UTC")
	}
	if err != nil || c.key == "" {
		r.report.Status = "NOT_RUN"
		r.report.Checks = append(r.report.Checks, result{Operation: "configuration", Status: "FAIL", Code: "invalid_configuration"})
		r.report.finish()
		return r.report
	}
	r.userID = id
	r.marker = "January SDK E2E " + id
	r.day = r.started.Format("2006-01-02")
	defer func() {
		if recover() != nil {
			v := result{Operation: "runner", Status: "FAIL", Code: "runner_panicked"}
			r.report.Checks = append(r.report.Checks, v)
			if emit != nil {
				emit(v)
			}
		}
		r.cleanup()
		r.report.finish()
		report = r.report
	}()
	r.step("credits", "", func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.client.GetCredits(ctx)
		if err != nil {
			return meta, err
		}
		return meta, assert(value != nil && value.Plan != "" && value.ResetsAt != "" && value.UsedCredits >= 0)
	})
	var candidates []january.FoodSearchItem
	r.step("foods.search", "", func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.Foods.Search(ctx, january.SearchFoodsRequest{Query: c.query})
		if err != nil {
			return meta, err
		}
		if value != nil {
			candidates = append(candidates, value.Items...)
		}
		return meta, assert(value != nil && value.Items != nil)
	})
	r.step("foods.autocomplete", "", func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.Foods.Autocomplete(ctx, january.AutocompleteFoodsRequest{Query: c.query})
		if err != nil {
			return meta, err
		}
		return meta, assert(value != nil && value.Items != nil)
	})
	r.step("foods.lookupBarcode", "", func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.Foods.LookupBarcode(ctx, january.LookupFoodByBarcodeRequest{Barcode: c.upc})
		if err != nil {
			return meta, err
		}
		if value != nil {
			candidates = append(candidates, *value)
		}
		return meta, assert(value != nil && value.ID != "")
	})
	var foodID string
	for _, food := range candidates {
		if food.ID != "" {
			foodID = food.ID
			break
		}
	}
	r.step("foods.get", dependency(foodID != "", "no_live_food_id"), func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.Foods.Get(ctx, january.GetFoodRequest{FoodID: foodID})
		if err != nil {
			return meta, err
		}
		if value != nil {
			candidates = append([]january.FoodSearchItem{*value}, candidates...)
		}
		return meta, assert(value != nil && value.ID == foodID && value.Name != nil && value.Servings != nil)
	})
	r.step("foods.suggestAlternatives", dependency(foodID != "", "no_live_food_id"), func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.Foods.SuggestAlternatives(ctx, january.SuggestFoodAlternativesRequest{FoodID: foodID})
		if err != nil {
			return meta, err
		}
		return meta, assert(value != nil && value.Alternatives != nil)
	})
	var restaurantID string
	r.step("restaurants.search", "", func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.Restaurants.Search(ctx, january.SearchRestaurantsRequest{Query: c.restaurantQuery, Latitude: c.latitude, Longitude: c.longitude})
		if err != nil {
			return meta, err
		}
		if value != nil && len(value.Items) > 0 {
			restaurantID = value.Items[0].ID
		}
		return meta, assert(value != nil && value.Items != nil)
	})
	r.step("restaurants.getMenuItems", dependency(restaurantID != "", "no_live_restaurant_id"), func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.Restaurants.GetMenuItems(ctx, january.GetRestaurantMenuItemsRequest{RestaurantID: restaurantID})
		if err != nil {
			return meta, err
		}
		return meta, assert(value != nil && value.Items != nil)
	})
	r.step("restaurants.searchMenuItems", "", func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.Restaurants.SearchMenuItems(ctx, january.SearchRestaurantMenuItemsRequest{Query: c.restaurantQuery, Latitude: c.latitude, Longitude: c.longitude})
		if err != nil {
			return meta, err
		}
		return meta, assert(value != nil && value.Items != nil)
	})
	var scan *january.FoodScan
	r.step("foodAnalysis.analyzePhoto", "", func(ctx context.Context) (*january.Response, error) {
		f, err := os.Open(c.imagePath)
		if err != nil {
			return nil, safeError("image_unreadable")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 10<<20+1))
		if err != nil || len(data) == 0 || len(data) > 10<<20 {
			return nil, safeError("invalid_image")
		}
		mime := http.DetectContentType(data)
		if mime != "image/png" && mime != "image/jpeg" {
			return nil, safeError("invalid_image")
		}
		value, meta, err := r.user.FoodAnalysis.AnalyzePhoto(ctx, january.ScanFoodPhotoRequest{Image: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)})
		if err != nil {
			return meta, err
		}
		if value != nil && len(value.Detections) > 0 {
			scan = value
		}
		return meta, assert(value != nil && len(value.Detections) > 0)
	})
	r.step("foodAnalysis.analyzeDescription", "", func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.FoodAnalysis.AnalyzeDescription(ctx, january.SearchFoodsByNaturalLanguageRequest{Query: "one banana"})
		if err != nil {
			return meta, err
		}
		if scan == nil && value != nil && len(value.Detections) > 0 {
			scan = value
		}
		return meta, assert(value != nil && len(value.Detections) > 0)
	})
	r.step("foodAnalysis.correct", dependency(scan != nil, "no_returned_detections"), func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.FoodAnalysis.Correct(ctx, january.CorrectPhotoScanRequest{Analysis: scan.Correction(), Instruction: "The portion is one serving."})
		if err != nil {
			return meta, err
		}
		return meta, assert(value != nil && len(value.Detections) > 0)
	})
	var selection []january.FoodLogInputFood
outer:
	for _, food := range candidates {
		if food.ID != "" {
			for _, serving := range food.Servings {
				if serving.ID != nil && *serving.ID != "" {
					selection = []january.FoodLogInputFood{{FoodID: food.ID, ServingID: *serving.ID, Quantity: 1}}
					break outer
				}
			}
		}
	}
	var logID string
	r.step("foodLogs.create", dependency(len(selection) > 0, "no_live_food_and_serving"), func(ctx context.Context) (*january.Response, error) {
		r.createUnresolved = true
		value, meta, err := r.user.FoodLogs.Create(ctx, january.CreateFoodLogRequest{Foods: selection, EatenAt: january.Value(r.started.Format(time.RFC3339)), Name: january.Value(r.marker)})
		if value != nil && value.ID != nil && *value.ID != "" {
			logID = *value.ID
			r.owned[logID] = true
			r.createUnresolved = false
		}
		if err != nil {
			return meta, err
		}
		return meta, assert(value != nil && value.ID != nil && *value.ID != "" && len(value.Foods) > 0 && value.Foods[0].FoodID != nil && *value.Foods[0].FoodID == selection[0].FoodID)
	})
	r.step("foodLogs.list", "", func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.FoodLogs.List(ctx, january.ListFoodLogsRequest{StartDate: r.day, EndDate: time.Now().UTC().Format("2006-01-02"), Timezone: "UTC"})
		if err != nil {
			return meta, err
		}
		if value == nil {
			return meta, safeError("response_assertion_failed")
		}
		r.rememberLogs(value.Items)
		if logID != "" {
			found := false
			for _, v := range value.Items {
				if v.ID != nil && *v.ID == logID {
					found = true
				}
			}
			if !found {
				return meta, safeError("created_log_not_listed")
			}
		}
		return meta, assert(value.Items != nil)
	})
	r.step("foodLogs.getSummary", "", func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.FoodLogs.GetSummary(ctx, january.GetFoodLogSummaryRequest{StartDate: r.day, EndDate: time.Now().UTC().Format("2006-01-02"), Timezone: "UTC"})
		if err != nil {
			return meta, err
		}
		if value == nil || len(value.Buckets) == 0 {
			return meta, safeError("response_assertion_failed")
		}
		return meta, nil
	})
	r.step("foodLogs.get", dependency(logID != "", "no_created_log_id"), func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.FoodLogs.Get(ctx, january.GetFoodLogRequest{LogID: logID})
		if err != nil {
			return meta, err
		}
		return meta, assert(value != nil && value.ID != nil && *value.ID == logID)
	})
	r.step("foodLogs.update", dependency(logID != "", "no_created_log_id"), func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.FoodLogs.Update(ctx, january.UpdateFoodLogRequest{LogID: logID, Name: january.Value(r.marker + " updated")})
		if err != nil {
			return meta, err
		}
		if value == nil {
			return meta, safeError("response_assertion_failed")
		}
		return meta, assert(value.ID != nil && *value.ID == logID && value.Name != nil && *value.Name == r.marker+" updated")
	})
	r.step("foodLogs.delete", dependency(logID != "", "no_created_log_id"), func(ctx context.Context) (*january.Response, error) {
		meta, err := r.user.FoodLogs.Delete(ctx, january.DeleteFoodLogRequest{LogID: logID})
		if err != nil {
			return meta, err
		}
		if err = assert(meta != nil && meta.StatusCode == http.StatusNoContent); err == nil {
			delete(r.owned, logID)
		}
		return meta, err
	})
	var waterLogID string
	loggedAt := r.started.Format(time.RFC3339)
	r.step("waterLogs.create", "", func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.WaterLogs.Create(ctx, january.CreateWaterLogRequest{Amount: january.WaterAmount{Value: 8, Unit: january.VolumeUnitFlOz}, ConsumedAt: january.Value(loggedAt)})
		if value != nil && value.ID != "" {
			waterLogID = value.ID
			r.ownedWater[waterLogID] = true
		} else if err == nil || createOutcomeUnknown(err) {
			r.recordUnconfirmed("cleanup.waterLogs.unconfirmed", "water_log_cleanup_unconfirmed", loggedAt)
		}
		if err != nil {
			return meta, err
		}
		return meta, assert(value != nil && value.ID != "" && value.Amount.Value == 8 && value.Amount.Unit == january.VolumeUnitFlOz)
	})
	r.step("waterLogs.list", "", func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.WaterLogs.List(ctx, january.ListWaterLogsRequest{StartDate: r.day, EndDate: time.Now().UTC().Format("2006-01-02"), Timezone: "UTC", Unit: january.VolumeUnitFlOz})
		if err != nil {
			return meta, err
		}
		if value == nil || value.Items == nil {
			return meta, safeError("response_assertion_failed")
		}
		if waterLogID != "" && len(value.Items) == 0 {
			return meta, safeError("created_log_not_listed")
		}
		return meta, nil
	})
	r.step("waterLogs.delete", dependency(waterLogID != "", "no_created_log_id"), func(ctx context.Context) (*january.Response, error) {
		meta, err := r.user.WaterLogs.Delete(ctx, january.DeleteWaterLogRequest{LogID: waterLogID})
		if err != nil {
			return meta, err
		}
		if err = assert(meta != nil && meta.StatusCode == http.StatusNoContent); err == nil {
			delete(r.ownedWater, waterLogID)
		}
		return meta, err
	})
	// Weight logs have no delete endpoint. The runner creates one only for its own
	// synthetic end user, where it stays; the report lists it under retained.
	r.step("weightLogs.create", dependency(strings.HasPrefix(r.userID, runUserPrefix), "not_a_run_owned_user"), func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.WeightLogs.Create(ctx, january.CreateWeightLogRequest{Weight: january.Weight{Value: 70, Unit: january.WeightUnitKg}, MeasuredAt: january.Value(loggedAt)})
		if err == nil {
			retained := result{Operation: "weightLogs.create", Status: "RETAINED", Reason: "no_delete_endpoint_run_user_only"}
			r.report.Retained = append(r.report.Retained, retained)
			if r.emit != nil {
				r.emit(retained)
			}
		} else if createOutcomeUnknown(err) {
			r.recordUnconfirmed("cleanup.weightLogs.unconfirmed", "weight_log_create_unconfirmed", loggedAt)
		}
		if err != nil {
			return meta, err
		}
		return meta, assert(value != nil && value.Weight.Value == 70 && value.Weight.Unit == january.WeightUnitKg && value.MeasuredAt != "")
	})
	r.step("weightLogs.list", "", func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.WeightLogs.List(ctx, january.ListWeightLogsRequest{StartDate: r.day, EndDate: time.Now().UTC().Format("2006-01-02"), Timezone: "UTC"})
		if err != nil {
			return meta, err
		}
		return meta, assert(value != nil && value.Items != nil && len(value.Items) > 0)
	})
	r.step("glucose.predict", dependency(len(selection) > 0, "no_live_food_and_serving"), func(ctx context.Context) (*january.Response, error) {
		value, meta, err := r.user.Glucose.Predict(ctx, january.PredictGlucoseRequest{
			UserProfile: january.GlucosePredictionProfile{Age: 30, Sex: january.SexMale, Height: january.Height{Value: 175, Unit: january.HeightUnitCm}, Weight: january.Weight{Value: 70, Unit: january.WeightUnitKg}},
			Timezone:    "UTC", Foods: selection, StartTime: r.started.Format(time.RFC3339),
		})
		if err != nil {
			return meta, err
		}
		return meta, assert(value != nil && len(value.Points) > 0 && value.ImpactScore != nil)
	})
	r.step("createClientToken", "", func(ctx context.Context) (*january.Response, error) {
		r.mintAttempt = true
		value, meta, err := r.client.CreateClientToken(ctx, january.CreateClientTokenRequest{EndUserID: id, Scopes: []string{"foods:read"}, TTLSeconds: january.Value(int64(300))})
		if value != nil {
			r.token = value.Token
		}
		if err != nil {
			return meta, err
		}
		if value == nil {
			return meta, safeError("response_assertion_failed")
		}
		expires, parseErr := time.Parse(time.RFC3339Nano, value.ExpiresAt)
		return meta, assert(strings.HasPrefix(value.Token, "ct-") && value.EndUserID == id && len(value.Scopes) == 1 && value.Scopes[0] == "foods:read" && value.ExpiresIn > 0 && value.ExpiresIn <= 300 && parseErr == nil && expires.After(time.Now().UTC()))
	})
	return r.report
}
