//go:build cgo

// This file is the ONLY place internal/decision imports the zen-go binding. It
// mirrors the proven spike wrapper internal/zenspike/zen.go EXACTLY: the engine
// is zen.NewEngine(zen.EngineConfig{}) (no error, deferred init), compile is
// eng.CreateDecision(jdm), evaluate is dec.Evaluate(input) with NO ctx arg, and
// the context is honored only AROUND the call because zen-go cannot cancel a
// synchronous in-eval CPU call. resp.Result (json.RawMessage) is unmarshalled to
// map[string]any at this seam; dispose maps Compiled.Close -> dec.Dispose and
// Engine.Close -> eng.Dispose (via the backend).
package decision

import (
	"context"
	"encoding/json"
	"fmt"

	zen "github.com/gorules/zen-go/v2"
)

// New builds a CGO-backed decision Engine over the given JDM loader. The zen
// engine is created with no loader (we compile JDM bytes directly via
// CreateDecision; the (id,version) cache in engine.go owns reuse).
func New(loader JDMLoader, opts ...Option) *Engine {
	be := &zenBackend{eng: zen.NewEngine(zen.EngineConfig{})}
	return newEngine(loader, be, opts...)
}

// zenBackend adapts a zen.Engine to the decision.backend seam.
type zenBackend struct {
	eng zen.Engine
}

// compile parses JDM bytes into a reusable compiled decision handle.
func (b *zenBackend) compile(jdm []byte) (Compiled, error) {
	dec, err := b.eng.CreateDecision(jdm)
	if err != nil {
		return nil, fmt.Errorf("compile decision: %w", err)
	}
	return &zenCompiled{dec: dec}, nil
}

// close frees the engine's Rust-side resources on shutdown.
func (b *zenBackend) close() { b.eng.Dispose() }

// zenCompiled adapts a zen.Decision to the Compiled seam. Eval honors ctx around
// the call only (zen-go cannot cancel mid-eval); Close maps to Dispose.
type zenCompiled struct {
	dec zen.Decision
}

// Eval evaluates the compiled decision over input and returns the JDM output as
// a map. ctx is checked before and after the (fast, synchronous) call.
func (c *zenCompiled) Eval(ctx context.Context, input map[string]any) (map[string]any, error) {
	// Honor ctx BEFORE the call — zen-go cannot cancel mid-eval.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resp, err := c.dec.Evaluate(input)
	if err != nil {
		return nil, fmt.Errorf("evaluate: %w", err)
	}

	// Re-check AFTER so a cancelled/expired ctx is reported rather than returning
	// a result the caller no longer wants.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var out map[string]any
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}
	return out, nil
}

// Close frees the compiled decision's Rust-side graph.
func (c *zenCompiled) Close() { c.dec.Dispose() }
