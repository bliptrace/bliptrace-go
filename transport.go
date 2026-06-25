package g8scope

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// Transport ships an envelope to the ingest API. Implementations must never
// return a value that breaks the host app — errors are swallowed.
type Transport interface {
	Send(endpoint, key string, envelope Envelope, timeout time.Duration)
}

// HTTPTransport is the default, dependency-free transport built on net/http.
type HTTPTransport struct {
	// Client is optional; when nil a per-send client honouring the timeout is used.
	Client *http.Client
}

// NewHTTPTransport returns the default transport.
func NewHTTPTransport() *HTTPTransport {
	return &HTTPTransport{}
}

// Send POSTs the envelope to {endpoint}/v1/ingest. All errors are swallowed so
// that capturing can never break the monitored app.
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
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ingest-Key", key)

	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	resp, err := client.Do(req)
	if err != nil {
		return
	}
	// Drain and close so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
