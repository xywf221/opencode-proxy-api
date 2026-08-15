package retry

import (
	"net/http"
	"strconv"
	"time"
)

// ParseRetryAfter extracts the delay from an HTTP Retry-After header.
// Returns 0 if the header is absent or cannot be parsed.
//
// Supports two formats:
//   - Seconds: "120" (wait 120 seconds)
//   - HTTP-date: "Wed, 21 Oct 2015 07:28:00 GMT"
func ParseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}

	// Try parsing as seconds (most common case)
	if seconds, err := strconv.ParseInt(header, 10, 64); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}

	// Try parsing as HTTP-date
	if t, err := http.ParseTime(header); err == nil {
		delay := time.Until(t)
		if delay > 0 {
			return delay
		}
	}

	return 0
}

// ExponentialBackoff calculates exponential backoff with a cap.
// failures: number of consecutive failures (1-indexed)
// baseDelay: starting delay (e.g. 15s)
// maxDelay: maximum delay cap (e.g. 2m)
//
// Returns: baseDelay * 2^(failures-1), capped at maxDelay.
// Examples with baseDelay=15s, maxDelay=2m:
//   - failures=1: 15s
//   - failures=2: 30s
//   - failures=3: 60s
//   - failures=4: 120s (capped)
//   - failures=5: 120s (capped)
func ExponentialBackoff(failures uint32, baseDelay, maxDelay time.Duration) time.Duration {
	if failures == 0 {
		return 0
	}

	// Cap the exponent to prevent overflow
	exponent := failures - 1
	if exponent > 4 {
		exponent = 4
	}

	delay := baseDelay * (1 << exponent)
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}
