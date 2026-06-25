package g8scope

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Transport ships an envelope to the ingest API. Implementations must never
// return a value that breaks the host app — errors are swallowed.
type Transport interface {
	Send(endpoint, key string, envelope Envelope, timeout time.Duration)
}

// HTTPTransport is the default, dependency-free transport built on net/http.
//
// It retries a briefly-failing or load-shedding ingest API (transport error,
// 408, 429, 5xx) with exponential backoff + jitter. The same serialised body —
// hence the same event_id — is replayed on every attempt, so the ingest API's
// idempotency dedupe makes a retry safe: it never creates a duplicate
// occurrence (spec §10).
type HTTPTransport struct {
	// Client is optional; when nil a per-send client honouring the timeout is used.
	Client *http.Client
	// policy governs retries. Its zero value (all caps 0) disables retrying, so
	// always construct via NewHTTPTransport / newHTTPTransport.
	policy RetryPolicy

	// attempt performs one HTTP attempt and reports the status (0 on a transport
	// error/timeout) and a parsed numeric Retry-After in seconds (0 = none). It is
	// a field so tests can script status sequences without real network.
	attempt func(url, key string, body []byte, timeout time.Duration) (status, retryAfterSec int)
	// sleep blocks for d. A field so tests can record delays without real sleeping.
	sleep func(d time.Duration)
	// jitter is the backoff jitter source ([0,1)); nil means a real random draw.
	jitter func() float64
}

// NewHTTPTransport returns the default transport with default retry knobs.
func NewHTTPTransport() *HTTPTransport {
	return newHTTPTransport(Config{}.withDefaults())
}

// newHTTPTransport builds the default transport wired with the config's retry
// policy. cfg is expected to be already defaulted.
func newHTTPTransport(cfg Config) *HTTPTransport {
	t := &HTTPTransport{policy: newRetryPolicy(cfg)}
	t.attempt = t.httpAttempt
	t.sleep = time.Sleep
	return t
}

// Send POSTs the envelope to {endpoint}/v1/ingest, retrying per the policy. All
// errors are swallowed so that capturing can never break the monitored app, and
// the total wait can never exceed the configured cap.
func (t *HTTPTransport) Send(endpoint, key string, envelope Envelope, timeout time.Duration) {
	defer func() { _ = recover() }()

	body, err := json.Marshal(envelope)
	if err != nil {
		return
	}

	url := strings.TrimRight(endpoint, "/") + "/v1/ingest"

	if timeout <= 0 {
		timeout = defaultTimeout
	}

	// Retry loop. The same body (identical event_id) is replayed each attempt.
	for attempt := 0; ; attempt++ {
		status, retryAfterSec := t.attempt(url, key, body, timeout)
		if !t.policy.ShouldRetry(status, attempt) {
			return
		}
		delay := t.policy.DelayMs(attempt, retryAfterSec, t.jitter)
		if delay > 0 {
			t.sleep(time.Duration(delay) * time.Millisecond)
		}
	}
}

// httpAttempt performs one real HTTP attempt. It returns status 0 on any
// transport error/timeout, and the numeric Retry-After header (seconds) if the
// server sent one.
func (t *HTTPTransport) httpAttempt(url, key string, body []byte, timeout time.Duration) (status, retryAfterSec int) {
	defer func() {
		if recover() != nil {
			status, retryAfterSec = 0, 0
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, 0
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ingest-Key", key)

	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, 0 // transport error / timeout
	}
	// Drain and close so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	return resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After"))
}

// parseRetryAfter reads a numeric Retry-After value (seconds). Non-numeric or
// negative values yield 0 (no server-directed delay). The HTTP-date form is not
// honoured — only the seconds form, matching the PHP SDK.
func parseRetryAfter(v string) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
