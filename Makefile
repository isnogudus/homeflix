# Evaluated by the shell in each recipe, so it works with GNU make and BSD make.
LDFLAGS = -s -w -X main.version=$$(git describe --always --dirty 2>/dev/null || echo dev)

.PHONY: all build build-freebsd test vet check deploy-dev clean

all: check build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o build/homeflix ./cmd/homeflix

build-freebsd:
	GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o build/homeflix-freebsd-amd64 ./cmd/homeflix

test:
	go test -race ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt needed"; exit 1; }

check: vet test

deploy-dev:
	scripts/deploy-dev.sh

clean:
	rm -rf build
