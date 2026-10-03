//go:build !cgo

// This stub mirrors internal/zenspike/zen_stub.go: a non-CGO build still
// COMPILES the whole decision package (useful for a pure-Go lint/vet lane and
// for other slices' unit tests that mock Evaluator), but every compile/evaluate
// returns a Validation-class error so the engine never silently no-ops when ZEN
// is unavailable. Constructors succeed in SHAPE; failure surfaces at compile.
package decision

import "context"

// New builds a decision Engine with a stub backend. It does not fail here so the
// symbol shape matches the cgo build; the failure surfaces on first evaluate.
func New(loader JDMLoader, opts ...Option) *Engine {
	return newEngine(loader, stubBackend{}, opts...)
}

// stubBackend satisfies the backend seam without CGO.
type stubBackend struct{}

// compile always fails on the stub lane with a Validation-class error so a
// caller gets a clear "built without CGO" message instead of a silent no-op.
func (stubBackend) compile(jdm []byte) (Compiled, error) {
	return nil, newErr(Validation, "", 0, "built without CGO; ZEN unavailable (CGO_ENABLED=1 required)")
}

// close is a no-op on the stub lane.
func (stubBackend) close() {}

// stubCompiled is never returned by compile (which always errors); it exists so
// the Compiled interface is satisfied in shape on this lane.
type stubCompiled struct{}

// Eval always fails on the stub lane.
func (stubCompiled) Eval(ctx context.Context, input map[string]any) (map[string]any, error) {
	return nil, newErr(Validation, "", 0, "built without CGO; ZEN unavailable (CGO_ENABLED=1 required)")
}

// Close is a no-op on the stub lane.
func (stubCompiled) Close() {}
