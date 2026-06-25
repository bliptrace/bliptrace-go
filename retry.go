package g8scope

import "math/rand"

// RetryPolicy decides whether a failed ingest attempt should be retried, and how
// long to wait before the next one.
//
// The SDK owns "the backend might be briefly down or shedding (429)". Strategy:
// exponential backoff (BaseMs × 2^attempt), ±25% jitter to avoid a synchronised
// thundering herd, and a hard cap on both attempts and delay so capturing can
// never block the monitored app for long. Retries are only safe because the
// envelope's event_id is reused across attempts and the ingest API dedupes on it
// (spec §10).
//
// Pure and immutable: no I/O, no sleeping — that lives in the transport.
type RetryPolicy struct {
	// MaxRetries is the number of retries after the initial attempt.
	MaxRetries int
	// BaseMs is the base backoff delay in milliseconds.
	BaseMs int
	// CapMs caps both the backoff delay and an honoured Retry-After, in ms.
	CapMs int
}

// newRetryPolicy builds a policy from the (already defaulted) config.
func newRetryPolicy(cfg Config) RetryPolicy {
	return RetryPolicy{
		MaxRetries: cfg.MaxRetries,
		BaseMs:     cfg.RetryBaseMs,
		CapMs:      cfg.RetryCapMs,
	}
}

// ShouldRetry reports whether to retry after seeing status, having already done
// attempt retries.
//
// Retryable: transport errors / timeouts (status 0), 408, 429, and any 5xx.
// Never retry a 2xx (success) or other 4xx (the request itself is bad).
func (p RetryPolicy) ShouldRetry(status, attempt int) bool {
	if attempt >= p.MaxRetries {
		return false
	}
	return p.isRetryable(status)
}

func (p RetryPolicy) isRetryable(status int) bool {
	return status == 0 || status == 408 || status == 429 || status >= 500
}

// DelayMs returns the delay before the next attempt, in milliseconds.
//
// A server-sent retryAfterSec (seconds) wins when > 0, capped to CapMs so a
// hostile or misconfigured header can't stall the app. Otherwise: exponential
// backoff with ±25% jitter, capped at CapMs. jitter returns a float in [0,1) and
// is injectable for deterministic tests; when nil it defaults to a uniform random
// draw. The result is always clamped to [0, CapMs].
func (p RetryPolicy) DelayMs(attempt, retryAfterSec int, jitter func() float64) int {
	if retryAfterSec > 0 {
		return min(p.CapMs, retryAfterSec*1000)
	}

	if attempt < 0 {
		attempt = 0
	}
	// Shift one step at a time and stop at the cap, so a large attempt count can
	// never overflow the exponential before it is clamped.
	exp := p.BaseMs
	for i := 0; i < attempt && exp < p.CapMs; i++ {
		exp <<= 1
	}
	exp = min(p.CapMs, exp)

	if jitter == nil {
		jitter = rand.Float64
	}
	delta := int(float64(exp) * 0.25 * (2*jitter() - 1)) // ±25%

	d := exp + delta
	if d < 0 {
		d = 0
	}
	return min(p.CapMs, d)
}
