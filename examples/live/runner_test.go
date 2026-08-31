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
	if r.Status != "NOT_RUN" || r.Counts.Passed != 0 || r.Counts.Blocked != 18 {
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
	minted   bool
	mu       sync.Mutex
}

const mockKey = "sk-OFFLINE-secret"
const mockToken = "ct-OFFLINE-token"
const foodID = 909001
const servingID = 707001
const logID = "52fdd931-5acd-432a-a5fe-5a072d848b34"

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
	user := r.Header.Get("X-End-User-ID")
	if id == "mintClientToken" {
		user, _ = request["end_user_id"].(string)
	}
	if id == "revokeClientTokens" {
		user = r.URL.Query().Get("end_user_id")
	}
	if id != "credits" {
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
	if strings.Contains(id, "FoodLog") || id == "predictGlucose" {
		if r.Header.Get("X-End-User-Timezone") != "UTC" {
			s.t.Error("missing UTC")
		}
	}
	if id == "createFoodLog" || id == "predictGlucose" {
		foods := request["foods"].([]any)
		food := foods[0].(map[string]any)
		if food["id"] != float64(foodID) || food["serving"].(map[string]any)["id"] != float64(servingID) {
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
	if id == "correctPhotoScan" && (request["meal_name"] != "Breakfast Bowl" || len(request["detections"].([]any)) == 0) {
		s.t.Error("correction must reuse returned scan")
	}
	var body map[string]any
	if string(f.Response.Body) != "null" {
		if err := json.Unmarshal(f.Response.Body, &body); err != nil {
			s.t.Error(err)
			return
		}
	}
	switch id {
	case "searchFoods", "lookupFoodByBarcode":
		for _, v := range body["items"].([]any) {
			setFood(v.(map[string]any))
		}
	case "getFood":
		if !strings.HasSuffix(r.URL.Path, fmt.Sprint(foodID)) {
			s.t.Error("stale get ID")
		}
		setFood(body)
	case "createFoodLog":
		body["id"] = logID
		body["name"] = request["name"]
		body["timestamp_utc"] = request["timestamp_utc"]
		body["foods"].([]any)[0].(map[string]any)["id"] = float64(foodID)
		s.log = body
	case "listFoodLogs":
		if s.log == nil {
			body = map[string]any{"total_count": 0, "items": []any{}}
		} else {
			body = map[string]any{"total_count": 1, "items": []any{s.log}}
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
	case "mintClientToken":
		s.minted = true
		body = map[string]any{"token": mockToken, "expires_in": 300, "expires_at": time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339Nano), "end_user_id": user, "scopes": []string{"foods:read"}}
		if request["ttl_seconds"] != float64(300) {
			s.t.Error("wrong TTL")
		}
	case "revokeClientTokens":
		if len(data) > 0 {
			s.t.Error("revoke request body unexpected")
		}
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
	if id == "revokeClientTokens" {
		s.minted = false
		w.Header().Set("X-Revoked-Count", "500")
	}
	w.WriteHeader(f.Response.Status)
	if f.Response.Status != 204 {
		_ = json.NewEncoder(w).Encode(body)
	}
}
func setFood(f map[string]any) {
	f["id"] = float64(foodID)
	for _, v := range f["servings"].([]any) {
		v.(map[string]any)["id"] = float64(servingID)
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
func TestLiveWorkflowAll18Offline(t *testing.T) {
	s := newFake(t, nil)
	root := t.TempDir()
	c := s.config(root)
	var out bytes.Buffer
	env := map[string]string{"JANUARY_API_KEY": c.key, "JANUARY_E2E_IMAGE_PATH": c.imagePath}
	if runCommand(context.Background(), root, lookup(env), &out, s.newClient) != 0 {
		t.Fatal(out.String())
	}
	r := readReport(t, root)
	if r.Status != "PASS" || r.Counts.Passed != 18 || r.Counts.Failed != 0 || r.Counts.Blocked != 0 || r.CleanupFailed != 0 {
		t.Fatalf("wrong counts: %+v", r.Counts)
	}
	for _, f := range s.fixtures {
		if s.count(f.OperationID) != 1 {
			t.Errorf("%s count %d", f.OperationID, s.count(f.OperationID))
		}
	}
	if s.log != nil || s.minted {
		t.Fatal("leftovers")
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
	if r.Status != "FAIL" || r.Counts.Blocked != 7 {
		t.Fatalf("expected seven blocked: %+v", r.Counts)
	}
	for _, label := range []string{"foods.get", "foods.suggestAlternatives", "foodAnalysis.correct", "foodLogs.create", "foodLogs.update", "foodLogs.delete", "glucose.predict"} {
		if status(r, label) != "BLOCKED" {
			t.Error("dependency counted as success")
		}
	}
	for _, label := range []string{"credits", "foods.autocomplete", "restaurants.search", "restaurants.searchMenuItems", "foodLogs.list", "mintClientToken", "revokeClientTokens"} {
		if status(r, label) != "PASS" {
			t.Errorf("independent operation stopped: %s", label)
		}
	}
	if s.count("revokeClientTokens") != 1 || s.minted {
		t.Fatal("token cleanup missing")
	}
}
func TestAmbiguousMintCleanup(t *testing.T) {
	s := newFake(t, map[string]string{"mintClientToken": "disconnect"})
	r := runWorkflow(context.Background(), s.config(t.TempDir()), nil, s.newClient)
	if r.Status != "FAIL" || status(r, "mintClientToken") != "FAIL" || status(r, "revokeClientTokens") != "PASS" || s.count("mintClientToken") != 1 || s.count("revokeClientTokens") != 1 || s.minted {
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
