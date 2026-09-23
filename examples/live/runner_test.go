package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/January-ai/january-server-sdk-go/january"
)

func lookup(values map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := values[k]; return v, ok }
}
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestEnvLoader(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "never-created")
	writeFile(t, filepath.Join(root, ".env"), []byte("JANUARY_API_KEY='file-key'\nJANUARY_E2E_QUERY=\"banana # literal\" # tail\nJANUARY_E2E_RESTAURANT_QUERY='$(touch "+marker+")'\nexport JANUARY_E2E_UPC=\"049000006346\"\n"))
	c, err := loadConfig(root, lookup(map[string]string{"JANUARY_API_KEY": "shell-key"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.key != "shell-key" || c.query != "banana # literal" || c.restaurantQuery != "$(touch "+marker+")" || c.upc != "049000006346" || c.timeout != 120*time.Second {
		t.Fatal("precedence/quotes/defaults failed")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("env executed")
	}
	_, err = loadConfig(root, lookup(map[string]string{"JANUARY_API_KEY": ""}))
	if err != safeError("missing_api_key") {
		t.Fatal("empty shell override failed")
	}
	alternate := filepath.Join(root, "alternate.env")
	writeFile(t, alternate, []byte("JANUARY_API_KEY=alternate\nJANUARY_E2E_QUERY=pear # comment\n"))
	c, err = loadConfig(root, lookup(map[string]string{"JANUARY_ENV_FILE": alternate}))
	if err != nil || c.key != "alternate" || c.query != "pear" {
		t.Fatal("alternate env failed")
	}
	for _, input := range []string{"not an assignment", "KEY='unclosed", "KEY=\"ok\" trailing-command"} {
		if _, err := parseEnv(input); err == nil {
			t.Fatal("invalid env accepted")
		}
	}
	literal := "$HOME " + string(rune(96)) + "whoami" + string(rune(96))
	values, err := parseEnv("LITERAL=\"" + literal + "\"\nQUOTE=\"a\\\"b\"\n")
	if err != nil || values["LITERAL"] != literal || values["QUOTE"] != "a\"b" {
		t.Fatal("expansion or quote handling failed")
	}
}
func TestMissingKeyNoNetwork(t *testing.T) {
	var calls int
	newClient := func(january.Config) (*january.Client, error) {
		calls++
		return nil, safeError("unexpected_client_creation")
	}
	root := t.TempDir()
	var out bytes.Buffer
	exit := runCommand(context.Background(), root, lookup(nil), &out, newClient)
	if exit == 0 || calls != 0 || !strings.Contains(out.String(), "missing_api_key") {
		t.Fatal("missing key must fail before HTTP")
	}
	r := readReport(t, root)
	if r.Status != "NOT_RUN" || r.Counts.Passed != 0 || r.Counts.Blocked != 26 {
		t.Fatal("wrong not-run counts")
	}
}

type fixture struct {
	OperationID, Method, Path string
	Response                  struct {
		Status int
		Body   json.RawMessage
	}
}
type fakeService struct {
	t        *testing.T
	server   *httptest.Server
	fixtures []fixture
	calls    map[string]int
	modes    map[string]string
	userID   string
	log      map[string]any
	water    map[string]any
	minted   bool
	mu       sync.Mutex
}

const mockKey = "sk-OFFLINE-secret"
const mockToken = "ct-OFFLINE-token"
const foodID = "909001"
const servingID = "707001"
const logID = "52fdd931-5acd-432a-a5fe-5a072d848b34"
const waterLogID = "9c1f2a3b-4d5e-4f60-8a71-b2c3d4e5f607"

func newFake(t *testing.T, modes map[string]string) *fakeService {
	t.Helper()
	data, err := os.ReadFile("../../january/testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct{ Operations []fixture }
	if err = json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	s := &fakeService{t: t, fixtures: bundle.Operations, calls: map[string]int{}, modes: modes}
	s.server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.server.Close)
	return s
}
func (s *fakeService) config(root string) config {
	image, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jvZkAAAAASUVORK5CYII=")
	path := filepath.Join(root, "food.png")
	writeFile(s.t, path, image)
	return config{key: mockKey, query: "banana", upc: "049000006346", restaurantQuery: "chicken", latitude: 37.7749, longitude: -122.4194, timeout: time.Second, imagePath: path}
}

func (s *fakeService) newClient(c january.Config) (*january.Client, error) {
	if c.BaseURL != "" || c.SecretKey != mockKey {
		return nil, safeError("unexpected_client_configuration")
	}
	c.BaseURL = s.server.URL
	c.HTTPClient = s.server.Client()
	return january.NewClient(c)
}

func (s *fakeService) count(id string) int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls[id] }
func (s *fakeService) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var f *fixture
	for i := range s.fixtures {
		c := &s.fixtures[i]
		segments := strings.Split(c.Path, "/")
		for j, p := range segments {
			if strings.HasPrefix(p, "{") {
				segments[j] = "[^/]+"
			} else {
				segments[j] = regexp.QuoteMeta(p)
			}
		}
		if c.Method == r.Method && regexp.MustCompile("^"+strings.Join(segments, "/")+"$").MatchString(r.URL.Path) {
			f = c
			break
		}
	}
	if f == nil {
		s.t.Error("unexpected route")
		http.Error(w, "unknown", 404)
		return
	}
	id := f.OperationID
	s.calls[id]++
	if r.Header.Get("Authorization") != "Bearer "+mockKey {
		s.t.Error("wrong auth")
	}
	w.Header().Set("X-Request-ID", "offline-request-"+id)
	var request map[string]any
	data, _ := io.ReadAll(r.Body)
	if len(data) > 0 {
		if err := json.Unmarshal(data, &request); err != nil {
			s.t.Error("invalid body")
		}
	}
	user := r.Header.Get("January-End-User-ID")
	if id == "createClientToken" {
		user, _ = request["end_user_id"].(string)
	}
	if id == "revokeClientTokens" {
		user, _ = request["end_user_id"].(string)
	}
	if strings.Contains(id, "FoodLog") || strings.Contains(id, "WaterLog") || strings.Contains(id, "WeightLog") || id == "createClientToken" || id == "revokeClientTokens" {
		if !regexp.MustCompile("^sdk-e2e-go-[a-f0-9-]{36}$").MatchString(user) || len(user) > 64 {
			s.t.Error("invalid isolated user")
		}
		if s.userID == "" {
			s.userID = user
		}
		if s.userID != user {
			s.t.Error("cross-user request")
		}
	}
	if (id == "listFoodLogs" || id == "listWaterLogs" || id == "listWeightLogs") && r.URL.Query().Get("timezone") != "UTC" {
		s.t.Error("missing UTC")
	}
	if id == "listWaterLogs" && r.URL.Query().Get("unit") != "fl_oz" {
		s.t.Error("missing water unit")
	}
	if id == "predictGlucose" {
		if request["timezone"] != "UTC" {
			s.t.Error("missing UTC")
		}
	}
	if id == "createFoodLog" || id == "predictGlucose" {
		foods := request["foods"].([]any)
		food := foods[0].(map[string]any)
		if food["food_id"] != foodID || food["serving_id"] != servingID {
			s.t.Error("stale selection IDs")
		}
	}
	if id == "scanFoodPhoto" {
		image, _ := request["image"].(string)
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(image, "data:image/png;base64,"))
		if !strings.HasPrefix(image, "data:image/png;base64,") || err != nil || http.DetectContentType(decoded) != "image/png" {
			s.t.Error("not an actual image data URI")
		}
	}
	if id == "searchFoodsByNaturalLanguage" && request["text"] != "one banana" {
		s.t.Error("wrong query")
	}
	if id == "correctPhotoScan" {
		analysis, ok := request["analysis"].(map[string]any)
		if !ok || analysis["meal_name"] != "Breakfast Bowl" || request["instruction"] != "The portion is one serving." {
			s.t.Error("correction must reuse returned scan")
		}
	}
	var body map[string]any
	if string(f.Response.Body) != "null" {
		if err := json.Unmarshal(f.Response.Body, &body); err != nil {
			s.t.Error(err)
			return
		}
	}
	switch id {
	case "searchFoods":
		for _, v := range body["items"].([]any) {
			setFood(v.(map[string]any))
		}
	case "lookupFoodByBarcode":
		setFood(body)
	case "getFood":
		if !strings.HasSuffix(r.URL.Path, fmt.Sprint(foodID)) {
			s.t.Error("stale get ID")
		}
		setFood(body)
	case "createFoodLog":
		body["id"] = logID
		body["name"] = request["name"]
		body["eaten_at"] = request["eaten_at"]
		body["foods"].([]any)[0].(map[string]any)["food_id"] = foodID
		s.log = body
	case "listFoodLogs":
		if s.log == nil {
			body = map[string]any{"items": []any{}}
		} else {
			body = map[string]any{"items": []any{s.log}}
		}
	case "getFoodLog":
		if !strings.HasSuffix(r.URL.Path, logID) {
			s.t.Error("read unknown log")
		}
		if s.log != nil {
			body = s.log
		}
	case "updateFoodLog":
		if !strings.HasSuffix(r.URL.Path, logID) {
			s.t.Error("updated unknown log")
		}
		if s.log != nil {
			s.log["name"] = request["name"]
			body = s.log
		}
	case "deleteFoodLog":
		if !strings.HasSuffix(r.URL.Path, logID) {
			s.t.Error("deleted unknown log")
		}
	case "createWaterLog":
		amount, _ := request["amount"].(map[string]any)
		if amount["unit"] != "fl_oz" || request["consumed_at"] == nil {
			s.t.Error("water log body unexpected")
		}
		body["id"] = waterLogID
		body["amount"] = amount
		// A rejected create records nothing; an ambiguous one is recorded, then fails.
		if s.modes[id] != "reject" {
			s.water = body
		}
	case "listWaterLogs":
		if s.water == nil {
			body = map[string]any{"items": []any{}}
		}
	case "deleteWaterLog":
		if !strings.HasSuffix(r.URL.Path, waterLogID) {
			s.t.Error("deleted unknown water log")
		}
	case "createWeightLog":
		weight, _ := request["weight"].(map[string]any)
		if weight["unit"] != "kg" || request["measured_at"] == nil {
			s.t.Error("weight log body unexpected")
		}
		body["weight"] = weight
		// The API returns the stored time in UTC with milliseconds.
		measured, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(request["measured_at"]))
		body["measured_at"] = measured.UTC().Format("2006-01-02T15:04:05.000Z")
		switch s.modes[id] {
		case "malformed":
			// Recorded, but the success reply does not match what was sent.
			body["weight"] = map[string]any{"value": 71, "unit": "kg"}
		case "shifted":
			// Recorded, but the reply names a different measurement time.
			body["measured_at"] = measured.UTC().Add(time.Minute).Format("2006-01-02T15:04:05.000Z")
		}
	case "createClientToken":
		s.minted = true
		body = map[string]any{"token": mockToken, "expires_in": 300, "expires_at": time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339Nano), "end_user_id": user, "scopes": []string{"foods:read"}}
		if request["ttl_seconds"] != float64(300) {
			s.t.Error("wrong TTL")
		}
	case "revokeClientTokens":
		if request["end_user_id"] != s.userID {
			s.t.Error("revoke request body unexpected")
		}
		body = map[string]any{"revoked_count": 500}
	}
	mode := s.modes[id]
	if mode == "disconnect" {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			s.t.Error(err)
			return
		}
		conn.Close()
		return
	}
	if mode == "reject" {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "daily_water_limit_exceeded", "message": "over the daily cap"})
		return
	}
	if mode == "fail" || mode == "ambiguous" || mode == "secret" {
		if mode == "secret" {
			w.Header().Set("X-Request-ID", mockToken)
		}
		w.WriteHeader(503)
		code := "service_unavailable"
		if mode == "secret" {
			code = mockKey
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": "PRIVATE " + mockKey + " " + mockToken, "docs_url": "https://private.example/body"})
		return
	}
	if id == "deleteFoodLog" {
		s.log = nil
	}
	if id == "deleteWaterLog" {
		s.water = nil
	}
	if id == "revokeClientTokens" {
		s.minted = false
	}
	w.WriteHeader(f.Response.Status)
	if f.Response.Status != 204 {
		_ = json.NewEncoder(w).Encode(body)
	}
}
func setFood(f map[string]any) {
	f["id"] = foodID
	for _, v := range f["servings"].([]any) {
		v.(map[string]any)["id"] = servingID
	}
}
func readReport(t *testing.T, root string) runReport {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".e2e-results", "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r runReport
	if err = json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
func status(r runReport, label string) string {
	for _, v := range r.Operations {
		if v.Operation == label {
			return v.Status
		}
	}
	return ""
}
func TestLiveWorkflowAll26Offline(t *testing.T) {
	s := newFake(t, nil)
	root := t.TempDir()
	c := s.config(root)
	var out bytes.Buffer
	env := map[string]string{"JANUARY_API_KEY": c.key, "JANUARY_E2E_IMAGE_PATH": c.imagePath}
	if runCommand(context.Background(), root, lookup(env), &out, s.newClient) != 0 {
		t.Fatal(out.String())
	}
	r := readReport(t, root)
	if r.Status != "PASS" || r.Counts.Passed != 26 || r.Counts.Failed != 0 || r.Counts.Blocked != 0 || r.CleanupFailed != 0 {
		t.Fatalf("wrong counts: %+v", r.Counts)
	}
	for _, f := range s.fixtures {
		if s.count(f.OperationID) != 1 {
			t.Errorf("%s count %d", f.OperationID, s.count(f.OperationID))
		}
	}
	if s.log != nil || s.water != nil || s.minted {
		t.Fatal("leftovers")
	}
	if len(r.Retained) != 1 || r.Retained[0].Operation != "weightLogs.create" || r.Retained[0].Status != "RETAINED" || !strings.Contains(out.String(), "weightLogs.create RETAINED reason=no_delete_endpoint_run_user_only") {
		t.Fatalf("retained weight log not reported: %+v", r.Retained)
	}
	data, _ := json.Marshal(r)
	for _, secret := range []string{mockKey, mockToken, s.userID, "PRIVATE", "Breakfast Bowl", "data:image/png"} {
		if bytes.Contains(data, []byte(secret)) || strings.Contains(out.String(), secret) {
			t.Fatal("sensitive data leaked")
		}
	}
}
func TestFailureCleanupAndBlocked(t *testing.T) {
	s := newFake(t, map[string]string{"searchFoods": "fail", "lookupFoodByBarcode": "fail", "scanFoodPhoto": "fail", "searchFoodsByNaturalLanguage": "fail"})
	r := runWorkflow(context.Background(), s.config(t.TempDir()), nil, s.newClient)
	if r.Status != "FAIL" || r.Counts.Blocked != 8 {
		t.Fatalf("expected eight blocked: %+v", r.Counts)
	}
	for _, label := range []string{"foods.get", "foods.suggestAlternatives", "foodAnalysis.correct", "foodLogs.create", "foodLogs.get", "foodLogs.update", "foodLogs.delete", "glucose.predict"} {
		if status(r, label) != "BLOCKED" {
			t.Error("dependency counted as success")
		}
	}
	for _, label := range []string{"credits", "foods.autocomplete", "restaurants.search", "restaurants.getMenuItems", "restaurants.searchMenuItems", "foodLogs.list", "waterLogs.create", "waterLogs.list", "waterLogs.delete", "weightLogs.create", "weightLogs.list", "createClientToken", "revokeClientTokens"} {
		if status(r, label) != "PASS" {
			t.Errorf("independent operation stopped: %s", label)
		}
	}
	if s.count("revokeClientTokens") != 1 || s.minted {
		t.Fatal("token cleanup missing")
	}
}
func TestAmbiguousMintCleanup(t *testing.T) {
	s := newFake(t, map[string]string{"createClientToken": "disconnect"})
	r := runWorkflow(context.Background(), s.config(t.TempDir()), nil, s.newClient)
	if r.Status != "FAIL" || status(r, "createClientToken") != "FAIL" || status(r, "revokeClientTokens") != "PASS" || s.count("createClientToken") != 1 || s.count("revokeClientTokens") != 1 || s.minted {
		t.Fatal("ambiguous mint cleanup failed")
	}
}
func TestAmbiguousCreateCleanup(t *testing.T) {
	s := newFake(t, map[string]string{"createFoodLog": "ambiguous"})
	r := runWorkflow(context.Background(), s.config(t.TempDir()), nil, s.newClient)
	if status(r, "foodLogs.create") != "FAIL" || status(r, "foodLogs.update") != "BLOCKED" || status(r, "foodLogs.delete") != "BLOCKED" {
		t.Fatal("wrong ambiguous dependency status")
	}
	if s.log != nil || s.count("deleteFoodLog") != 1 || r.CleanupFailed != 0 {
		t.Fatal("own ambiguous log not cleaned")
	}
}
func unconfirmedCleanup(t *testing.T, r runReport, s *fakeService, label, code string) {
	t.Helper()
	found := 0
	for _, v := range r.Cleanup {
		if v.Operation != label {
			continue
		}
		found++
		at, err := time.Parse(time.RFC3339, v.At)
		if v.Status != "FAIL" || v.Code != code || v.EndUserID == "" || v.EndUserID != s.userID || err != nil || time.Since(at) > time.Minute {
			t.Fatalf("unconfirmed write not named: %+v", v)
		}
	}
	if found != 1 || r.Status != "FAIL" || r.CleanupFailed < 1 {
		t.Fatalf("unconfirmed write did not fail the run: %d %s %d", found, r.Status, r.CleanupFailed)
	}
}

// A water create whose reply is an error the server may have committed behind, or
// that never arrives, leaves a log the runner cannot find: the list endpoint only
// returns daily totals. The run fails and names the end user and time instead of
// passing silently.
func TestAmbiguousWaterCreateCleanup(t *testing.T) {
	for _, mode := range []string{"ambiguous", "disconnect"} {
		s := newFake(t, map[string]string{"createWaterLog": mode})
		r := runWorkflow(context.Background(), s.config(t.TempDir()), nil, s.newClient)
		if status(r, "waterLogs.create") != "FAIL" || status(r, "waterLogs.delete") != "BLOCKED" || status(r, "waterLogs.list") != "PASS" || s.count("createWaterLog") != 1 {
			t.Fatalf("%s: wrong ambiguous water dependency status", mode)
		}
		unconfirmedCleanup(t, r, s, "cleanup.waterLogs.unconfirmed", "water_log_cleanup_unconfirmed")
	}
}

func TestRejectedWaterCreateNeedsNoCleanup(t *testing.T) {
	s := newFake(t, map[string]string{"createWaterLog": "reject"})
	r := runWorkflow(context.Background(), s.config(t.TempDir()), nil, s.newClient)
	if status(r, "waterLogs.create") != "FAIL" || r.CleanupFailed != 0 {
		t.Fatalf("a rejected create was reported as unconfirmed: %+v", r.Cleanup)
	}
	if s.water != nil || s.count("deleteWaterLog") != 0 {
		t.Fatal("a rejected create was recorded or deleted")
	}
	for _, v := range r.Cleanup {
		if v.EndUserID != "" {
			t.Fatalf("end user named without an unconfirmed write: %+v", v)
		}
	}
}

// Weight logs cannot be deleted. A create whose outcome is unknown, including a
// success reply that does not match what was sent, is reported with the end user
// and time; only a confirmed one is listed as retained.
func TestAmbiguousWeightCreateIsReported(t *testing.T) {
	for _, mode := range []string{"ambiguous", "disconnect", "malformed", "shifted"} {
		s := newFake(t, map[string]string{"createWeightLog": mode})
		r := runWorkflow(context.Background(), s.config(t.TempDir()), nil, s.newClient)
		if status(r, "weightLogs.create") != "FAIL" || s.count("createWeightLog") != 1 || len(r.Retained) != 0 {
			t.Fatalf("%s: wrong weight status", mode)
		}
		unconfirmedCleanup(t, r, s, "cleanup.weightLogs.unconfirmed", "weight_log_create_unconfirmed")
	}
}
func TestWaterCleanupAfterFailedDelete(t *testing.T) {
	s := newFake(t, map[string]string{"deleteWaterLog": "fail"})
	r := runWorkflow(context.Background(), s.config(t.TempDir()), nil, s.newClient)
	if r.Status != "FAIL" || status(r, "waterLogs.delete") != "FAIL" || s.count("deleteWaterLog") != 2 || r.CleanupFailed != 1 {
		t.Fatalf("water cleanup missing: %+v", r.Counts)
	}
}
func TestCleanupFailureFailsRun(t *testing.T) {
	s := newFake(t, map[string]string{"deleteFoodLog": "fail", "revokeClientTokens": "fail"})
	r := runWorkflow(context.Background(), s.config(t.TempDir()), nil, s.newClient)
	if r.Status != "FAIL" || r.CleanupFailed != 2 || s.count("deleteFoodLog") != 2 || s.count("revokeClientTokens") != 1 {
		t.Fatalf("cleanup failures hidden: %+v", r.Counts)
	}
}
func TestSafeReportAndLogging(t *testing.T) {
	s := newFake(t, map[string]string{"searchFoods": "secret"})
	root := t.TempDir()
	c := s.config(root)
	var out bytes.Buffer
	exit := runCommand(context.Background(), root, lookup(map[string]string{"JANUARY_API_KEY": c.key, "JANUARY_E2E_IMAGE_PATH": c.imagePath}), &out, s.newClient)
	b, err := os.ReadFile(filepath.Join(root, ".e2e-results", "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if exit == 0 {
		t.Fatal("failed operation exit zero")
	}
	for _, secret := range []string{mockKey, mockToken, "PRIVATE", "private.example"} {
		if bytes.Contains(b, []byte(secret)) || strings.Contains(out.String(), secret) {
			t.Fatal("unsafe error output")
		}
	}
}
func TestFreshIDsAndConfigurationValidation(t *testing.T) {
	a, err := freshUserID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := freshUserID()
	if err != nil || a == b || len(a) > 64 || !strings.HasPrefix(a, "sdk-e2e-go-") {
		t.Fatal("invalid identity")
	}
	for key, value := range map[string]string{"JANUARY_E2E_TIMEOUT_SECONDS": "NaN", "JANUARY_E2E_LATITUDE": "91", "JANUARY_E2E_LONGITUDE": "-181", "JANUARY_API_KEY": "ct-wrong", "JANUARY_ENV_FILE": "missing.env"} {
		env := map[string]string{"JANUARY_API_KEY": mockKey}
		env[key] = value
		if _, err := loadConfig(t.TempDir(), lookup(env)); err == nil {
			t.Errorf("invalid %s accepted", key)
		}
	}
}
