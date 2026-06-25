// Example: capture a catalogue of Go errors with the g8scope SDK.
//
//	G8SCOPE_KEY=... G8SCOPE_ENDPOINT=http://localhost:8088 go run ./examples
package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	g8 "github.com/developers-hub-my/g8scope-go"
)

func main() {
	client := g8.New(g8.Config{
		Key:         os.Getenv("G8SCOPE_KEY"),
		Endpoint:    env("G8SCOPE_ENDPOINT", "http://localhost:8088"),
		Environment: "demo",
		Release:     "demo-go@1.0",
	})

	ctx := map[string]interface{}{"demo": true}

	client.Capture(errors.New("demo: generic error"), ctx)
	fmt.Println("captured: errors.New")

	client.Capture(fmt.Errorf("demo: wrapped: %w", errors.New("root cause")), ctx)
	fmt.Println("captured: fmt.Errorf")

	if _, err := strconv.Atoi("not-a-number"); err != nil {
		client.Capture(err, ctx)
		fmt.Printf("captured: %T\n", err)
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				client.Capture(fmt.Errorf("panic: %v", r), ctx)
				fmt.Println("captured: panic")
			}
		}()
		var m map[string]int
		m["x"] = 1 // panic: assignment to nil map
	}()

	fmt.Println("done")
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
