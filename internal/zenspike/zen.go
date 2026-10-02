//go:build cgo

// Package zenspike is a THROWAWAY Phase-0 spike wrapper over gorules zen-go v2.
// It proves AC-S1 (CGO build+run) and AC-S2 (ZEN evaluates the real condition).
// It mirrors the shape of Slice C's decision.Compiled seam closely enough to
// surface where the real zen-go v2 API diverges from Slice C's assumptions.
package zenspike

import (
	"context"
	"encoding/json"
	"fmt"

	zen "github.com/gorules/zen-go/v2"
)

// Engine wraps a zen-go Engine. One Engine per process is enough for the spike.
type Engine struct {
	eng zen.Engine
}

// NewEngine constructs a ZEN engine with no loader (we compile JDMs directly
// from bytes via CreateDecision — no key-based loading in the spike).
//
// Note (Slice C divergence): zen.NewEngine returns no error; an init failure is
// deferred inside the engine value and surfaces on the first Evaluate/CreateDecision.
func NewEngine() *Engine {
	return &Engine{eng: zen.NewEngine(zen.EngineConfig{})}
}

// Compile parses JDM bytes into a reusable, compiled decision handle.
// This is the compile-once / evaluate-many step Slice C caches by (id,version).
func (e *Engine) Compile(jdm []byte) (*Compiled, error) {
	dec, err := e.eng.CreateDecision(jdm)
	if err != nil {
		return nil, fmt.Errorf("zenspike: compile decision: %w", err)
	}
	return &Compiled{dec: dec}, nil
}

// Dispose frees the engine's Rust-side resources.
func (e *Engine) Dispose() { e.eng.Dispose() }

// Compiled is the spike analogue of Slice C's decision.Compiled handle.
//
// Slice C assumed: Eval(ctx, input) (map[string]any, error) + Close().
// Real zen-go v2 is: Decision.Evaluate(input any) (*EvaluationResponse, error) + Dispose().
// This wrapper adapts the real API to Slice C's intended seam:
//   - it accepts a context.Context and honors it AROUND the call (ZEN eval is a
//     synchronous in-process CPU call that cannot be aborted mid-eval);
//   - it unmarshals EvaluationResponse.Result (json.RawMessage) into map[string]any;
//   - Close() maps to the real Dispose().
type Compiled struct {
	dec zen.Decision
}

// Eval evaluates the compiled decision over input and returns the JDM output
// object as a map. ctx is honored around the call only (see type doc).
func (c *Compiled) Eval(ctx context.Context, input map[string]any) (map[string]any, error) {
	// Honor ctx BEFORE the call — zen-go cannot cancel mid-eval (Slice C Timeout note).
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resp, err := c.dec.Evaluate(input)
	if err != nil {
		return nil, fmt.Errorf("zenspike: evaluate: %w", err)
	}

	// Re-check after the (fast, synchronous) call so a cancelled/expired ctx is
	// reported rather than returning a result the caller no longer wants.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var out map[string]any
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, fmt.Errorf("zenspike: unmarshal result: %w", err)
	}
	return out, nil
}

// Close frees the compiled decision's Rust-side graph (Slice C Close() -> Dispose()).
func (c *Compiled) Close() { c.dec.Dispose() }
