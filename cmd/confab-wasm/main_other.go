//go:build !(js && wasm)

// Non-WASM stub so `go build ./...` and `go vet ./...` succeed natively.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "confab-wasm is the browser core; build it with:")
	fmt.Fprintln(os.Stderr, "  GOOS=js GOARCH=wasm go build -o web/dist/confab.wasm ./cmd/confab-wasm")
	os.Exit(1)
}
