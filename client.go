// Package g8scope is the runtime Go SDK for the g8scope error-capture platform.
//
// It builds a language-neutral error envelope from a Go error, scrubs PII from
// the supplied context, and ships it to the ingest API. The capture path is
// fully guarded: it will never panic into the host application.
//
// The project is resolved server-side from the ingest key — it is never placed
// in the payload, so a client cannot spoof another tenant.
package g8scope

import "strings"

// Client decides whether to capture, builds + scrubs the envelope, and ships it.
type Client struct {
	cfg       Config
	transport Transport
	scrubber  *Scrubber
}

// New constructs a Client, filling unset Config fields with their defaults.
//
// Enabled defaults to true. Because Go's bool zero value is false, New treats an
// unset Enabled as enabled — so Config{Key: "k"} captures normally. To turn
// capture off, set Config.Disabled to true.
func New(cfg Config) *Client {
	resolved := cfg.withDefaults()
	resolved.Enabled = !cfg.Disabled

	return &Client{
		cfg:       resolved,
		transport: newHTTPTransport(resolved),
		scrubber:  NewScrubber(resolved.ScrubKeys),
	}
}

// NewWithTransport is like New but injects a custom Transport (used in tests).
func NewWithTransport(cfg Config, transport Transport) *Client {
	c := New(cfg)
	if transport != nil {
		c.transport = transport
	}
	return c
}

// Capture builds and ships an envelope for err immediately. It is fully guarded
// and never panics into the caller.
func (c *Client) Capture(err error, ctx map[string]interface{}) {
	defer func() { _ = recover() }()

	env, ok := c.BuildEnvelope(err, ctx)
	if !ok {
		return
	}
	c.SendEnvelope(env)
}

// BuildEnvelope builds and scrubs the envelope. The second return value is false
// when capture is disabled, unconfigured (no key), the error is nil, or the
// error type is ignored. It never panics.
func (c *Client) BuildEnvelope(err error, ctx map[string]interface{}) (env Envelope, ok bool) {
	defer func() {
		if recover() != nil {
			env, ok = Envelope{}, false
		}
	}()

	if !c.cfg.Enabled || c.cfg.Key == "" {
		return Envelope{}, false
	}
	if err == nil {
		return Envelope{}, false
	}
	if c.shouldIgnore(err) {
		return Envelope{}, false
	}

	return BuildEnvelope(err, c.cfg, c.scrubber.Scrub(ctx)), true
}

// SendEnvelope ships a previously built envelope. It no-ops without a key and
// swallows all transport errors.
func (c *Client) SendEnvelope(env Envelope) {
	defer func() { _ = recover() }()
	if c.cfg.Key == "" {
		return
	}
	c.transport.Send(c.cfg.Endpoint, c.cfg.Key, env, c.cfg.Timeout)
}

func (c *Client) shouldIgnore(err error) bool {
	if len(c.cfg.IgnoreErrors) == 0 {
		return false
	}
	t := errorType(err)
	for _, name := range c.cfg.IgnoreErrors {
		if name == t || strings.TrimPrefix(t, "*") == strings.TrimPrefix(name, "*") {
			return true
		}
	}
	return false
}

// compile-time assertion that the default transport satisfies the interface.
var _ Transport = (*HTTPTransport)(nil)
