// Package saga implements the orchestration-style saga/compensation coordinator
// for nzr-rules-engine flows (HLD Risk R3).
//
// Standards applied:
//   - [[saga-pattern]]: orchestration style, reverse-order compensation, state machine,
//     log saga id + step + outcome on every transition, timeout on every step.
//   - [[idempotency-and-dedup]]: saga steps and compensations must be idempotent
//     (contract enforced by callers; each step carries an idempotency key if needed).
//   - [[observability-and-logging]]: every state transition is emitted via Logger.
//   - [[simplicity-and-design]]: in-memory, request-scoped for v1 synchronous flows;
//     no persistent saga store (deferred per design doc).
//
// v1 scope: The Context lives within a single HTTP request (all flow steps are
// synchronous). Parallel branches may call Record concurrently; a mutex makes
// Context safe for those concurrent writes.
package saga

import (
	"context"
	"fmt"
	"sync"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/observ"
)

// State is the saga lifecycle position.
type State string

const (
	// StateStarted: saga created, no steps executed yet.
	StateStarted State = "started"
	// StateExecuting: forward steps in progress.
	StateExecuting State = "executing"
	// StateCommitted: all forward steps completed successfully.
	StateCommitted State = "committed"
	// StateCompensating: a step failed; running compensations in reverse.
	StateCompensating State = "compensating"
	// StateCompensated: all compensations completed successfully.
	StateCompensated State = "compensated"
	// StateFailed: a compensation itself failed — requires manual intervention.
	StateFailed State = "failed"
)

// CompensationSpec defines the rollback operation for one saga step.
type CompensationSpec struct {
	// Connection is the connection key for the compensation op. If empty, the
	// caller should default it to the forward step's connection.
	Connection string
	// Operation is the undo I/O (e.g. DELETE, refund call).
	Operation connect.Operation
}

// Step is one recorded write operation with its optional compensation.
type Step struct {
	NodeID       string            // action node that produced this step
	OpKind       string            // "exec", "set", "del", "http", …
	Connection   string            // connection key used for the forward op
	Compensation *CompensationSpec // nil = no automatic compensation for this step
	Completed    bool              // true after the forward step finished successfully
}

// Context tracks saga state for a single flow execution. A sync.Mutex makes it
// safe for concurrent calls from parallel branches (they all call Record on the
// shared request context while running simultaneously).
//
// Compensate must only be called after ip.walk() returns (all goroutines joined),
// so Compensate and Record are never concurrent.
type Context struct {
	id  string
	log observ.Logger

	mu    sync.Mutex
	state State
	steps []Step // ordered by execution time; compensations iterate in reverse
}

// New creates a saga Context in StateStarted. log may be nil (logging is a no-op).
func New(id string, log observ.Logger) *Context {
	return &Context{id: id, state: StateStarted, log: log}
}

// ID returns the saga identifier used for log/span correlation.
func (c *Context) ID() string { return c.id }

// State returns the current saga state.
func (c *Context) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Steps returns a snapshot of the recorded steps (safe to read after the walk).
func (c *Context) Steps() []Step {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Step, len(c.steps))
	copy(out, c.steps)
	return out
}

// Record registers a completed write step. It MUST be called only after a
// successful forward write — never on error paths. comp may be nil when the step
// has no automatic compensation (idempotent or read-only op).
//
// State transition: Started → Executing on the first call.
func (c *Context) Record(nodeID, opKind, connection string, comp *CompensationSpec) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == StateStarted {
		c.state = StateExecuting
	}
	c.steps = append(c.steps, Step{
		NodeID:       nodeID,
		OpKind:       opKind,
		Connection:   connection,
		Compensation: comp,
		Completed:    true,
	})
	c.emitLocked(nil, "saga.step_recorded", nodeID)
}

// Commit marks the saga as committed (all forward steps done, no compensation
// needed). Called by the interpreter after a successful walk.
func (c *Context) Commit() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == StateExecuting || c.state == StateStarted {
		c.state = StateCommitted
		c.emitLocked(nil, "saga.committed", "")
	}
}

// Compensate runs compensations in reverse order of completed forward steps.
// Returns nil if all compensations succeed or there is nothing to compensate.
// Returns the first compensation error and transitions to StateFailed on failure.
// It is a no-op when state != StateExecuting.
//
// conns must not be nil when there are steps with compensations.
func (c *Context) Compensate(ctx context.Context, conns connect.Registry) error {
	c.mu.Lock()
	if c.state != StateExecuting {
		c.mu.Unlock()
		return nil
	}
	c.state = StateCompensating
	// Snapshot steps so we don't hold the lock during I/O.
	steps := make([]Step, len(c.steps))
	copy(steps, c.steps)
	c.emitLocked(nil, "saga.compensating", "")
	c.mu.Unlock()

	// Reverse order: the last completed step compensates first
	// (per [[saga-pattern]] § Compensation).
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if !step.Completed || step.Compensation == nil {
			continue // no compensation defined for this step; skip
		}

		client, err := conns.Client(ctx, step.Compensation.Connection)
		if err != nil {
			c.transitionFailed()
			c.emit(err, "saga.compensation.conn_failed", step.NodeID)
			return fmt.Errorf("saga compensation: get client for %q (step %q): %w",
				step.Compensation.Connection, step.NodeID, err)
		}

		_, err = client.Execute(ctx, step.Compensation.Operation)
		if err != nil {
			c.transitionFailed()
			c.emit(err, "saga.compensation.exec_failed", step.NodeID)
			return fmt.Errorf("saga compensation: execute for step %q: %w", step.NodeID, err)
		}

		c.emit(nil, "saga.compensation.ok", step.NodeID)
	}

	c.mu.Lock()
	c.state = StateCompensated
	c.emitLocked(nil, "saga.compensated", "")
	c.mu.Unlock()
	return nil
}

// transitionFailed transitions to StateFailed (called without the lock held).
func (c *Context) transitionFailed() {
	c.mu.Lock()
	c.state = StateFailed
	c.mu.Unlock()
}

// emit logs a structured transition (called without the lock held).
func (c *Context) emit(err error, label, nodeID string) {
	if c.log == nil {
		return
	}
	c.mu.Lock()
	state := c.state
	c.mu.Unlock()
	c.logEvent(context.Background(), err, label, nodeID, state)
}

// emitLocked logs a structured transition while the lock is already held.
// Must NOT acquire c.mu internally.
func (c *Context) emitLocked(err error, label, nodeID string) {
	if c.log == nil {
		return
	}
	c.logEvent(context.Background(), err, label, nodeID, c.state)
}

// logEvent performs the actual structured log emit.
func (c *Context) logEvent(ctx context.Context, err error, label, nodeID string, state State) {
	fields := map[string]any{
		"saga_id": c.id,
		"state":   string(state),
	}
	if nodeID != "" {
		fields["node_id"] = nodeID
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	level := "info"
	if err != nil {
		level = "error"
	}
	c.log.Emit(ctx, level, label, fields)
}
