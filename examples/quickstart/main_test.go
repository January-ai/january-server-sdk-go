package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/January-ai/january-server-sdk-go/january"
)

func offlineGoEnv() []string {
	// Allowlist build infrastructure only: never inherit keys, proxy credentials,
	// JANUARY_* settings, or other arbitrary environment variables.
	env := []string{"GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local"}
	for _, name := range []string{"PATH", "HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "SystemRoot", "TEMP", "TMP", "TMPDIR", "GOCACHE", "GOPATH", "GOROOT", "GOMODCACHE"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func installOfflineTransport(t *testing.T, dir string) {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata", "transport.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "transport.go"), source, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestQuickstartExecutable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), source, 0644); err != nil {
		t.Fatal(err)
	}
	installOfflineTransport(t, dir)
	binary := filepath.Join(dir, "quickstart")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, filepath.Join(dir, "main.go"), filepath.Join(dir, "transport.go"))
	build.Env = offlineGoEnv()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build quickstart: %v\n%s", err, output)
	}

	const fakeKey = "sk-quickstart-offline-fixture"
	tests := []struct {
		name       string
		key        string
		keySet     bool
		envFile    string
		status     int
		body       string
		requestID  string
		wantOutput string
		wantExit   int
		wantCalls  int32
	}{
		{
			name: "success", key: fakeKey, status: 200,
			body:       `{"total_count":1,"items":[{"id":42,"name":"Banana","nutrients":{},"servings":[]}]}`,
			wantOutput: "Found 1 foods.\nFirst food: Banana\n", wantCalls: 1,
		},
		{
			name: "dotenv_success", envFile: "# Local key\nJANUARY_API_KEY=' " + fakeKey + " '\n", status: 200,
			body:       `{"total_count":1,"items":[{"id":42,"name":"Banana","nutrients":{},"servings":[]}]}`,
			wantOutput: "Found 1 foods.\nFirst food: Banana\n", wantCalls: 1,
		},
		{
			name: "environment_takes_precedence", key: fakeKey, envFile: "JANUARY_API_KEY=sk-fake-file-key\n", status: 200,
			body:       `{"total_count":1,"items":[{"id":42,"name":"Banana","nutrients":{},"servings":[]}]}`,
			wantOutput: "Found 1 foods.\nFirst food: Banana\n", wantCalls: 1,
		},
		{
			name: "empty_environment_takes_precedence", keySet: true, envFile: "JANUARY_API_KEY=" + fakeKey + "\n",
			wantOutput: "Set JANUARY_API_KEY in .env or your environment before running this example.\n", wantExit: 1,
		},
		{
			name: "blank_dotenv_key_no_network", envFile: "JANUARY_API_KEY=\n",
			wantOutput: "Set JANUARY_API_KEY in .env or your environment before running this example.\n", wantExit: 1,
		},
		{
			name: "malformed_dotenv_safe_error", key: fakeKey, envFile: "JANUARY_API_KEY=\"sk-fake-file-key\n",
			wantOutput: "Unable to load .env. Check that it is readable and contains valid KEY=value entries.\n", wantExit: 1,
		},
		{
			name: "no_results", key: fakeKey, status: 200,
			body:       `{"total_count":0,"items":[]}`,
			wantOutput: "Found 0 foods.\nNo results.\n", wantCalls: 1,
		},
		{
			name: "missing_key_no_network", status: 500,
			wantOutput: "Set JANUARY_API_KEY in .env or your environment before running this example.\n", wantExit: 1,
		},
		{
			name: "server_failure_redacted", key: fakeKey, status: 503,
			body:      `{"code":"SERVER_ECHO","message":"` + fakeKey + `","private":"response-body-marker"}`,
			requestID: "offline-request",
			wantOutput: "Food search failed: status=503 code=\"SERVER_ECHO\" request_id=\"offline-request\".\n" +
				"Contact support@january.ai with these safe diagnostic fields.\n", wantExit: 1, wantCalls: 1,
		},
		{
			name: "client_token_rejected_no_network", key: "ct-quickstart-offline-fixture",
			wantOutput: "Invalid January client configuration. Use a server sk- API key, not a ct- client token.\n",
			wantExit:   1,
		},
		{
			name: "unauthorized", key: fakeKey, status: 401,
			body: `{"code":"unauthorized","message":"response-body-marker"}`, requestID: "offline-request",
			wantOutput: "Food search failed: status=401 code=\"unauthorized\" request_id=\"offline-request\".\n" +
				"Check that JANUARY_API_KEY is the full, active server sk- key for your organization.\n", wantExit: 1, wantCalls: 1,
		},
		{
			name: "forbidden", key: fakeKey, status: 403,
			body: `{"code":"forbidden","message":"response-body-marker"}`, requestID: "offline-request",
			wantOutput: "Food search failed: status=403 code=\"forbidden\" request_id=\"offline-request\".\n" +
				"Check your organization's access and the key's permissions; client tokens are not needed for server food search.\n", wantExit: 1, wantCalls: 1,
		},
		{
			name: "rate_limited", key: fakeKey, status: 429,
			body: `{"code":"rate_limited","message":"response-body-marker"}`, requestID: "offline-request",
			wantOutput: "Food search failed: status=429 code=\"rate_limited\" request_id=\"offline-request\".\n" +
				"Reduce request frequency and wait before explicitly trying again; this example does not retry.\n", wantExit: 1, wantCalls: 1,
		},
		{
			name: "credit_limit_exceeded", key: fakeKey, status: 429,
			body: `{"code":"credit_limit_exceeded","message":"response-body-marker"}`, requestID: "offline-request",
			wantOutput: "Food search failed: status=429 code=\"credit_limit_exceeded\" request_id=\"offline-request\".\n" +
				"Check your plan and credit allowance at https://dashboard.january.ai/billing; this example does not retry.\n", wantExit: 1, wantCalls: 1,
		},
		{
			name: "credential_echo_in_metadata", key: fakeKey, status: 500,
			body: `{"code":"echo-` + fakeKey + `","message":"` + fakeKey + `","private":"response-body-marker"}`, requestID: "echo-" + fakeKey,
			wantOutput: "Food search failed: status=500 code=\"echo-[REDACTED]\" request_id=\"echo-[REDACTED]\".\n" +
				"Contact support@january.ai with these safe diagnostic fields.\n", wantExit: 1, wantCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/v1.2/foods" || r.URL.RawQuery != "query=banana" {
					t.Error("expected exactly the banana food-search request")
				}
				if r.Header.Get("Authorization") != "Bearer "+fakeKey || r.Header.Get("X-End-User-ID") != "january-quickstart" {
					t.Error("expected synthetic credential and quickstart user")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", tt.requestID)
				w.Header().Set("X-Private-Fixture", "response-body-marker")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			command := exec.CommandContext(ctx, binary, server.URL)
			root := t.TempDir()
			command.Dir = filepath.Join(root, "app")
			if err := os.Mkdir(command.Dir, 0755); err != nil {
				t.Fatal(err)
			}
			// A synthetic parent file must never supply the working directory's key.
			if err := os.WriteFile(filepath.Join(root, ".env"), []byte("JANUARY_API_KEY=sk-fake-file-key\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if tt.envFile != "" {
				if err := os.WriteFile(filepath.Join(command.Dir, ".env"), []byte(tt.envFile), 0600); err != nil {
					t.Fatal(err)
				}
			}
			command.Env = []string{}
			if tt.key != "" || tt.keySet {
				command.Env = append(command.Env, "JANUARY_API_KEY="+tt.key)
			}
			output, err := command.CombinedOutput()
			if command.ProcessState == nil || command.ProcessState.ExitCode() != tt.wantExit {
				t.Fatalf("unexpected executable exit: %v", err)
			}
			if string(output) != tt.wantOutput {
				t.Errorf("unexpected safe output: %q", output)
			}
			for _, forbidden := range []string{fakeKey, "sk-fake-file-key", "ct-quickstart-offline-fixture", "response-body-marker"} {
				if strings.Contains(string(output), forbidden) {
					t.Error("output leaked a synthetic credential or server response")
				}
			}
			if calls.Load() != tt.wantCalls {
				t.Errorf("got %d requests, want %d", calls.Load(), tt.wantCalls)
			}
		})
	}
}

