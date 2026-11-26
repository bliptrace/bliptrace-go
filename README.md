# bliptrace-go

The runtime **Go** SDK for [Bliptrace](https://bliptrace.com) — a central,
multi-tenant error-capture and observability platform.

The SDK builds the language-neutral Bliptrace **error envelope** from a Go `error`,
scrubs PII from your context, and POSTs it to the ingest API. It is **stdlib
only** (no external dependencies) and the capture path is fully guarded — it will
**never panic into your application**.

## Installation

```sh
go get github.com/bliptrace/bliptrace-go
```

## Usage

```go
package main

import (
	"errors"
	"time"

	"github.com/bliptrace/bliptrace-go"
)

func main() {
	client := bliptrace.New(bliptrace.Config{
		Key:         "your-ingest-key",          // resolves the project server-side
		Environment: "production",
		Release:     "my-app@2026.06.25",
		BasePath:    "/srv/app",                  // marks your code as in-app
		ScrubKeys:   []string{"password", "token", "authorization"},
		Timeout:     3 * time.Second,
	})

	if err := doWork(); err != nil {
		client.Capture(err, map[string]interface{}{
			"user":   map[string]interface{}{"id": "u_123"},
			"custom": map[string]interface{}{"feature": "reset-password"},
		})
	}
}

func doWork() error { return errors.New("boom") }
```

### Deferring the send

Use `BuildEnvelope` / `SendEnvelope` when you want to build the envelope now and
ship it later (for example after the HTTP response has flushed):

```go
env, ok := client.BuildEnvelope(err, ctx)
if ok {
	// ... do other work ...
	client.SendEnvelope(env)
}
```

## Configuration

| Field          | Default                                | Notes                                                        |
| -------------- | -------------------------------------- | ------------------------------------------------------------ |
| `Key`          | `""`                                   | Ingest key. Capture is a no-op without it.                   |
| `Endpoint`     | `https://ingest.scope.g8suite.com`     | Posts to `{Endpoint}/v1/ingest`.                             |
| `Environment`  | `production`                           |                                                              |
| `Release`      | `""`                                   | Optional release marker.                                     |
| `Timeout`      | `3s`                                   | Per-request HTTP timeout.                                    |
| `MaxFrames`    | `30`                                   | Caps captured stack frames.                                  |
| `MaxMessage`   | `2000`                                 | Caps the exception message length.                           |
| `IgnoreErrors` | `nil`                                  | Error type names to skip, e.g. `*mypkg.NotFound`.            |
| `ScrubKeys`    | `nil`                                  | Context keys (case-insensitive) redacted to `[REDACTED]`.    |
| `BasePath`     | `""`                                   | When set, a frame is in-app iff its file starts with it.     |
| `Disabled`     | `false`                                | Capture is on by default; set `true` to opt out.            |
| `SDKName`      | `bliptrace-go`                           |                                                              |
| `SDKVersion`   | `1.0.0`                                |                                                              |

## How the project is resolved

The **project is never sent in the payload**. The ingest API resolves it
server-side from the `X-Ingest-Key` header, so a client cannot spoof another
tenant. This is by design — see the platform specification §10.

## Testing

```sh
go vet ./...
go test ./...
```

## Licence

MIT — see [LICENSE](LICENSE).
