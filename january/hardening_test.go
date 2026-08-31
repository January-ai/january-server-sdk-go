package january

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestErrorClassificationMatchesPython(t *testing.T) {
	statuses := map[int]error{400: ErrBadRequest, 401: ErrAuthentication, 403: ErrPermissionDenied, 404: ErrNotFound, 413: ErrPayloadTooLarge, 429: ErrRateLimit, 500: ErrInternalServer, 502: ErrInternalServer, 503: ErrInternalServer, 504: ErrInternalServer}
	for status, want := range statuses {
		for _, code := range []string{"", "future_code", "unauthorized", "not_found", "rate_limited", "credit_limit_exceeded"} {
			t.Run(fmt.Sprintf("%d/%s", status, code), func(t *testing.T) {
				if code == "rate_limited" {
					want = ErrRateLimit
				}
				if code == "credit_limit_exceeded" {
					want = ErrCreditLimitExceeded
				}
				if !errors.Is(&APIError{StatusCode: status, Code: code}, want) {
					t.Fatal("wrong classification")
				}
			})
		}
	}
}

func TestRetryPolicyAndBudgets(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 413, 429, 500, 501, 502, 503, 504} {
		for _, code := range []string{"credit_limit_exceeded", "invalid_request", "unauthorized", "forbidden", "not_found", "not_implemented", "payload_too_large"} {
			if retryableStatus(status, code) {
				t.Fatalf("retried permanent %d/%s", status, code)
			}
		}
	}
	for _, code := range []string{"rate_limited", "internal_error", "upstream_error", "service_unavailable", "upstream_timeout"} {
		if !retryableStatus(500, code) {
			t.Fatal(code)
		}
	}
	for _, status := range []int{429, 500, 502, 503, 504} {
		if !retryableStatus(status, "future_code") {
			t.Fatal(status)
		}
	}
	for _, op := range []operation{opCreateFoodLog, opMintClientToken, opRevokeClientTokens} {
		if _, ok, _ := retryDelay(op, &APIError{StatusCode: 503}, 0, 0); ok {
			t.Fatalf("ambiguous write retried: %s", op.ID)
		}
	}
	if _, ok, _ := retryDelay(opRevokeClientTokens, &APIError{StatusCode: 429}, 0, 0); ok {
		t.Fatal("revocation retried")
	}
	for _, tc := range []struct {
		value  string
		waited time.Duration
	}{{"61", 0}, {"40", 30 * time.Second}} {
		err := &APIError{StatusCode: 429, Response: &Response{RetryAfter: tc.value}}
		if _, ok, _ := retryDelay(opSearchFoods, err, 0, tc.waited); ok || err.RetryNote == "" {
			t.Fatal("unbounded wait")
		}
	}
	for _, raw := range []string{"NaN", "Inf", "bad", ""} {
		if _, ok := ParseRetryAfter(raw, time.Now()); ok {
			t.Fatal(raw)
		}
	}
	if delay, ok := ParseRetryAfter("0.25", time.Now()); !ok || delay != 250*time.Millisecond {
		t.Fatal(delay, ok)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if delay, ok := ParseRetryAfter(now.Add(time.Second).Format(http.TimeFormat), now); !ok || delay != time.Second {
		t.Fatal(delay, ok)
	}
}

func TestRetriesThroughRealHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		retries    Optional[int]
		want       int
	}{{"default", "rate_limited", Optional[int]{}, 3}, {"disabled", "rate_limited", Value(0), 1}, {"credits", "credit_limit_exceeded", Optional[int]{}, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(429)
				fmt.Fprintf(w, `{"code":%q,"message":"slow down"}`, tc.code)
			}))
			defer server.Close()
			c, err := NewClient(Config{SecretKey: "test", BaseURL: server.URL, MaxRetries: tc.retries})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = c.Foods.Search(context.Background(), SearchFoodsRequest{Query: "banana"})
			if err == nil || int(calls.Load()) != tc.want {
				t.Fatal(calls.Load(), err)
			}
		})
	}
}

