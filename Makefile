# nzr-rules-engine — build/test targets.
#
# The Go engine lives under engine/ (its own go.mod); the Strapi CMS lives under
# cms/. These targets drive the engine; they cd into engine/ so they work from
# the repo root.
#
# CGO is REQUIRED: the decision engine links the embedded ZEN native library
# (github.com/gorules/zen-go/v2, -lzen_ffi), so every target sets CGO_ENABLED=1
# and a C toolchain (gcc/cc) must be on PATH. The container image must be
# glibc-based (debian-slim/distroless), NOT Alpine/musl (ADR-003,
# docs/phase0-spike-report.md).

export CGO_ENABLED := 1

ENGINE_DIR := engine

.PHONY: build test test-integration vet

# build compiles the whole engine tree, including cmd/engine, with CGO on.
build:
	cd $(ENGINE_DIR) && CGO_ENABLED=1 go build ./...

# test runs the unit tests (fakes only; no Docker, no network).
test:
	cd $(ENGINE_DIR) && CGO_ENABLED=1 go test ./...

# test-integration runs the real-HTTP end-to-end test (Docker REQUIRED): an
# ephemeral postgres:16 + an in-process httptest REST stub + the real ZEN engine.
test-integration:
	cd $(ENGINE_DIR) && CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi

# vet runs go vet over the whole engine tree.
vet:
	cd $(ENGINE_DIR) && CGO_ENABLED=1 go vet ./...