func readREADMEQuickstart(t *testing.T) string {
	t.Helper()
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	const start = "<!-- quickstart:start -->\n```go\n"
	const end = "```\n<!-- quickstart:end -->"
	_, rest, ok := strings.Cut(string(readme), start)
	if !ok {
		t.Fatal("README quickstart start marker missing")
	}
	code, _, ok := strings.Cut(rest, end)
	if !ok {
		t.Fatal("README quickstart end marker missing")
	}
	return code
}

func TestREADMEQuickstartMatchesSource(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if readREADMEQuickstart(t) != string(source) {
		t.Fatal("README quickstart must match examples/quickstart/main.go byte for byte")
	}
}

func TestREADMEPublicInstallation(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(content)
	for _, command := range []string{"go get github.com/January-ai/january-server-sdk-go@latest", "go get github.com/joho/godotenv@v1.5.1", "go mod init example.com/january-quickstart", "test -e .env || cp .env.example .env", "go run ."} {
		if !strings.Contains(readme, command) {
			t.Errorf("README missing public setup command %q", command)
		}
	}
	for _, phrase := range []string{"unpublished", "until publication", "after publication", "for sdk access", "go mod edit -replace"} {
		if strings.Contains(strings.ToLower(readme), phrase) {
			t.Errorf("README contains private-checkout instruction %q", phrase)
		}
	}
}

