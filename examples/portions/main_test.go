package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func runOfflineExample(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	env := []string{"GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local"}
	for _, name := range []string{"PATH", "HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "SystemRoot", "TEMP", "TMP", "TMPDIR", "GOCACHE", "GOPATH", "GOROOT", "GOMODCACHE"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	binary := filepath.Join(dir, "portion-example")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, path)
	build.Env = env
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build offline portion example: %v\n%s", err, output)
	}
	run := exec.CommandContext(ctx, binary)
	run.Dir = dir
	run.Env = []string{}
	output, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("offline example failed: %v\n%s", err, output)
	}
	return string(output)
}

func TestREADMEFoodPortionPrediction(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(readme), "### FoodPortion: local serving calculations\n")
	if !ok {
		t.Fatal("README FoodPortion section missing")
	}
	_, block, ok := strings.Cut(section, "```go\n")
	if !ok {
		t.Fatal("README FoodPortion code missing")
	}
	code, _, ok := strings.Cut(block, "\n```")
	if !ok {
		t.Fatal("README FoodPortion code fence missing")
	}
	// Compile the exact documented fragment, not a manually duplicated input.
	// The transport has no network implementation at all; reaching it proves
	// the real SDK request validator accepted the complete documented request.
	source := `package main
import (
    "context"
    "errors"
    "fmt"
    "net/http"
    "os"
    "github.com/January-ai/january-server-sdk-go/january"
)
var intercepted = errors.New("offline request intercepted")
type offlineTransport struct { calls int }
func (t *offlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
    t.calls++
	if r.Method != "POST" || r.URL.String() != "https://partners.january.ai/v1.2/glucose/predictions" ||
		r.Header.Get("Authorization") != "Bearer sk-readme-offline-fixture" {
        return nil, errors.New("unexpected prediction request")
    }
    return nil, intercepted
}
func verify(food january.FoodSearchItem) error {
` + code + `
    if logInput.Foods[0] != glucoseInput.Foods[0] {
        return errors.New("selection mismatch")
    }
    transport := &offlineTransport{}
    client, err := january.NewClient(january.Config{
        SecretKey: "sk-readme-offline-fixture", HTTPClient: &http.Client{Transport: transport},
    })
    if err != nil { return err }
    user, err := client.ForUser("january-quickstart", "UTC")
    if err != nil { return err }
    _, _, err = user.Glucose.Predict(context.Background(), glucoseInput)
    if !errors.Is(err, intercepted) || transport.calls != 1 {
        return errors.New("exact README prediction input did not pass SDK validation")
    }
    missingTime := glucoseInput
    missingTime.StartTime = ""
    missingProfile := glucoseInput
    missingProfile.UserProfile = january.GlucosePredictionProfile{}
    for _, invalid := range []january.PredictGlucoseRequest{
        {Foods: glucoseInput.Foods}, missingTime, missingProfile,
    } {
        _, _, err = user.Glucose.Predict(context.Background(), invalid)
        if !errors.Is(err, january.ErrInvalidInput) || transport.calls != 1 {
            return errors.New("incomplete prediction must fail before transport")
        }
    }
    return nil
}
func main() {
    name, id, unit := "Synthetic food", "2", "piece"
    quantity, scaling, primary := 1.0, 1.0, true
    food := january.FoodSearchItem{ID: "42", Name: &name, Servings: []january.ServingOption{
        {ID: &id, Quantity: &quantity, Unit: &unit, ScalingFactor: &scaling, IsPrimary: &primary},
    }}
    if err := verify(food); err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
    fmt.Println("Exact README FoodPortion prediction passed SDK validation; incomplete requests rejected; no network.")
}
`
	output := runOfflineExample(t, source)
	const want = "Exact README FoodPortion prediction passed SDK validation; incomplete requests rejected; no network.\n"
	if output != want {
		t.Fatal("unexpected README validation output")
	}
	t.Log(strings.TrimSpace(output))
}

func TestPortionExecutableOffline(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	output := runOfflineExample(t, string(source))
	const want = "Offline portion: 600 kcal, protein 0 g, weight 240 g; missing fiber preserved.\n" +
		"Log/glucose selection: {\"food_id\":\"42\",\"quantity\":4,\"serving_id\":\"2\"}\n"
	if output != want {
		t.Fatal("unexpected portion executable output")
	}
	t.Log(strings.TrimSpace(output))
}
