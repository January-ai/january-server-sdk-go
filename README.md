# January Server SDK for Go

[![CI](https://github.com/January-ai/january-server-sdk-go/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/January-ai/january-server-sdk-go/actions/workflows/ci.yml)
[![Go 1.26+](https://img.shields.io/badge/go-1.26%2B-00ADD8.svg)](https://github.com/January-ai/january-server-sdk-go/blob/main/go.mod)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](https://github.com/January-ai/january-server-sdk-go/blob/main/LICENSE)

Typed Go access to January food search, analysis, food logs, and glucose prediction,
plus server-only token and credit operations. Requires Go 1.26 or newer.

Keep secret API keys on trusted servers, never in browser or mobile apps.

## Contents

- [Quick start](#quick-start)
- [Detailed setup and credentials](#detailed-setup-and-credentials)
- [Complete diagnostic example](#complete-diagnostic-example)
- [Common tasks](#common-tasks)
- [Server-only operations](#server-only-operations)
- [Configuration and errors](#configuration-and-errors)
- [Examples and testing](#examples-and-testing)
- [Distribution and releases](#distribution-and-releases)
- [Reference, support, and contributing](#reference-support-and-contributing)
- [License](#license)

## Quick start

### 1. Create and configure a server API key

[Sign in to the Developer Dashboard](https://dashboard.january.ai/dashboard),
open **API keys → Create key**, and copy the full `sk-…` value when it is shown.
Keep it on your trusted backend and never commit it.

```sh
export JANUARY_API_KEY=sk-your-server-api-key
```

### 2. Install, connect, and make the first request

```sh
mkdir january-quickstart
cd january-quickstart
go mod init example.com/january-quickstart
go get github.com/January-ai/january-server-sdk-go@latest
```

Save this as `main.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/January-ai/january-server-sdk-go/january"
)

func main() {
	secretKey := strings.TrimSpace(os.Getenv("JANUARY_API_KEY"))
	if secretKey == "" {
		panic("JANUARY_API_KEY is required")
	}
	client, err := january.NewClient(january.Config{
		SecretKey: secretKey,
	})
	if err != nil {
		panic(err)
	}
	user, err := client.ForUser("january-quickstart", "UTC")
	if err != nil {
		panic(err)
	}
	foods, _, err := user.Foods.Search(
		context.Background(),
		january.SearchFoodsRequest{Query: "banana"},
	)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Found %d foods\n", len(foods.Items))
}
```

Run it with `go run .`. A successful request prints a result count; an empty
result is still a successful connection. Replace the synthetic ID with the
stable ID from your authenticated server session. This read-only request may
consume API credits.

This server SDK accepts server API keys (`sk-…`), not client tokens (`ct-…`).
Client tokens are needed only when your backend serves a browser or mobile app;
see [server-only operations](#server-only-operations) for token creation.

## Detailed setup and credentials

<details>
<summary>Account, billing, and new-module details</summary>

1. [Sign up](https://dashboard.january.ai/sign-up) or
   [sign in](https://dashboard.january.ai/sign-in) to the developer platform.
   If you have no active organization, complete **Set up your organization** first.
2. Open the [dashboard](https://dashboard.january.ai/dashboard): **API keys** →
   **Create key** → enter a **Key name** → **Create key**. Copy the full `sk-` key
   when it is shown once, and store it in your secrets manager.
3. Check [Billing](https://dashboard.january.ai/billing) for your current plan and
   credit allowance. The first API call is billable; the SDK does not automatically
   retry. Allowances and costs depend on your plan.

Dashboard login authenticates you as a human. This backend SDK authenticates with
the server API key (`sk-`), supplied explicitly through `JANUARY_API_KEY` in the
examples below. A `ct-` client token is a short-lived end-user credential for
mobile/web clients and is rejected by this server SDK. Client tokens are optional
and **not needed for server food search**. To mint client tokens, open
[Client tokens](https://dashboard.january.ai/dashboard/client-tokens), choose
**Enable client tokens**, then call `CreateClientToken` on your backend.
This enablement is required for minting, not for revocation.

### Package installation

In an existing Go module, install the SDK:

```sh
go get github.com/January-ai/january-server-sdk-go@latest
```

For a new application, create a module first:

```sh
mkdir january-quickstart
cd january-quickstart
go mod init example.com/january-quickstart
go get github.com/January-ai/january-server-sdk-go@latest
```

Import the client package as `github.com/January-ai/january-server-sdk-go/january`.

</details>

## Complete diagnostic example

The tested [repository example](examples/quickstart/main.go) adds `.env` loading,
credential checks, timeouts, and sanitized error handling to the same request.

Save the Go example below as `main.go` in your application directory. Install
`godotenv`, an application helper for local `.env` loading (not SDK configuration):

```sh
go get github.com/joho/godotenv@v1.5.1
```

Create `.env` in that same directory using your editor, and set your server key:

```dotenv
JANUARY_API_KEY=sk-replace-with-your-server-api-key
```

Add `.env` to your application's `.gitignore` before adding the real key:

```gitignore
.env
```

Keep the file private; never commit or share it. Then run from that directory:

```sh
go mod tidy
go run .
```

In a local SDK checkout, [.env.example](.env.example) provides the blank key
template and `.env` is already ignored. From the SDK root:

```sh
test -e .env || cp .env.example .env
# Edit .env and set JANUARY_API_KEY, then explicitly opt in:
go run ./examples/quickstart
```

This command uses the SDK's built-in production endpoint and is billable.
A missing key fails before any request.
Failures exit nonzero with safe status/code/request-ID diagnostics and a fixed
actionable hint, without raw errors, messages, headers, or response bodies.
There are no automatic retries.

The quickstart uses `godotenv.Load()` to read only the current working directory's
`.env`; it does not search parent directories. Existing environment values take
precedence, including empty values. Exported `JANUARY_API_KEY` remains supported
for deployments, and a missing `.env` is fine when the key is already supplied.
An unreadable or malformed `.env` produces a fixed safe error before any request.
The SDK itself never loads files or environment variables: the example passes
the key explicitly to `NewClient`. The separate [live E2E runner](docs/live-testing.md)
has its own loader. The quickstart uses the synthetic user `january-quickstart`
and timezone UTC. In production, derive the user ID from your authenticated user,
not caller-supplied identity.

<details>
<summary>Complete diagnostic source</summary>

<!-- quickstart:start -->
```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/January-ai/january-server-sdk-go/january"
	"github.com/joho/godotenv"
)

func main() {
	os.Exit(run(os.Stdout, os.Stderr))
}

func run(stdout, stderr io.Writer) int {
	// Load only the working directory's .env; existing environment values win.
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stderr, "Unable to load .env. Check that it is readable and contains valid KEY=value entries.")
		return 1
	}
	key := strings.TrimSpace(os.Getenv("JANUARY_API_KEY"))
	if key == "" {
		fmt.Fprintln(stderr, "Set JANUARY_API_KEY in .env or your environment before running this example.")
		return 1
	}
	client, err := january.NewClient(january.Config{
		SecretKey:  key,
		Timeout:    30 * time.Second,
		MaxRetries: january.Value(0), // Keep this diagnostic example single-attempt.
	})
	if err != nil {
		fmt.Fprintln(stderr, "Invalid January client configuration. Use a server sk- API key, not a ct- client token.")
		return 1
	}
	// Synthetic demo identity; use your authenticated user's ID in production.
	user, err := client.ForUser("january-quickstart", "UTC")
	if err != nil {
		fmt.Fprintln(stderr, "Unable to configure the January user context.")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	foods, _, err := user.Foods.Search(ctx, january.SearchFoodsRequest{Query: "banana"})
	if err != nil {
		printSearchFailure(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Found %d foods.\n", len(foods.Items))
	if len(foods.Items) == 0 {
		fmt.Fprintln(stdout, "No results.")
	} else {
		name := "(unnamed)"
		if foods.Items[0].Name != nil {
			name = *foods.Items[0].Name
		}
		fmt.Fprintf(stdout, "First food: %s\n", name)
	}
	return 0
}

func printSearchFailure(stderr io.Writer, err error) {
	var apiError *january.APIError
	hint := "Contact support@january.ai with these safe diagnostic fields."
	switch {
	case errors.As(err, &apiError):
		// Use SDK-sanitized fields, not raw Response headers or error/body text.
		// Quoting also escapes control characters in server-supplied identifiers.
		fmt.Fprintf(stderr, "Food search failed: status=%d code=%q request_id=%q.\n",
			apiError.StatusCode, apiError.Code, apiError.RequestID)
		switch {
		case apiError.StatusCode == 401:
			hint = "Check that JANUARY_API_KEY is the full, active server sk- key for your organization."
		case apiError.StatusCode == 403:
			hint = "Check your organization's access and the key's permissions; client tokens are not needed for server food search."
		case apiError.StatusCode == 429 && apiError.Code == "rate_limited":
			hint = "Reduce request frequency and wait before explicitly trying again; this example does not retry."
		case apiError.StatusCode == 429 && apiError.Code == "credit_limit_exceeded":
			hint = "Check your plan and credit allowance at https://dashboard.january.ai/billing; this example does not retry."
		case apiError.StatusCode == 429:
			hint = "Check rate limits and your plan's credit allowance before explicitly trying again."
		}
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(stderr, "Food search failed: transport timeout.")
		hint = "Check network access; review the 30-second deadline before trying again."
	default:
		fmt.Fprintln(stderr, "Food search failed: transport or response error.")
		hint = "Check network access; contact support@january.ai if the problem persists."
	}
	fmt.Fprintln(stderr, hint)
}
```
<!-- quickstart:end -->

</details>

## Common tasks

Reuse a client across goroutines. Treat its resource handles as read-only.
`ForUser` creates an immutable shared-resource view whose identity overrides
per-call user values; timezone applies where the contract declares it.

| Resource | Methods |
| --- | --- |
| `Foods` | `Search`, `Autocomplete`, `SuggestAlternatives`, `LookupBarcode`, `Get` |
| `Restaurants` | `Search`, `GetMenuItems`, `SearchMenuItems` |
| `FoodAnalysis` | `AnalyzePhoto`, `AnalyzeDescription`, `Correct` |
| `FoodLogs` | `List`, `Get`, `Create`, `Update`, `Delete` |
| `Glucose` | `Predict` |

All network methods take `context.Context` first. JSON operations return typed
data, response metadata, and an error. Input names follow the production contract:
barcode `Barcode` preserves leading zeros, description uses `Query`, photo uses
`Image` (base64/data URI), IDs are opaque strings, and food-log listing uses
`StartDate`, `EndDate`, and `Timezone`. Model names retain contract vocabulary.

Fragments below assume `user` and `ctx` from the quickstart; check each returned
error before using the result:

```go
analysis, _, err := user.FoodAnalysis.AnalyzeDescription(ctx,
    january.SearchFoodsByNaturalLanguageRequest{Query: "two eggs"})
foods, _, err := user.Foods.LookupBarcode(ctx,
    january.LookupFoodByBarcodeRequest{Barcode: "049000006346"})
```

### FoodPortion: local serving calculations

`NewFoodPortion` makes no API call. This fragment assumes `food` is a returned
`january.FoodSearchItem`, inside a function returning an error:

```go
portion, err := january.NewFoodPortion(food, january.FoodPortionOptions{
    Quantity: january.Value(2.0),
})
if err != nil {
    return err
}
logInput := january.CreateFoodLogRequest{
    Foods: []january.FoodLogInputFood{portion.Selection},
}
glucoseInput := january.PredictGlucoseRequest{
	Timezone:  "UTC",
	StartTime: "2026-08-30T12:00:00Z",
    UserProfile: january.GlucosePredictionProfile{
        Age: 30, Sex: january.SexMale,
        Height: january.Height{Value: 175, Unit: january.HeightUnitCm},
        Weight: january.Weight{Value: 70, Unit: january.WeightUnitKg},
    },
    Foods: []january.FoodLogInputFood{portion.Selection},
}
```

The prediction input includes a synthetic adult profile and meal start time in
UTC. Use the actual user's profile and meal time when calling
`user.Glucose.Predict(ctx, glucoseInput)`; constructing these inputs is local.

`food.Portion(options)` is equivalent. Optional `ServingID` selects an exact
match; otherwise the first primary serving wins, then the first serving.
Omitted quantity uses that serving's quantity. Requested quantity must be finite,
positive, and at most 10,000; explicit zero or null is invalid. The selected
serving's quantity and scaling factor must be finite and positive.

All 16 nutrients and glycemic load scale by
`quantity * scalingFactor / serving.quantity`. Units, absent nutrients, and real
zero values are preserved. Weight scales by `quantity / serving.quantity`;
glycemic index is unchanged. Optional output values distinguish absence from zero.
The input is not mutated and the serving weight pointer is copied; create a new
portion to change quantity. `Selection` uses the generated `FoodLogInputFood`
type accepted by food-log and glucose requests.

Use `errors.As` with `*january.FoodPortionError` to inspect `Code`:
`no_servings`, `serving_not_found`, `invalid_serving`, or `invalid_quantity`.
The complete [portion example](examples/portions/main.go) runs entirely offline.

## Server-only operations

Only the root client exposes `CreateClientToken`, `RevokeClientTokens`, and
`GetCredits`; these are not methods on the `ForUser` view.

Independent fragments below assume `client`, `ctx`, and a trusted
`authenticatedUserID` are available. Check each error before using its result:

Token creation requires **Enable client tokens** in the
[Client tokens dashboard](https://dashboard.january.ai/dashboard/client-tokens).
Use root `GetCredits` to read the balance and [Billing](https://dashboard.january.ai/billing)
to check your current plan and credit allowance. These are
separate operations, not a workflow: do not revoke tokens immediately after
minting them unless that is the action your application intends.

```go
token, _, err := client.CreateClientToken(ctx, january.CreateClientTokenRequest{
    EndUserID: authenticatedUserID,
    Scopes: []string{"foods:read"},
    TTLSeconds: january.Value(int64(1800)),
})

result, response, err := client.RevokeClientTokens(ctx, january.RevokeClientTokensRequest{
    EndUserID: authenticatedUserID,
})

balance, _, err := client.GetCredits(ctx)
```

Revocation is one POST with `end_user_id`, returning a typed result whose
`RevokedCount` is the number revoked by that request. There is no revoke-all loop,
even when that count is 500. Never log minted tokens.
See [legacy compatibility](CONTRIBUTING.md#legacy-compatibility) for prototype aliases.

## Configuration and errors

The SDK accepts explicit `Config` values; it does not read shell variables or
files. The quickstart loads the current directory's `.env` without replacing
existing environment values, then passes `JANUARY_API_KEY` explicitly to the SDK.

- Generated JSON fields use idiomatic Go names and exact wire JSON tags.
- Nutrient maps may be sparse or empty. Omitted nutrients stay absent; a returned
  numeric zero remains a supplied value. Response validation checks contract-required
  fields before typed decoding, so a nutrient amount missing `value` or `unit` is
  rejected instead of silently becoming zero. Response enums, ranges, and formats
  are not constrained by request validation; extra response fields remain tolerated.
- Optional fields use `Optional[T]`: zero value omits, `Value(v)` supplies, and `Null[T]()`
  preserves explicit null. Validation permits null only where the contract allows it.
- `IsSet`, `IsNull`, `Get` distinguish absent/uncapped values from zero. Unknown response fields
  are ignored; unknown response enum values are accepted. Dates remain wire-format strings.
- Default timeout is 30 seconds (120 for photo/description analysis and correction) for the entire call; configure `Config.Timeout` and pass a shorter
  context deadline when needed. Cancellation is preserved through `errors.Is`.
- Two bounded retries by default, using stable API error codes and `Retry-After` (up to 60 seconds per wait and total server-requested waiting). Set `MaxRetries: january.Value(0)` to disable. Credit exhaustion and permanent errors are never retried. Token creation and food-log creation are not replayed after ambiguous failures; revocation always makes one request. Retried analysis can consume additional credits. No automatic pagination or revoke-all loops. See [photos, errors and retries](docs/images-and-errors.md).
- Production requests use the SDK's built-in HTTPS endpoint. Redirects are not followed.
- Per-call `Response` includes status, cloned headers, request ID, Retry-After, and revoked count.
- Inspect `*january.APIError` with `errors.As` for status, code, message, docs URL, and request ID.
  `*january.TransportError` wraps the original cause. Error strings and generated model formatting
  redact sensitive data; explicit JSON serialization intentionally contains the requested data.
- No telemetry or logging is enabled. A response size limit protects the client from unbounded bodies.

### Troubleshooting your first request

| Symptom | Next step |
| --- | --- |
| Missing key or local configuration error; no HTTP status | Set `JANUARY_API_KEY` to the full server `sk-` key in the working directory's `.env`, not a `ct-` token. Existing environment values take precedence. |
| Unable to load `.env`; no HTTP status | Check the working directory's file permissions and `KEY=value` syntax. Do not share the file or its contents. |
| HTTP 401 | Check that the full key is active and belongs to the intended organization; a dashboard login is not an API credential. |
| HTTP 403 | Check organization access and key permissions. For token minting only, check **Enable client tokens**; enabling it is not required for server food search. |
| HTTP 429, `rate_limited` | Default clients retry within configured limits. The quickstart and production test runner explicitly disable retries. |
| HTTP 429, `credit_limit_exceeded` | Check [Billing](https://dashboard.january.ai/billing) for the current plan/allowance; root `Credits` reads the balance. Do not repeatedly retry the request. |
| Transport timeout; no HTTP status | Check connectivity. Review the 30-second deadline before explicitly trying again. |

The complete quickstart uses `errors.As` to read sanitized `APIError.StatusCode`,
`Code`, and `RequestID`, and quotes identifiers to escape control characters.
Example safe diagnostic output (illustrative values):

```text
Food search failed: status=401 code="unauthorized" request_id="example-request".
Check that JANUARY_API_KEY is the full, active server sk- key for your organization.
```

Share these diagnostic fields with [support@january.ai](mailto:support@january.ai),
never keys, tokens, raw error messages, response bodies, or headers.

## Examples and testing

For repository examples and tests, follow the [contributor setup](CONTRIBUTING.md#build-and-test).
From the repository root, these commands are offline and need no API key:

```sh
go run ./examples/portions
go test -race ./examples/quickstart -v
(cd examples/local-http && go run .)
```

The quickstart tests compile and run the actual executable against localhost
fixtures through a test-only transport helper, with isolated fake credentials.
They use only temporary fake `.env` files and cover file loading, environment
precedence, no parent-directory search, malformed-file redaction, success, no results,
missing-key/no-network, actionable failure diagnostics, redaction, and
README/source parity. They also create a fresh consumer module from the README
source, resolve the SDK locally for testing, and exercise one localhost
request. These checks are included in the normal CI test command.

The [live E2E demo](docs/live-testing.md) is a separate explicit opt-in that uses
real credits and exercises all 20 operations with cleanup. Its `.env` setup,
safety rules, options, and reporting are documented there.
See [contributor checks](CONTRIBUTING.md#build-and-test) for full offline verification.

## Distribution and releases

The SDK is distributed as the Go source module `github.com/January-ai/january-server-sdk-go`.
Versioned repository tags identify releases; Go downloads the module through its
module system, so no separate package registry account is needed.

`go get github.com/January-ai/january-server-sdk-go@latest` installs or updates to the latest
release. You can select a specific version instead of `latest`. Commit `go.mod`
and `go.sum` to record your dependencies and their checksums. See
[distribution checks](CONTRIBUTING.md#distribution-and-consumers) for contributor
installation tests.

Maintainers publish a Go release by pushing a semantic version tag. The release
workflow verifies that commit and creates a draft GitHub release for review.
The repository must be public before external users can resolve the documented
module path without private GitHub credentials.

## Reference, support, and contributing

- [API reference (Swagger)](https://partners.january.ai/v1.2/docs#/)
- [Generated operation surface](sdk-surface.json) and [contract lock](sdk-contract.lock.json)
- [Contributing, generation, builds, and compatibility](CONTRIBUTING.md)
- For support and feedback, email [support@january.ai](mailto:support@january.ai).

## License

The Apache 2.0 license applies to the source code in this repository. It does not grant rights to nutrition data, food images, or other content returned by the January API, which are subject to the January API Developer Terms.
