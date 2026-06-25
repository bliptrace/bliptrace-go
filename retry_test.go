package g8scope

import "testing"

func TestRetryableClassification(t *testing.T) {
	// maxRetries high so attempt never gates the classification.
	p := RetryPolicy{MaxRetries: 5, BaseMs: 100, CapMs: 2000}
	tests := []struct {
		name   string
		status int
		want   bool
	}{
		{"transport error / timeout", 0, true},
		{"request timeout 408", 408, true},
		{"too many requests 429", 429, true},
		{"internal server error 500", 500, true},
		{"bad gateway 502", 502, true},
		{"service unavailable 503", 503, true},
		{"ok 200", 200, false},
		{"accepted 202", 202, false},
		{"bad request 400", 400, false},
		{"unauthorized 401", 401, false},
		{"forbidden 403", 403, false},
		{"not found 404", 404, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.isRetryable(tt.status); got != tt.want {
				t.Errorf("isRetryable(%d) = %v, want %v", tt.status, got, tt.want)
			}
			// ShouldRetry mirrors isRetryable while attempts remain.
			if got := p.ShouldRetry(tt.status, 0); got != tt.want {
				t.Errorf("ShouldRetry(%d, 0) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestShouldRetryStopsAtMaxRetries(t *testing.T) {
	p := RetryPolicy{MaxRetries: 2, BaseMs: 100, CapMs: 2000}
	tests := []struct {
		name    string
		attempt int
		want    bool
	}{
		{"first retry allowed", 0, true},
		{"second retry allowed", 1, true},
		{"exhausted", 2, false},
		{"beyond cap", 3, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.ShouldRetry(503, tt.attempt); got != tt.want {
				t.Errorf("ShouldRetry(503, %d) = %v, want %v", tt.attempt, got, tt.want)
			}
		})
	}
}

func TestShouldRetryZeroMaxRetriesNeverRetries(t *testing.T) {
	p := RetryPolicy{MaxRetries: 0, BaseMs: 100, CapMs: 2000}
	if p.ShouldRetry(503, 0) {
		t.Error("MaxRetries=0 must never retry")
	}
}

func TestDelayExponentialGrowthAndCap(t *testing.T) {
	p := RetryPolicy{MaxRetries: 5, BaseMs: 100, CapMs: 2000}
	noJitter := func() float64 { return 0.5 } // 2*0.5-1 = 0 → no jitter

	tests := []struct {
		name    string
		attempt int
		want    int
	}{
		{"attempt 0", 0, 100},
		{"attempt 1", 1, 200},
		{"attempt 2", 2, 400},
		{"attempt 3", 3, 800},
		{"attempt 4", 4, 1600},
		{"attempt 5 capped", 5, 2000},
		{"attempt 8 capped", 8, 2000},
		{"huge attempt no overflow", 62, 2000},
		{"negative attempt clamps to 0", -3, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.DelayMs(tt.attempt, 0, noJitter); got != tt.want {
				t.Errorf("DelayMs(%d) = %d, want %d", tt.attempt, got, tt.want)
			}
		})
	}
}

func TestDelayJitterWithinBounds(t *testing.T) {
	// Large cap so the cap never masks the jitter band.
	p := RetryPolicy{MaxRetries: 5, BaseMs: 100, CapMs: 100000}
	// attempt 3 → exponential base 800ms; ±25% → [600, 1000].
	const base, lo, hi = 800, 600, 1000

	tests := []struct {
		name   string
		jitter func() float64
		want   int // exact when deterministic
	}{
		{"jitter 0.0 → -25%", func() float64 { return 0.0 }, lo},
		{"jitter 0.5 → 0%", func() float64 { return 0.5 }, base},
		{"jitter ~1.0 → +25%", func() float64 { return 0.999 }, 999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.DelayMs(3, 0, tt.jitter)
			if got < lo || got > hi {
				t.Errorf("DelayMs jitter band: got %d, want within [%d,%d]", got, lo, hi)
			}
			if got != tt.want {
				t.Errorf("DelayMs = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestDelayDefaultJitterStaysInBounds(t *testing.T) {
	// nil jitter uses the real random source; result must still respect ±25% + cap.
	p := RetryPolicy{MaxRetries: 5, BaseMs: 100, CapMs: 100000}
	for i := 0; i < 1000; i++ {
		got := p.DelayMs(3, 0, nil)
		if got < 600 || got > 1000 {
			t.Fatalf("default-jitter DelayMs out of band: %d", got)
		}
	}
}

func TestDelayRetryAfterHonouredAndCapped(t *testing.T) {
	p := RetryPolicy{MaxRetries: 5, BaseMs: 100, CapMs: 5000}
	tests := []struct {
		name          string
		retryAfterSec int
		want          int
	}{
		{"2s honoured", 2, 2000},
		{"hostile value capped", 999, 5000},
		{"zero falls through to backoff", 0, 100}, // attempt 0 backoff, no jitter below
		{"negative ignored", -5, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.DelayMs(0, tt.retryAfterSec, func() float64 { return 0.5 })
			if got != tt.want {
				t.Errorf("DelayMs(0, %d) = %d, want %d", tt.retryAfterSec, got, tt.want)
			}
		})
	}
}

func TestDelayAlwaysClampedToCap(t *testing.T) {
	p := RetryPolicy{MaxRetries: 5, BaseMs: 100, CapMs: 2000}
	// Maximum jitter at a near-cap exponential must never exceed the cap.
	maxJitter := func() float64 { return 0.999 }
	for attempt := 0; attempt < 10; attempt++ {
		got := p.DelayMs(attempt, 0, maxJitter)
		if got < 0 || got > p.CapMs {
			t.Errorf("DelayMs(%d) = %d, out of [0,%d]", attempt, got, p.CapMs)
		}
	}
}

func TestNewRetryPolicyFromConfig(t *testing.T) {
	cfg := Config{Key: "k", MaxRetries: 7, RetryBaseMs: 50, RetryCapMs: 9000}.withDefaults()
	p := newRetryPolicy(cfg)
	if p.MaxRetries != 7 || p.BaseMs != 50 || p.CapMs != 9000 {
		t.Errorf("policy = %+v, want {7 50 9000}", p)
	}
}

func TestConfigRetryDefaults(t *testing.T) {
	cfg := Config{Key: "k"}.withDefaults()
	tests := []struct {
		name string
		got  int
		want int
	}{
		{"MaxRetries", cfg.MaxRetries, defaultMaxRetries},
		{"RetryBaseMs", cfg.RetryBaseMs, defaultRetryBase},
		{"RetryCapMs", cfg.RetryCapMs, defaultRetryCap},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %d, want %d", tt.name, tt.got, tt.want)
		}
	}
}

func TestConfigNegativeMaxRetriesDisables(t *testing.T) {
	cfg := Config{Key: "k", MaxRetries: -1}.withDefaults()
	if cfg.MaxRetries != 0 {
		t.Errorf("MaxRetries = %d, want 0 (disabled)", cfg.MaxRetries)
	}
}
