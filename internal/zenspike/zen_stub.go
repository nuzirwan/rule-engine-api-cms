//go:build !cgo

// This stub mirrors Slice C §2: a non-CGO build still COMPILES (useful for a
// pure-Go lint/vet lane), but every constructor returns a clear error so the
// engine never silently no-ops when ZEN is unavailable.
package zenspike

import (
	"context"
	"errors"
)

// ErrNoCGO is returned by every entrypoint when built without CGO.
var ErrNoCGO = errors.New("zenspike: built without CGO; ZEN unavailable (CGO_ENABLED=1 required)")

// Engine is the non-CGO stub; it carries no real engine.
type Engine struct{}

// NewEngine returns a stub engine. It does not fail here so the symbol shape
// matches the cgo build; failures surface at Compile.
func NewEngine() *Engine { return &Engine{} }

// Compile always fails on the stub lane.
func (e *Engine) Compile(jdm []byte) (*Compiled, error) { return nil, ErrNoCGO }

// Dispose is a no-op on the stub lane.
func (e *Engine) Dispose() {}

// Compiled is the non-CGO stub handle.
type Compiled struct{}

// Eval always fails on the stub lane.
func (c *Compiled) Eval(ctx context.Context, input map[string]any) (map[string]any, error) {
	return nil, ErrNoCGO
}

// Close is a no-op on the stub lane.
func (c *Compiled) Close() {}
