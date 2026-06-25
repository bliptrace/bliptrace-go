package g8scope

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedTransport drives HTTPTransport's retry loop with a pre-baked sequence
// of (status, retryAfterSec) responses. It records every body it was asked to
// send and every delay it was asked to sleep — no network, no real sleeping.
type scriptedTransport struct {
	*HTTPTransport
	script [][2]int // {status, retryAfterSec} per attempt
	calls  int
	bodies [][]byte
	delays []time.Duration
}

func newScripted(policy RetryPolicy, script [][2]int) *scriptedTransport {
	s := &scriptedTransport{
		HTTPTransport: &HTTPTransport{policy: policy},
		script:        script,
	}
	s.HTTPTransport.attempt = func(_ /*url*/, _ /*key*/ string, body []byte, _ time.Duration) (int, int) {
		// copy the body so a later mutation could never alias what we recorded.
		b := make([]byte, len(body))
		copy(b, body)
		s.bodies = append(s.bodies, b)
		i := s.calls
		s.calls++
		if i < len(s.script) {
			return s.script[i][0], s.script[i][1]
		}
		return 0, 0
	}
	s.HTTPTransport.sleep = func(d time.Duration) { s.delays = append(s.delays, d) }
	// deterministic jitter so recorded delays are exact.
	s.HTTPTransport.jitter = func() float64 { return 0.5 }
	return s
}

func envFixture() Envelope {
	return Envelope{SchemaVersion: "1.0", EventID: "fixed-event-id", Exception: Exception{Type: "E"}}
}

func TestTransportRetrySequences(t *testing.T) {
	tests := []struct {
		name       string
		policy     RetryPolicy
		script     [][2]int
		wantCalls  int
		wantDelays []time.Duration
	}{
		{
			name:       "no retry on first success",
			policy:     RetryPolicy{MaxRetries: 3, BaseMs: 100, CapMs: 2000},
			script:     [][2]int{{202, 0}},
			wantCalls:  1,
			wantDelays: nil,
		},
		{
			name:       "retries 5xx then stops on success",
			policy:     RetryPolicy{MaxRetries: 5, BaseMs: 100, CapMs: 2000},
			script:     [][2]int{{503, 0}, {500, 0}, {202, 0}},
			wantCalls:  3,
			wantDelays: []time.Duration{100 * time.Millisecond, 200 * time.Millisecond},
		},
		{
			name:       "gives up after maxRetries on persistent failure",
			policy:     RetryPolicy{MaxRetries: 2, BaseMs: 100, CapMs: 2000},
			script:     [][2]int{{500, 0}, {500, 0}, {500, 0}, {500, 0}},
			wantCalls:  3, // 1 initial + 2 retries
			wantDelays: []time.Duration{100 * time.Millisecond, 200 * time.Millisecond},
		},
		{
			name:       "does not retry a non-retryable 4xx",
			policy:     RetryPolicy{MaxRetries: 5, BaseMs: 100, CapMs: 2000},
			script:     [][2]int{{400, 0}},
			wantCalls:  1,
			wantDelays: nil,
		},
		{
			name:       "retries a transport error (status 0)",
			policy:     RetryPolicy{MaxRetries: 2, BaseMs: 100, CapMs: 2000},
			script:     [][2]int{{0, 0}, {202, 0}},
			wantCalls:  2,
			wantDelays: []time.Duration{100 * time.Millisecond},
		},
		{
			name:       "honours Retry-After on 429, capped",
			policy:     RetryPolicy{MaxRetries: 3, BaseMs: 100, CapMs: 5000},
			script:     [][2]int{{429, 2}, {202, 0}},
			wantCalls:  2,
			wantDelays: []time.Duration{2000 * time.Millisecond},
		},
		{
			name:       "zero maxRetries never retries",
			policy:     RetryPolicy{MaxRetries: 0, BaseMs: 100, CapMs: 2000},
			script:     [][2]int{{503, 0}},
			wantCalls:  1,
			wantDelays: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScripted(tt.policy, tt.script)
			s.Send("http://api", "k", envFixture(), time.Second)

			if s.calls != tt.wantCalls {
				t.Errorf("calls = %d, want %d", s.calls, tt.wantCalls)
			}
			if len(s.delays) != len(tt.wantDelays) {
				t.Fatalf("delays = %v, want %v", s.delays, tt.wantDelays)
			}
			for i, d := range tt.wantDelays {
				if s.delays[i] != d {
					t.Errorf("delay[%d] = %v, want %v", i, s.delays[i], d)
				}
			}

			// Idempotency: every attempt must replay the identical body (event_id).
			for i := 1; i < len(s.bodies); i++ {
				if string(s.bodies[i]) != string(s.bodies[0]) {
					t.Errorf("body[%d] differs from body[0]: %q vs %q", i, s.bodies[i], s.bodies[0])
				}
			}
			if len(s.bodies) > 0 && !strings.Contains(string(s.bodies[0]), "fixed-event-id") {
				t.Errorf("body missing event_id: %s", s.bodies[0])
			}
		})
	}
}

