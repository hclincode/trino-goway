.PHONY: all build test test-integration vet lint install-lint proto starlark-test expr-test clean

# Pinned golangci-lint version. Update GOLANGCI_LINT_VERSION and re-run make install-lint
# when upgrading. Must match the version documented in docs/CONVENTIONS.md.
GOLANGCI_LINT_VERSION := v2.12.2

# Default target: build, vet, lint, test.
all: build vet lint test

# Build all packages.
build:
	go build ./...

# Run unit tests with race detector.
test:
	go test -race ./...

# Run integration tests with race detector (requires //go:build integration tag).
test-integration:
	go test -tags=integration -race -v ./internal/integration/...

# Run go vet.
vet:
	go vet ./...

# Install the pinned golangci-lint version via go install (uses the current Go toolchain).
# Run once after cloning or when GOLANGCI_LINT_VERSION changes.
install-lint:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# Run golangci-lint. Requires install-lint to have been run.
lint:
	golangci-lint run ./...

# Regenerate Go stubs from proto/router.proto.
proto:
	./proto/generate.sh

# Build starlark-test CLI tool into bin/.
starlark-test:
	go build -o bin/starlark-test ./tools/starlark-test

# Build expr-test CLI tool into bin/.
expr-test:
	go build -o bin/expr-test ./tools/expr-test

# Remove generated binaries.
clean:
	rm -rf bin/