func TestErrorDiagnosticsAreRedactedAndBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "sk-secret")
		w.Header().Set("X-Debug", "ct-token")
		w.WriteHeader(403)
		fmt.Fprintf(w, `{"code":"forbidden","message":%q}`, strings.Repeat("sk-secret ", 100))
	}))
	defer server.Close()
	c, _ := NewClient(Config{SecretKey: "sk-secret", BaseURL: server.URL})
	_, response, err := c.Credits(context.Background())
	var api *APIError
	if !errors.As(err, &api) || !errors.Is(err, ErrPermissionDenied) {
		t.Fatal(err)
	}
	if len(api.Message) > 240 || strings.Contains(api.Body, "sk-secret") || strings.Contains(fmt.Sprint(response.Headers), "ct-token") || response.RequestID != "[REDACTED]" {
		t.Fatal("unsafe diagnostics")
	}
}

func TestImageInputsAndPreparation(t *testing.T) {
	var original bytes.Buffer
	if err := png.Encode(&original, image.NewNRGBA(image.Rect(0, 0, 8, 4))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "food.png")
	if err := os.WriteFile(path, original.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	want := imageDataURI(original.Bytes(), "image/png")
	for name, source := range map[string]any{"bytes": original.Bytes(), "reader": bytes.NewReader(original.Bytes()), "path": path, "data": want, "url": "https://example.com/food.jpg"} {
		t.Run(name, func(t *testing.T) {
			got, err := PrepareImage(source)
			if err != nil {
				t.Fatal(err)
			}
			if name == "url" {
				if got != source {
					t.Fatal("URL was changed")
				}
			} else if got != want {
				t.Fatal("compliant bytes were changed")
			}
		})
	}
	for _, source := range []any{[]byte{}, []byte("invalid"), bytes.NewReader(nil), "file:///private/photo", 123} {
		if _, err := PrepareImage(source); err == nil {
			t.Fatalf("accepted invalid %T", source)
		}
	}
	if _, err := PrepareImage(path + "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := PrepareImage(image.NewRGBA(image.Rect(0, 0, 2, 2)), ImageOptions{DisablePreprocessing: true}); err == nil {
		t.Fatal("image.Image needs encoding")
	}
	large := image.NewNRGBA(image.Rect(0, 0, 2048, 1024))
	uri, err := PrepareImage(large)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := base64.StdEncoding.DecodeString(strings.SplitN(uri, ",", 2)[1])
	if err != nil {
		t.Fatal(err)
	}
	decoded, format, err := image.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if format != "jpeg" || decoded.Bounds().Dx() != 1024 || decoded.Bounds().Dy() != 512 || len(encoded) > MaxImageBytes {
		t.Fatal("wrong image output")
	}
	r, g, b, _ := decoded.At(0, 0).RGBA()
	if min(r, g, b) < 65000 {
		t.Fatal("transparency was not flattened on white")
	}
	p := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.White, color.Black})
	var animation bytes.Buffer
	if err := gif.EncodeAll(&animation, &gif.GIF{Image: []*image.Paletted{p, p}, Delay: []int{0, 0}}); err != nil {
		t.Fatal(err)
	}
	for _, disabled := range []bool{false, true} {
		if _, err := PrepareImage(animation.Bytes(), ImageOptions{DisablePreprocessing: disabled}); err == nil {
			t.Fatal("accepted animation")
		}
	}
}

func TestEXIFOrientations(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	src.Set(0, 0, color.White)
	for orientation := 1; orientation <= 8; orientation++ {
		result := orientImage(src, orientation)
		if orientation >= 5 && result.Bounds().Dx() != 2 {
			t.Fatal(orientation)
		}
	}
	// Little-endian TIFF Orientation=6 in a JPEG APP1 segment.
	exif := []byte{0xff, 0xd8, 0xff, 0xe1, 0, 34, 'E', 'x', 'i', 'f', 0, 0, 'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xd9}
	if imageOrientation(exif) != 6 {
		t.Fatal("EXIF orientation lost")
	}
	for i := 0; i < len(exif); i++ {
		_ = imageOrientation(exif[:i])
	}
}