// TestTransportRealServerRetriesAndDedupes exercises the real httpAttempt path
// against an httptest.Server that fails twice (503 then 429+Retry-After) before
// accepting, asserting every request carried the identical event_id and the
// X-Ingest-Key header.
func TestTransportRealServerRetriesAndDedupes(t *testing.T) {
	var (
		mu       sync.Mutex
		eventIDs []string
		keys     []string
	)

	statuses := []int{http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusAccepted}
	var n int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var env map[string]any
		_ = json.Unmarshal(body, &env)

		mu.Lock()
		id, _ := env["event_id"].(string)
		eventIDs = append(eventIDs, id)
		keys = append(keys, r.Header.Get("X-Ingest-Key"))
		i := n
		n++
		mu.Unlock()

		status := http.StatusAccepted
		if i < len(statuses) {
			status = statuses[i]
		}
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "1") // 1s, honoured (and well under cap)
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()

	// Real attempt + real header parsing, but a recording (non-blocking) sleep so
	// the test doesn't actually wait out the backoff.
	tr := newHTTPTransport(Config{MaxRetries: 5, RetryBaseMs: 1, RetryCapMs: 5000}.withDefaults())
	var slept []time.Duration
	tr.sleep = func(d time.Duration) { slept = append(slept, d) }
	tr.jitter = func() float64 { return 0.5 }

	env := envFixture()
	tr.Send(srv.URL, "secret-key", env, 2*time.Second)

	mu.Lock()
	defer mu.Unlock()

	if len(eventIDs) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(eventIDs))
	}
	for i, id := range eventIDs {
		if id != "fixed-event-id" {
			t.Errorf("request %d event_id = %q, want fixed-event-id (dedupe relies on it)", i, id)
		}
		if keys[i] != "secret-key" {
			t.Errorf("request %d X-Ingest-Key = %q, want secret-key", i, keys[i])
		}
	}
	// Two failures → two backoff sleeps; the 429 one must honour Retry-After (1s).
	if len(slept) != 2 {
		t.Fatalf("expected 2 sleeps, got %v", slept)
	}
	if slept[1] != time.Second {
		t.Errorf("second sleep = %v, want 1s from Retry-After", slept[1])
	}
}

func TestTransportNeverBlocksBeyondCap(t *testing.T) {
	// A persistently failing server with a hostile Retry-After must still be
	// clamped to CapMs per attempt.
	p := RetryPolicy{MaxRetries: 3, BaseMs: 100, CapMs: 500}
	s := newScripted(p, [][2]int{{429, 9999}, {429, 9999}, {429, 9999}, {429, 9999}})
	s.Send("http://api", "k", envFixture(), time.Second)
	for i, d := range s.delays {
		if d > 500*time.Millisecond {
			t.Errorf("delay[%d] = %v exceeds cap 500ms", i, d)
		}
	}
}

func TestTransportNeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Send panicked: %v", r)
		}
	}()
	// attempt that panics must be swallowed by httpAttempt's guard analogue; here
	// we panic from the injected attempt and rely on Send's recover.
	tr := &HTTPTransport{policy: RetryPolicy{MaxRetries: 2, BaseMs: 1, CapMs: 10}}
	tr.attempt = func(string, string, []byte, time.Duration) (int, int) { panic("boom") }
	tr.sleep = func(time.Duration) {}
	tr.Send("http://api", "k", envFixture(), time.Second)
}

func TestHTTPAttemptParsesRetryAfter(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   int
	}{
		{"numeric seconds", "5", 5},
		{"whitespace trimmed", "  7 ", 7},
		{"empty", "", 0},
		{"non-numeric (http-date) ignored", "Wed, 21 Oct 2026 07:28:00 GMT", 0},
		{"negative ignored", "-3", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRetryAfter(tt.header); got != tt.want {
				t.Errorf("parseRetryAfter(%q) = %d, want %d", tt.header, got, tt.want)
			}
		})
	}
}
