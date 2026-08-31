# Contributing

For questions and feedback, contact [support@january.ai](mailto:support@january.ai).

## Build and test

Use Go 1.26 or newer. Run these commands from the Go SDK root; tests use synthetic
credentials and localhost services, never the real `.env` or production API:

```sh
go test -race ./...
go build ./...
go vet ./...
```

The normal suite includes the compiled quickstart's success, empty-result,
missing-key, and redacted-failure flows, plus a byte-for-byte README/source check.
Dotenv checks use fake files in temporary working directories only; they cover
file loading, existing environment precedence, malformed files, and no ancestor search.
The focused suite also initializes a fresh consumer module, uses the documented
local replacement and README source, and runs it against a localhost fixture.
When changing the quickstart, update both
[its source](examples/quickstart/main.go) and the marked README code block.

Focused documentation/example checks:

```sh
go test -race ./examples/quickstart -v
go build -o /tmp/january-go-quickstart ./examples/quickstart
```

Building the quickstart never sends requests. Running it does; automated tests
compile a test-only transport helper alongside the unchanged example in a temporary
directory and supply synthetic credentials through temporary `.env` files or an
isolated environment. Only that helper accepts an internal
localhost argument; it is not part of the production executable.

The [CI workflow](.github/workflows/ci.yml) configures Go 1.26.x and 1.27.x,
checks formatting, runs the race-enabled suite, builds the module and legacy
consumer, and installs/runs the local HTTP consumer. No publishing workflow or
registry credentials are configured.

## Distribution and consumers

To try local SDK changes in a separate application, start in the application's
intended parent directory and replace the checkout path below:

```sh
sdk_checkout='/absolute/path/to/january-server-sdk-go'
mkdir january-quickstart
cd january-quickstart
go mod init example.com/january-quickstart
go mod edit -replace="github.com/January-ai/january-server-sdk-go=$sdk_checkout"
go mod edit -require=github.com/January-ai/january-server-sdk-go@v0.0.0
cp "$sdk_checkout/examples/quickstart/main.go" ./main.go
go get github.com/joho/godotenv@v1.5.1
go mod tidy
go build .
```

The local replacement resolves `v0.0.0` to your working copy. Building sends no
requests. To run the example, follow the key setup in the README; running it calls
the production API using the SDK's built-in endpoint. Use the offline tests above
to exercise local fixtures.

`go test -run '^TestModuleDistribution$' .` constructs a module ZIP and consumes it
through a temporary local Go proxy without a replace directive. The
[test consumer](testdata/consumer/main.go) checks public imports, FoodPortion,
generated request models, and localhost HTTP calls. This does not publish anything.

The standalone [local HTTP consumer](examples/local-http/main.go) has its own module
and local replacement. From the SDK root:

```sh
(cd examples/local-http && go run .)
```

It uses an in-process localhost server and synthetic credentials, making exactly
four requests: scoped food search, token minting, token revocation, and credits.
It does not log tokens. This is distinct from the one-request quickstart.

## Generation

From the contract repository:

```sh
node tools/server-sdk/go.mjs --contract artifacts/server-sdk/contract.json --output ../january-server-sdk-go
node tools/server-sdk/go.mjs --contract artifacts/server-sdk/contract.json --output ../january-server-sdk-go --check
```

The generator uses Node builtins and Go's formatter. It derives operation wrappers, typed models,
HTTP bindings, validation schemas, and the default origin from the artifact. It records raw contract
and generator SHA-256 hashes in `sdk-contract.lock.json` and all 18 methods in `sdk-surface.json`.
The sibling fixture artifact is copied into testdata so SDK tests do not need the contract checkout.
Do not edit `zz_generated_*` files or maintain endpoint paths by hand. Runtime code is handwritten.

## Legacy compatibility

The older HTTP backend example remains in [examples/go](examples/go/README.md).
From the SDK root, build it with:

```sh
(cd examples/go && go build -o /tmp/january-go-legacy-example .)
```

The prototype `ClientTokens.Create`, demo issuer, and HTTP issuer constructors
remain compatibility aliases. Their relay JSON contains `token` and `expiresIn`;
legacy Go `AccessToken` and `TokenType` fields remain available. New integrations
should use root `MintClientToken`.

See [live testing](docs/live-testing.md) for the separate, explicitly opted-in
credit-consuming demo. [Back to the README](README.md).
