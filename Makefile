# nzr-rules-engine — build/test targets.
#
# CGO is REQUIRED: the decision engine links the embedded ZEN native library
# (github.com/gorules/zen-go/v2, -lzen_ffi), so every target sets CGO_ENABLED=1
# and a C toolchain (gcc/cc) must be on PATH. The container image must be
# glibc-based (debian-slim/distroless), NOT Alpine/musl (ADR-003,
# docs/phase0-spike-report.md).

export CGO_ENABLED := 1

.PHONY: build test test-integration vet

# build compiles the whole tree, including cmd/engine, with CGO on.
build:
	CGO_ENABLED=1 go build ./...

# test runs the unit tests (fakes only; no Docker, no network).
test:
	CGO_ENABLED=1 go test ./...

# test-integration runs the real-HTTP end-to-end test (Docker REQUIRED): an
# ephemeral postgres:16 + an in-process httptest REST stub + the real ZEN engine.
test-integration:
	CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi

# vet runs go vet over the whole tree.
vet:
	CGO_ENABLED=1 go vet ./...
