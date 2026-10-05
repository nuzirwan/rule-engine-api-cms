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

.PHONY: build test test-integration vet run run-engine run-cms k8s-validate

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

# run starts both the engine and CMS concurrently. Ctrl+C stops both.
# Use run-engine or run-cms to start them individually.
run:
	@trap 'kill 0' INT TERM; \
	(cd $(ENGINE_DIR) && CGO_ENABLED=1 go run ./cmd/engine) & \
	(cd cms && npm run dev) & \
	wait

# run-engine starts the Go decision engine on :8080.
run-engine:
	cd $(ENGINE_DIR) && CGO_ENABLED=1 go run ./cmd/engine

# run-cms starts the Strapi CMS in development mode on :1337.
run-cms:
	cd cms && npm run dev

# k8s-validate validates all Kustomize manifests (base + dev + prod overlays).
# Requires kustomize CLI or kubectl with kustomize plugin.
# Install kustomize: https://kubectl.docs.kubernetes.io/installation/kustomize/
k8s-validate:
	./k8s/validate.sh
