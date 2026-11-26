package bliptrace

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fakeTransport records every envelope it is asked to send. No network.
type fakeTransport struct {
	sent []sentRecord
}

type sentRecord struct {
	endpoint string
	key      string
	envelope Envelope
	timeout  time.Duration
}

func (f *fakeTransport) Send(endpoint, key string, envelope Envelope, timeout time.Duration) {
	f.sent = append(f.sent, sentRecord{endpoint, key, envelope, timeout})
}

// myError is a named error type used to exercise type naming + ignore matching.
type myError struct{ msg string }

func (e *myError) Error() string { return e.msg }

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestBuildsValidEnvelope(t *testing.T) {
	ft := &fakeTransport{}
	c := NewWithTransport(Config{Key: "k", Endpoint: "http://api"}, ft)

	c.Capture(errors.New("boom"), nil)

	if len(ft.sent) != 1 {
		t.Fatalf("expected 1 sent envelope, got %d", len(ft.sent))
	}
	env := ft.sent[0].envelope

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"schema_version", env.SchemaVersion, "1.0"},
		{"runtime.name", env.Runtime.Name, "go"},
		{"sdk.name", env.SDK.Name, defaultSDKName},
		{"sdk.version", env.SDK.Version, defaultSDKVersion},
		{"level", env.Level, LevelError},
		{"exception.type", env.Exception.Type, "*errors.errorString"},
		{"exception.message", env.Exception.Message, "boom"},
		{"environment", env.Environment, defaultEnviron},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}

	if !uuidShape.MatchString(env.EventID) {
		t.Errorf("event_id %q is not a uuid v4 shape", env.EventID)
	}
	if env.Runtime.Version == "" {
		t.Error("runtime.version is empty")
	}
	if _, err := time.Parse(time.RFC3339, env.OccurredAt); err != nil {
		t.Errorf("occurred_at %q not RFC3339: %v", env.OccurredAt, err)
	}
	if !strings.Contains(env.OccurredAt, ".") {
		t.Errorf("occurred_at %q lacks millisecond precision", env.OccurredAt)
	}

	if len(env.Exception.Frames) == 0 {
		t.Fatal("expected at least one frame")
	}
	for i, fr := range env.Exception.Frames {
		if fr.File == "" {
			t.Errorf("frame %d has empty file", i)
		}
		if fr.Line <= 0 {
			t.Errorf("frame %d has non-positive line %d", i, fr.Line)
		}
		if fr.Function == "" {
			t.Errorf("frame %d has empty function", i)
		}
		// No SDK-internal capture-path frames should leak into the trace.
		if isSDKFrame(fr.Function) {
			t.Errorf("frame %d leaks SDK function %q", i, fr.Function)
		}
	}
	// The top frame must be this test, marked in-app.
	if !env.Exception.Frames[0].InApp {
		t.Errorf("top frame %q expected in_app=true", env.Exception.Frames[0].Function)
	}
}

func TestEventIDsAreUnique(t *testing.T) {
	ft := &fakeTransport{}
	c := NewWithTransport(Config{Key: "k"}, ft)
	c.Capture(errors.New("a"), nil)
	c.Capture(errors.New("b"), nil)
	if len(ft.sent) != 2 {
		t.Fatalf("expected 2, got %d", len(ft.sent))
	}
	if ft.sent[0].envelope.EventID == ft.sent[1].envelope.EventID {
		t.Error("event_ids should be unique per capture")
	}
}

func TestIgnoresConfiguredErrorTypes(t *testing.T) {
	tests := []struct {
		name         string
		ignore       []string
		err          error
		wantSendable bool
	}{
		{"ignore by full type", []string{"*errors.errorString"}, errors.New("noise"), false},
		{"ignore named type", []string{"*bliptrace.myError"}, &myError{"noise"}, false},
		{"non-matching ignore sends", []string{"*bliptrace.myError"}, errors.New("real"), true},
		{"empty ignore sends", nil, errors.New("real"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := &fakeTransport{}
			c := NewWithTransport(Config{Key: "k", IgnoreErrors: tt.ignore}, ft)
			c.Capture(tt.err, nil)
			got := len(ft.sent) == 1
			if got != tt.wantSendable {
				t.Errorf("sendable = %v, want %v (sent=%d)", got, tt.wantSendable, len(ft.sent))
			}
		})
	}
}

func TestDoesNothingWithoutKeyOrDisabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"no key", Config{Key: ""}},
		{"disabled", Config{Key: "k", Disabled: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := &fakeTransport{}
			c := NewWithTransport(tt.cfg, ft)
			c.Capture(errors.New("x"), nil)
			if len(ft.sent) != 0 {
				t.Errorf("expected nothing sent, got %d", len(ft.sent))
			}
		})
	}
}

func TestEnabledDefaultsTrue(t *testing.T) {
	ft := &fakeTransport{}
	// Zero Config except a key — Enabled left at the bool zero value (false)
	// must still behave as enabled.
	c := NewWithTransport(Config{}, ft)
	if !c.cfg.Enabled {
		t.Error("expected Enabled to default to true")
	}
}

func TestScrubsSensitiveContext(t *testing.T) {
	ft := &fakeTransport{}
	c := NewWithTransport(Config{Key: "k", ScrubKeys: []string{"password", "TOKEN"}}, ft)

	c.Capture(errors.New("x"), map[string]interface{}{
		"user": map[string]interface{}{
			"password": "hunter2",
			"id":       "7",
			"Token":    "abc",
		},
		"items": []interface{}{
			map[string]interface{}{"password": "nested"},
		},
	})

	if len(ft.sent) != 1 {
		t.Fatalf("expected 1 sent, got %d", len(ft.sent))
	}
	ctx := ft.sent[0].envelope.Context
	user := ctx["user"].(map[string]interface{})

	tests := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"password redacted", user["password"], Redacted},
		{"token redacted (case-insensitive)", user["Token"], Redacted},
		{"non-sensitive untouched", user["id"], "7"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
		}
	}

	items := ctx["items"].([]interface{})
	nested := items[0].(map[string]interface{})
	if nested["password"] != Redacted {
		t.Errorf("nested-in-slice password = %v, want %v", nested["password"], Redacted)
	}
}

func TestNeverPanics(t *testing.T) {
	tests := []struct {
		name string
		err  error
		ctx  map[string]interface{}
	}{
		{"nil error", nil, nil},
		{"weird context value", errors.New("x"), map[string]interface{}{"fn": func() {}}},
		{"channel in context", errors.New("x"), map[string]interface{}{"ch": make(chan int)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("capture panicked: %v", r)
				}
			}()
			ft := &fakeTransport{}
			c := NewWithTransport(Config{Key: "k"}, ft)
			c.Capture(tt.err, tt.ctx)
		})
	}
}

func TestSendEnvelopeNoKeyNoOp(t *testing.T) {
	ft := &fakeTransport{}
	c := NewWithTransport(Config{Key: ""}, ft)
	c.SendEnvelope(Envelope{SchemaVersion: "1.0"})
	if len(ft.sent) != 0 {
		t.Errorf("expected no send without key, got %d", len(ft.sent))
	}
}

func TestNeverPutsProjectInPayload(t *testing.T) {
	ft := &fakeTransport{}
	c := NewWithTransport(Config{Key: "k"}, ft)
	c.Capture(errors.New("x"), map[string]interface{}{"a": 1})
	// The Envelope struct has no Project field; assert via fmt that the rendered
	// keys never include "project".
	rendered := fmt.Sprintf("%+v", ft.sent[0].envelope)
	if strings.Contains(strings.ToLower(rendered), "project:") {
		t.Errorf("envelope must never contain a project field: %s", rendered)
	}
}

func TestTransportReceivesEndpointAndKey(t *testing.T) {
	ft := &fakeTransport{}
	c := NewWithTransport(Config{Key: "secret", Endpoint: "http://example"}, ft)
	c.Capture(errors.New("x"), nil)
	if ft.sent[0].key != "secret" {
		t.Errorf("key = %q, want secret", ft.sent[0].key)
	}
	if ft.sent[0].endpoint != "http://example" {
		t.Errorf("endpoint = %q, want http://example", ft.sent[0].endpoint)
	}
	if ft.sent[0].timeout != defaultTimeout {
		t.Errorf("timeout = %v, want %v", ft.sent[0].timeout, defaultTimeout)
	}
}
