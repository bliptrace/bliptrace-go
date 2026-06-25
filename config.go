package g8scope

import "time"

// Config is the SDK configuration surface. Build it directly and pass it to New,
// which fills in any zero-valued defaults.
//
// Key resolves the project server-side — it is never placed in the payload, so a
// client cannot spoof another tenant.
type Config struct {
	// Key is the ingest key. The server resolves the project from this.
	Key string
	// Endpoint is the ingest API base URL. The SDK POSTs to {Endpoint}/v1/ingest.
	Endpoint string
	// Environment, e.g. production, staging, local.
	Environment string
	// Release marker, e.g. my-app@2026.06.21.
	Release string
	// Timeout is the per-request HTTP timeout.
	Timeout time.Duration
	// MaxFrames caps the number of stack frames captured.
	MaxFrames int
	// MaxMessage caps the exception message length (in bytes).
	MaxMessage int
	// IgnoreErrors lists error type names (e.g. "*errors.errorString") to skip.
	IgnoreErrors []string
	// ScrubKeys lists context keys (case-insensitive) to redact before sending.
	ScrubKeys []string
	// BasePath marks frames whose file path starts with it as in-app.
	BasePath string
	// Disabled turns capture off entirely. Capture is enabled by default, so the
	// zero Config captures normally; set Disabled to true to opt out. (The wire
	// envelope never carries this — it only gates the SDK.)
	Disabled bool
	// SDKName / SDKVersion identify this SDK in the envelope.
	SDKName    string
	SDKVersion string

	// Enabled is the resolved effective flag (Enabled == !Disabled), set by New.
	// Callers do not need to set this; use Disabled to opt out.
	Enabled bool
}

// Default values for unset fields.
const (
	defaultEndpoint   = "https://ingest.scope.g8suite.com"
	defaultEnviron    = "production"
	defaultTimeout    = 3 * time.Second
	defaultMaxFrames  = 30
	defaultMaxMessage = 2000
	defaultSDKName    = "g8scope-go"
	defaultSDKVersion = "1.0.0"
)

// withDefaults returns a copy of cfg with zero-valued fields filled in. The
// effective Enabled flag is resolved separately in New (Enabled == !Disabled).
func (cfg Config) withDefaults() Config {
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultEndpoint
	}
	if cfg.Environment == "" {
		cfg.Environment = defaultEnviron
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxFrames == 0 {
		cfg.MaxFrames = defaultMaxFrames
	}
	if cfg.MaxMessage == 0 {
		cfg.MaxMessage = defaultMaxMessage
	}
	if cfg.SDKName == "" {
		cfg.SDKName = defaultSDKName
	}
	if cfg.SDKVersion == "" {
		cfg.SDKVersion = defaultSDKVersion
	}
	return cfg
}
