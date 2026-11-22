package g8scope

import (
	"crypto/rand"
	"fmt"
	"runtime"
	"strings"
	"time"
)

// Envelope is the language-neutral g8scope wire contract (schema v1.0).
// Note: there is intentionally no Project field — the server resolves the
// project from the ingest key.
type Envelope struct {
	SchemaVersion string                 `json:"schema_version"`
	EventID       string                 `json:"event_id"`
	OccurredAt    string                 `json:"occurred_at"`
	Runtime       Runtime                `json:"runtime"`
	SDK           *SDK                   `json:"sdk,omitempty"`
	Environment   string                 `json:"environment,omitempty"`
	Release       string                 `json:"release,omitempty"`
	Level         string                 `json:"level"`
	Exception     Exception              `json:"exception"`
	Context       map[string]interface{} `json:"context,omitempty"`
}

// Runtime drives the per-runtime fingerprint normaliser server-side.
type Runtime struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// SDK identifies the SDK that produced the envelope.
type SDK struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Exception describes the captured error and its stack.
type Exception struct {
	Type    string  `json:"type"`
	Message string  `json:"message"`
	Frames  []Frame `json:"frames"`
}

// Frame is a single stack frame.
type Frame struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function"`
	InApp    bool   `json:"in_app"`
}

// Valid log levels.
const (
	LevelDebug   = "debug"
	LevelInfo    = "info"
	LevelWarning = "warning"
	LevelError   = "error"
	LevelFatal   = "fatal"
)

// BuildEnvelope assembles the envelope from an error. The supplied context is
// embedded as-is (the caller is expected to have scrubbed it already).
func BuildEnvelope(err error, cfg Config, ctx map[string]interface{}) Envelope {
	env := Envelope{
		SchemaVersion: "1.0",
		EventID:       uuid4(),
		OccurredAt:    time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Runtime:       Runtime{Name: "go", Version: runtime.Version()},
		SDK:           &SDK{Name: cfg.SDKName, Version: cfg.SDKVersion},
		Environment:   cfg.Environment,
		Release:       cfg.Release,
		Level:         LevelError,
		Exception: Exception{
			Type:    errorType(err),
			Message: truncate(errorMessage(err), cfg.MaxMessage),
			Frames:  captureFrames(cfg),
		},
	}
	if len(ctx) > 0 {
		env.Context = ctx
	}
	return env
}

// errorType returns a stable type name for the error, e.g. "*errors.errorString".
func errorType(err error) string {
	if err == nil {
		return "error"
	}
	return fmt.Sprintf("%T", err)
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// captureFrames walks the call stack at capture time, skipping the SDK's own
// frames, and maps each into a Frame.
func captureFrames(cfg Config) []Frame {
	// Skip runtime.Callers + this function; the rest is best-effort filtered
	// by package below.
	pcs := make([]uintptr, cfg.MaxFrames+16)
	n := runtime.Callers(2, pcs)
	if n == 0 {
		return []Frame{}
	}
	callers := runtime.CallersFrames(pcs[:n])

	frames := make([]Frame, 0, cfg.MaxFrames)
	for {
		frame, more := callers.Next()
		// Drop frames originating from this SDK package itself.
		if !isSDKFrame(frame.Function) {
			frames = append(frames, Frame{
				File:     frame.File,
				Line:     frame.Line,
				Function: frame.Function,
				InApp:    inApp(frame.File, cfg.BasePath),
			})
			if len(frames) >= cfg.MaxFrames {
				break
			}
		}
		if !more {
			break
		}
	}
	return frames
}

const sdkPkgPath = "github.com/bliptrace/bliptrace-go"

// sdkCapturePath lists the SDK's own functions on the capture call path. We skip
// these so the host app's frames sit at the top of the trace. We match by
// specific names (not the package prefix) so that user code living in the same
// package — e.g. the SDK's own tests — is not mistakenly filtered out.
var sdkCapturePath = map[string]struct{}{
	sdkPkgPath + ".(*Client).Capture":       {},
	sdkPkgPath + ".(*Client).BuildEnvelope": {},
	sdkPkgPath + ".BuildEnvelope":           {},
	sdkPkgPath + ".captureFrames":           {},
}

func isSDKFrame(function string) bool {
	_, ok := sdkCapturePath[function]
	return ok
}

// inApp decides whether a frame belongs to the monitored application.
// With BasePath set, a frame is in-app iff its file is under BasePath.
// Otherwise we treat everything not in the module cache / GOROOT as in-app.
func inApp(file, basePath string) bool {
	if file == "" {
		return false
	}
	if basePath != "" {
		return strings.HasPrefix(file, basePath)
	}
	if strings.Contains(file, "/go/pkg/mod/") || strings.Contains(file, "/pkg/mod/") {
		return false
	}
	if goroot := runtime.GOROOT(); goroot != "" && strings.HasPrefix(file, goroot) {
		return false
	}
	if strings.Contains(file, "/go/src/runtime/") || strings.Contains(file, "/libexec/src/") {
		return false
	}
	return true
}

func truncate(s string, max int) string {
	if max > 0 && len(s) > max {
		return s[:max]
	}
	return s
}

// uuid4 generates an RFC 4122 version-4 UUID using crypto/rand. No external dep.
func uuid4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand should not fail; fall back to a zeroed-but-shaped UUID.
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
