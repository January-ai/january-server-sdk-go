# Live API-key E2E demo (explicit opt-in)

All commands below run from the Go SDK root. [Back to the README](../README.md).

This is a real, credit-consuming integration run, not a mock and not part of default
tests or CI. It uses the SDK's built-in production endpoint and exercises all 18
SDK operations using synthetic data. No UI is needed.

From a fresh checkout, copy `.env.example` to `.env` **only if `.env` does not already
exist**, then set `JANUARY_API_KEY` in `.env`. Never overwrite an existing `.env`.
Both `.env` and `.e2e-results/` are ignored; only `.env.example` is tracked.

Before running the all-18-operations command below, complete the
[account, organization, API key, and billing prerequisites](../README.md#before-you-begin)
and choose **Enable client tokens** in the
[Client tokens dashboard](https://dashboard.january.ai/dashboard/client-tokens).
Enablement is required for the runner's mint operation; it is **not required for
server food search** or token revocation.

```sh
# Run from the Go SDK root; the guard preserves any existing configuration.
test -e .env || cp .env.example .env
# Edit .env and set JANUARY_API_KEY, then explicitly opt in:
go run ./examples/live
```

The runner reads root `.env` as data without shell evaluation or variable expansion.
Shell environment values override file values, including explicitly empty values.
`JANUARY_ENV_FILE` selects another file (relative paths resolve from the SDK root).
The runner never writes or overwrites an env file. A missing key produces a nonzero
NOT_RUN/configuration result before any network request.

| Setting | Default |
| --- | --- |
| `JANUARY_API_KEY` | Required server secret |
| `JANUARY_E2E_TIMEOUT_SECONDS` | `120` per operation; configurable up to `3600` |
| `JANUARY_E2E_QUERY` | `banana` |
| `JANUARY_E2E_UPC` | `049000006346` |
| `JANUARY_E2E_RESTAURANT_QUERY` | `chicken` |
| `JANUARY_E2E_LATITUDE` / `JANUARY_E2E_LONGITUDE` | `37.7749` / `-122.4194` |
| `JANUARY_E2E_IMAGE_PATH` | `examples/live/food.png` |

The image is read as bytes and sent as a base64 PNG/JPEG data URI. The description
demo analyzes `one banana`. Correction reuses returned detections and meal name.
Food logs and glucose prediction use actual returned food/serving IDs, never stale
fixture IDs; the glucose profile is synthetic.

Each run creates a fresh `sdk-e2e-go-UUID` user with timezone UTC. There is no option
to select an existing user. Independent operations continue after failures;
dependent operations are BLOCKED with a static reason, never counted as passes.
The runner validates the minted token's user, requested scope, token shape, and
expiry. It does not make the optional client-token usability request.

Final cleanup deletes only known logs created by this run. If creation was ambiguous,
it checks this fresh user's logs for the run's unique marker. Unresolved creation or
cleanup failures remain failures. Token revocation is the canonical final operation:
one `RevokeClientTokens` call total, even after an ambiguous mint timeout and even if
the revoked count is 500. There are no automatic retries or revoke-all loops.
Cleanup uses a fresh bounded context after cancellation; an unsuccessful normal log
deletion may receive one explicit cleanup deletion. No immediate revoked-token 401
is asserted because server caches may take 60 seconds to expire.

Output contains only operation labels, statuses, safe codes/request IDs, and static
blocked reasons. A safe report with durations and counts is written atomically to
`.e2e-results/latest.json`; it contains no key, token, user ID, food text, or response
body. Exit is zero only if all 18 operations and cleanup pass. Hard process termination
or machine failure can prevent final cleanup; use ordinary Ctrl-C for bounded cleanup.

Offline runner-only tests use a test-owned client constructor with localhost
fixtures and synthetic credentials only; they do not load the real `.env`:

```sh
go test -race ./examples/live -v
```

Full offline/package checks, without running the live executable:

```sh
go test -race ./...
go build ./...
go build -o /tmp/january-go-live-demo ./examples/live
```

Building does not opt in to live requests. Running `go run ./examples/live` (or the
built demo) does. Run the executable from within the SDK checkout so it can locate
the root config and report directory. Default `go test ./...` never reads live keys.
