GOROOT := $(shell go env GOROOT)

.PHONY: web wasm serve test lint clean

web:
	mkdir -p web/dist
	cp web/src/* web/dist/
	cp "$(GOROOT)/lib/wasm/wasm_exec.js" web/dist/

wasm: web
	GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o web/dist/confab.wasm ./cmd/confab-wasm
	go run ./cmd/compress-assets web/dist

serve: wasm
	go run ./cmd/confab

test:
	go test -race ./...

lint:
	go vet ./...
	golangci-lint run

clean:
	rm -f confab
	find web/dist -type f ! -name .gitkeep -delete
