package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func offlineBuildEnv() []string {
	// Build infrastructure only: never inherit credentials or proxy settings.
	env := []string{"GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local"}
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "GOCACHE", "GOPATH", "GOROOT"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func TestTokenServerOffline(t *testing.T) {
	dir := t.TempDir()
	// Reuse the quickstart's established transport harness. Only the test binary
	// accepts a fixture argument; the unchanged app keeps the production default.
	for name, source := range map[string]string{
		"main.go": "main.go", "transport.go": "../quickstart/testdata/transport.go",
	} {
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	binary := filepath.Join(dir, "token-server")
	build := exec.CommandContext(ctx, "go", "build", "-race", "-o", binary, filepath.Join(dir, "main.go"), filepath.Join(dir, "transport.go"))
	build.Env = offlineBuildEnv()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build token server: %v\n%s", err, output)
	}

	const fakeKey = "sk-token-server-offline-fixture"
	const fakeToken = "ct-token-server-offline-fixture"
	const successBody = `{"token":"` + fakeToken + `","expires_in":1800,"expires_at":"2026-08-30T12:30:00Z","end_user_id":"demo-user","scopes":["foods:read"]}`
	const genericFailure = "{\"error\":\"Unable to mint client token.\"}\n"
	for _, tt := range []struct {
		name, envFile, header, requestBody, upstreamBody, wantBody, startupError string
		upstreamStatus, wantStatus                                               int
		wantCalls                                                                int32
		disconnect                                                               bool
	}{
		{
			name: "dotenv_mint_relay", envFile: "JANUARY_API_KEY=' " + fakeKey + " '\n", header: "demo-user",
			upstreamStatus: 201, upstreamBody: successBody, wantStatus: 200,
			wantBody: "{\"token\":\"" + fakeToken + "\",\"expiresIn\":1800}\n", wantCalls: 1,
		},
		{
			name: "body_cannot_override_identity_scopes_or_ttl", envFile: "JANUARY_API_KEY=" + fakeKey + "\n", header: "demo-user",
			requestBody:    `{"end_user_id":"other-user","userId":"other-user","scopes":["food_logs:write"],"ttl_seconds":7200}`,
			upstreamStatus: 201, upstreamBody: successBody, wantStatus: 200,
			wantBody: "{\"token\":\"" + fakeToken + "\",\"expiresIn\":1800}\n", wantCalls: 1,
		},
		{
			name: "body_identity_cannot_authenticate", envFile: "JANUARY_API_KEY=" + fakeKey + "\n",
			requestBody: `{"end_user_id":"demo-user"}`, wantStatus: 401, wantBody: "{\"error\":\"unauthorized\"}\n",
		},
		{
			name: "blank_identity", envFile: "JANUARY_API_KEY=" + fakeKey + "\n", header: "   ",
			wantStatus: 401, wantBody: "{\"error\":\"unauthorized\"}\n",
		},
		{
			name: "validation_error_safe", envFile: "JANUARY_API_KEY=" + fakeKey + "\n", header: strings.Repeat("x", 65),
			wantStatus: 502, wantBody: genericFailure,
		},
		{
			name: "upstream_error_safe", envFile: "JANUARY_API_KEY=" + fakeKey + "\n", header: "demo-user",
			upstreamStatus: 403, upstreamBody: `{"code":"` + fakeKey + `","message":"` + fakeToken + ` response-body-marker"}`,
			wantStatus: 502, wantBody: genericFailure, wantCalls: 1,
		},
		{
			name: "malformed_response_safe", envFile: "JANUARY_API_KEY=" + fakeKey + "\n", header: "demo-user",
			upstreamStatus: 201, upstreamBody: `{"token":"` + fakeToken + `","private":"response-body-marker"}`,
			wantStatus: 502, wantBody: genericFailure, wantCalls: 1,
		},
		{
			name: "transport_error_safe", envFile: "JANUARY_API_KEY=" + fakeKey + "\n", header: "demo-user",
			disconnect: true, wantStatus: 502, wantBody: genericFailure, wantCalls: 1,
		},
		{name: "missing_key_no_ancestor_search", startupError: "Set JANUARY_API_KEY in .env or your environment before running this example."},
		{name: "blank_key", envFile: "JANUARY_API_KEY=\n", startupError: "Set JANUARY_API_KEY in .env or your environment before running this example."},
		{name: "legacy_key_not_used", envFile: "JANUARY_SECRET_KEY=" + fakeKey + "\n", startupError: "Set JANUARY_API_KEY in .env or your environment before running this example."},
		{name: "malformed_dotenv_safe", envFile: "JANUARY_API_KEY=\"" + fakeKey + "\n", startupError: "Unable to load .env. Check that it is readable and contains valid KEY=value entries."},
		{name: "client_token_rejected", envFile: "JANUARY_API_KEY=" + fakeToken + "\n", startupError: "Invalid January client configuration. Use a server sk- API key, not a ct- client token."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/v1.2/auth/client-tokens" || r.URL.RawQuery != "" {
					t.Error("expected the canonical root mint operation only")
				}
				if r.Header.Get("Authorization") != "Bearer "+fakeKey {
					t.Error("expected the fake API key from the working directory's .env")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error("invalid mint JSON")
				}
				want := map[string]any{"end_user_id": "demo-user", "scopes": []any{"foods:read"}, "ttl_seconds": float64(1800)}
				if !reflect.DeepEqual(body, want) {
					t.Error("mint must use header identity and server-selected scope/TTL only")
				}
				if tt.disconnect {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error("cannot disconnect fixture")
						return
					}
					_ = conn.Close()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", fakeKey)
				status := tt.upstreamStatus
				if status == 0 {
					status = 500
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, tt.upstreamBody)
			}))
			defer upstream.Close()

			parent := t.TempDir()
			// This is test-owned too; prove the app never searches ancestors.
			if err := os.WriteFile(filepath.Join(parent, ".env"), []byte("JANUARY_API_KEY=sk-fake-parent-key\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cwd := filepath.Join(parent, "cwd")
			if err := os.Mkdir(cwd, 0700); err != nil {
				t.Fatal(err)
			}
			if tt.envFile != "" {
				if err := os.WriteFile(filepath.Join(cwd, ".env"), []byte(tt.envFile), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, upstream.URL)
			command.Dir = cwd
			command.Env = []string{"PORT=0"} // All credentials come from fake .env files only.
			var logs string
			if tt.startupError != "" {
				output, err := command.CombinedOutput()
				logs = string(output)
				if err == nil || command.ProcessState == nil || command.ProcessState.ExitCode() != 1 || !strings.Contains(logs, tt.startupError) {
					t.Fatal("expected a safe configuration failure with exit 1")
				}
			} else {
				pipe, err := command.StderrPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() {
					_ = command.Process.Kill()
					_ = command.Wait()
				}()
				reader := bufio.NewReader(pipe)
				line, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal("server did not announce readiness")
				}
				logs = line
				_, address, ok := strings.Cut(strings.TrimSpace(line), "listening on ")
				target, parseErr := url.Parse(address)
				if !ok || parseErr != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" || target.Port() == "" {
					t.Fatal("server must listen on a loopback port")
				}
				request, err := http.NewRequestWithContext(ctx, "POST", address+"/api/january/token", strings.NewReader(tt.requestBody))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("x-demo-user-id", tt.header)
				request.Header.Set("Content-Type", "application/json")
				local := &http.Transport{} // No environment proxy or remote request target.
				defer local.CloseIdleConnections()
				response, err := (&http.Client{Transport: local, Timeout: 5 * time.Second,
					CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
				}).Do(request)
				if err != nil {
					t.Fatal("local token route failed")
				}
				body, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if err != nil || response.StatusCode != tt.wantStatus || string(body) != tt.wantBody {
					t.Fatal("unexpected token relay status or JSON shape")
				}
				if response.Header.Get("Content-Type") != "application/json" || response.Header.Get("X-Request-ID") != "" {
					t.Error("unexpected relay headers")
				}
				_ = command.Process.Kill()
				rest, _ := io.ReadAll(reader)
				logs += string(rest)
				t.Logf("actual executable: POST /api/january/token -> HTTP %d; upstream canonical mint calls=%d", response.StatusCode, calls.Load())
			}
			for _, forbidden := range []string{fakeKey, fakeToken, "sk-fake-parent-key", "response-body-marker"} {
				if strings.Contains(logs, forbidden) {
					t.Error("server logs leaked a synthetic credential or upstream content")
				}
			}
			if calls.Load() != tt.wantCalls {
				t.Errorf("upstream calls=%d, want %d; no retries or unrelated operations allowed", calls.Load(), tt.wantCalls)
			}
		})
	}
}
