package january

import (
	"context"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func retryableStatus(status int, code string) bool {
	switch code {
	case "rate_limited", "internal_error", "upstream_error", "service_unavailable", "upstream_timeout":
		return true
	case "credit_limit_exceeded", "invalid_request", "unauthorized", "forbidden", "not_found", "not_implemented", "payload_too_large":
		return false
	}
	return status == 429 || status == 500 || status == 502 || status == 503 || status == 504
}

// ParseRetryAfter accepts seconds or an HTTP date. Invalid values return false.
func ParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return 0, false
		}
		if seconds <= 0 {
			return 0, true
		}
		if seconds >= float64(math.MaxInt64)/float64(time.Second) {
			return time.Duration(math.MaxInt64), true
		}
		return time.Duration(seconds * float64(time.Second)), true
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	return max(0, date.Sub(now)), true
}

func retryDelay(op operation, err error, attempt int, waited time.Duration) (time.Duration, bool, bool) {
	if op.RetryNever || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0, false, false
	}
	var api *APIError
	if errors.As(err, &api) {
		if !retryableStatus(api.StatusCode, api.Code) || (api.StatusCode != 429 && !op.RetryAmbiguous) {
			return 0, false, false
		}
		if api.Response != nil {
			if delay, ok := ParseRetryAfter(api.Response.RetryAfter, time.Now()); ok {
				if delay > time.Minute || delay > time.Minute-waited {
					api.RetryNote = "Retry-After exceeds the 60-second per-wait or total wait limit; no wait was made"
					return 0, false, false
				}
				return delay, true, true
			}
		}
	} else {
		var transportError *TransportError
		if !errors.As(err, &transportError) || (transportError.Kind != "HTTP request failed" && transportError.Kind != "response read failed") {
			return 0, false, false
		}
		var netOp *net.OpError
		cause := transportError.Cause
		var urlError *url.Error
		if errors.As(cause, &urlError) {
			cause = urlError.Err
		}
		preSend := errors.As(cause, &netOp) && netOp.Op == "dial"
		var netError net.Error
		ambiguous := errors.As(cause, &netError) || errors.Is(cause, io.EOF) || errors.Is(cause, io.ErrUnexpectedEOF)
		if !preSend && !(op.RetryAmbiguous && ambiguous) {
			return 0, false, false
		}
	}
	ceiling := 500 * time.Millisecond * time.Duration(1<<min(attempt, 4))
	return time.Duration(float64(ceiling) * (0.75 + 0.25*rand.Float64())), true, false
}

func execute(ctx context.Context, s service, op operation, input any, output any) (*Response, error) {
	timeout := s.transport.timeout
	if !s.transport.explicitTimeout && (op.ID == "scanFoodPhoto" || op.ID == "searchFoodsByNaturalLanguage" || op.ID == "correctPhotoScan") {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var waited time.Duration
	for attempt := 0; ; attempt++ {
		response, err := executeOnce(ctx, s, op, input, output)
		if err == nil || attempt >= s.transport.maxRetries {
			return response, err
		}
		delay, retry, serverWait := retryDelay(op, err, attempt, waited)
		if !retry {
			return response, err
		}
		if deadline, ok := ctx.Deadline(); ok && delay >= time.Until(deadline) {
			var api *APIError
			if errors.As(err, &api) {
				api.RetryNote = "Retry waiting would exceed the request deadline; no wait was made"
			}
			return response, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return response, &TransportError{Kind: "HTTP request canceled", Cause: ctx.Err()}
		case <-timer.C:
		}
		if serverWait {
			waited += delay
		}
	}
}
