# Go token server example

This runnable `net/http` partner backend exposes `POST /api/january/token` and
uses the local January Go module through the checked-in `replace` directive.
Requires Go 1.26 or newer.

**Local demo only:** `x-demo-user-id` is not authentication. Before production,
replace it with the user ID from your application's verified session or JWT.
Never trust a caller-supplied user ID or expose your server API key to clients.
The server selects `foods:read` and a TTL of 1800 seconds; request-body user IDs,
scopes, and lifetimes are ignored.

## Run locally

First follow the [account and API key setup](../../README.md#detailed-setup-and-credentials).
Minting additionally requires **Enable client tokens** in the
[Client tokens dashboard](https://dashboard.january.ai/dashboard/client-tokens).
Client-token enablement is not needed for server food search or revocation.

Starting at the Go SDK checkout root, use the existing blank
[.env.example](../../.env.example) template without overwriting any local file:

```sh
cd examples/go
test -e .env || cp ../../.env.example .env
# Edit examples/go/.env in your editor and set JANUARY_API_KEY to your server sk- key.
# Stay in examples/go for both commands below:
go mod download
go run .
```

The exact working directory is `<SDK checkout>/examples/go`. `godotenv.Load()`
reads only that directory's `.env`, not the root `.env` or any ancestor file.
The template key is blank; you must fill it in privately. `.env` is ignored by Git;
never commit, log, or share it. Existing environment values take precedence,
including empty values. A missing `.env` is allowed if the key is already supplied
by the deployment environment. Missing keys and malformed files fail safely before
any request. The SDK itself does not load environment files.

The backend binds to `127.0.0.1:4030` by default (`PORT` can select another local
port). In a second terminal, request a token for a synthetic demo user:

```sh
curl --fail-with-body -X POST http://127.0.0.1:4030/api/january/token \
  -H 'x-demo-user-id: demo-user'
```

Expected JSON shape (safe placeholder, not a usable token):

```json
{"token":"ct-REDACTED-PLACEHOLDER","expiresIn":1800}
```

The relay uses root `CreateClientToken`, maps its `token` and `expires_in` fields to
the client SDK's `{token, expiresIn}` shape, and exposes no other upstream fields.
Missing/blank demo identity returns HTTP 401 with `{"error":"unauthorized"}`.
Mint failures return HTTP 502 with `{"error":"Unable to mint client token."}`;
raw upstream errors, response bodies, and credentials are not logged or relayed.

January requests use the SDK's built-in production endpoint; there is no API-base-URL
environment setting. Running the server and then requesting a token makes a real
API call and may consume credits. There are no automatic retries, revocations, or
credit checks in this example. Stop the server with Ctrl-C.

## Offline verification

From `<SDK checkout>/examples/go`, run:

```sh
go test -race -v .
```

These tests compile and execute the actual server, load only temporary fake `.env`
files, and route SDK requests to test-owned localhost fixtures using a test-only
transport. They never load existing `.env` files or contact January. The internal
fixture argument is not part of the production executable. This nested module is
not included in the root module's `go test ./...`.