func TestEnvExampleAPIKeyOnly(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	var assignments []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			assignments = append(assignments, line)
		}
	}
	if len(assignments) != 1 || assignments[0] != "JANUARY_API_KEY=" {
		t.Fatal(".env.example must contain only a blank API key and safety comments")
	}
}

func TestQuickstartTimeoutDiagnostic(t *testing.T) {
	var output strings.Builder
	err := &january.TransportError{Kind: "private-transport-message", Cause: fmt.Errorf("sk-fake-timeout-secret: %w", context.DeadlineExceeded)}
	printSearchFailure(&output, err)
	want := "Food search failed: transport timeout.\nCheck network access; review the 30-second deadline before trying again.\n"
	if output.String() != want {
		t.Fatal("timeout must produce only the fixed safe diagnostic and hint")
	}
}

func TestFreshConsumerWorkflow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	consumer := filepath.Join(t.TempDir(), "january-quickstart")
	if err := os.Mkdir(consumer, 0755); err != nil {
		t.Fatal(err)
	}
	sdkRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runGo := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(ctx, "go", args...)
		command.Dir = consumer
		command.Env = offlineGoEnv()
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("consumer go %v failed: %v\n%s", args, err, output)
		}
		return string(output)
	}
	runGo("mod", "init", "example.com/january-quickstart")
	runGo("mod", "edit", "-replace=github.com/January-ai/january-server-sdk-go="+sdkRoot)
	runGo("mod", "edit", "-require=github.com/January-ai/january-server-sdk-go@v0.0.0")
	// The example's application dependency is pinned and already cached for offline use.
	runGo("mod", "edit", "-require=github.com/joho/godotenv@v1.5.1")
	// Save the README's complete example, not a separately maintained consumer.
	if err := os.WriteFile(filepath.Join(consumer, "main.go"), []byte(readREADMEQuickstart(t)), 0644); err != nil {
		t.Fatal(err)
	}
	installOfflineTransport(t, consumer)
	runGo("mod", "tidy")
	if err := os.WriteFile(filepath.Join(consumer, ".env"), []byte("JANUARY_API_KEY=sk-fresh-consumer-fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1.2/foods" || r.URL.RawQuery != "query=banana" ||
			r.Header.Get("Authorization") != "Bearer sk-fresh-consumer-fixture" || r.Header.Get("X-End-User-ID") != "january-quickstart" {
			t.Error("fresh consumer did not send the expected synthetic banana search")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":1,"items":[{"id":42,"name":"Banana","nutrients":{},"servings":[]}]}`)
	}))
	defer server.Close()
	command := exec.CommandContext(ctx, "go", "run", ".", server.URL)
	command.Dir = consumer
	command.Env = offlineGoEnv()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fresh consumer execution failed: %v\n%s", err, output)
	}
	if string(output) != "Found 1 foods.\nFirst food: Banana\n" || calls.Load() != 1 {
		t.Fatalf("fresh consumer: expected one successful search; got %d requests and output %q", calls.Load(), output)
	}
	t.Log("Fresh module: init, local SDK replace/require, godotenv dependency, README main.go plus test-only transport, tidy, temporary .env key, go run . with internal fixture argument; one localhost banana search passed.")
}
