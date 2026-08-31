# Photos, retries and errors

The API vocabulary is identical to the client SDKs. These helpers prepare local
data; they do not add endpoints or change the OpenAPI contract.

## Photos

```go
image, err := january.PrepareImage("./lunch.jpg")
if err != nil { return err }
analysis, _, err := user.FoodAnalysis.AnalyzePhoto(ctx, january.ScanFoodPhotoRequest{Image: image})
```

`PrepareImage` accepts public HTTP(S) URLs, base64 data URIs, trusted file paths,
`[]byte`, `io.Reader`, and `image.Image`. URLs and data URIs pass through unchanged:
January fetches remote URLs, so they must not require a login or block hotlinking.
Readers start at their current position and remain open. Do not pass an untrusted
user's string as a filesystem path; accept upload bytes instead.

JPEG, PNG, WEBP and still GIF are supported. BMP and TIFF can be converted;
HEIC/HEIF/AVIF require conversion to JPEG/PNG first. Animations are rejected.
Preprocessing corrects JPEG EXIF rotation, limits the longest side to 1024 pixels,
flattens transparency onto white when conversion is needed, and encodes JPEG
under 3.5 MB. Re-encoding removes metadata; unchanged compliant images retain it.
Local sources are bounded to 64 MiB and 40 million decoded pixels. Use
`january.ImageOptions{DisablePreprocessing: true}` only for known-compliant bytes.

## Errors

```go
switch {
case errors.Is(err, january.ErrCreditLimitExceeded):
    // Check the organization's billing allowance; retries cannot restore credits.
case errors.Is(err, january.ErrRateLimit):
    // Automatic retries are exhausted, disabled, or could not honor the deadline.
case errors.Is(err, january.ErrAuthentication):
    // Check the server key.
case errors.Is(err, january.ErrPermissionDenied):
    // Check access to this operation.
}
var apiError *january.APIError
if errors.As(err, &apiError) {
    log.Printf("January status=%d code=%q request_id=%q", apiError.StatusCode, apiError.Code, apiError.RequestID)
}
```

Also available: `ErrBadRequest`, `ErrNotFound`, `ErrPayloadTooLarge`, and
`ErrInternalServer`. `errors.As` still exposes `APIError` with redacted `Body`,
bounded `Message`, and `RetryNote`. Only rate-limit and credit-limit codes override
HTTP status classification, matching Python. Never log arbitrary response data.

The default is two retries. Stable permanent codes override retryable HTTP status;
unknown codes fall back to 429/500/502/503/504. Backoff has jitter and all attempts
share one deadline. Cancellation interrupts waiting. `Retry-After` accepts seconds
or an HTTP date; excessive waits return the error with `RetryNote` immediately.

## Tests and release

`go test -race ./...` covers all 18 generated operations, portion calculations,
errors/retries, photos, quickstart, and a real installed module consumer against
loopback HTTP. `go run ./examples/live` is the separate, billable production check;
it reads the key from `.env` and disables retries. Offline tests are not production
proof. See [live testing](live-testing.md).

Go distributes this SDK as a versioned Git module. After reviewing CI, tag the
approved commit with a semantic version such as `v0.1.0` and push that tag. The
release workflow checks the module and creates a draft GitHub release for review.
Repository visibility is managed separately; this workflow never changes it.
