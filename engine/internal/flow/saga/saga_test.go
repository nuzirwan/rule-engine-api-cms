package saga_test

import (
	"context"
	"errors"
	"testing"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/flow/saga"
)

// ─── fakes ───────────────────────────────────────────────────────────────────

// fakeRegistry hands out fakeClients by key.
type fakeRegistry struct {
	clients map[string]*fakeClient
	err     error // non-nil: Client() returns this error
}

func (r *fakeRegistry) Client(_ context.Context, key string) (connect.Client, error) {
	if r.err != nil {
		return nil, r.err
	}
	c, ok := r.clients[key]
	if !ok {
		return nil, errors.New("unknown connection: " + key)
	}
	return c, nil
}
func (r *fakeRegistry) Reload(_ context.Context, _ []connect.ConnectionDef) error { return nil }
func (r *fakeRegistry) HealthCheck(_ context.Context) error                       { return nil }
func (r *fakeRegistry) Close() error                                              { return nil }
func (r *fakeRegistry) SecretProvider() connect.SecretProvider                    { return connect.NewEnvSecretProvider() }

// fakeClient records all Execute calls and optionally returns an error.
type fakeClient struct {
	ops []connect.Operation
	err error // non-nil: Execute() returns this error
}

func (c *fakeClient) Execute(_ context.Context, op connect.Operation) (any, error) {
	c.ops = append(c.ops, op)
	return nil, c.err
}
func (c *fakeClient) Close() error { return nil }

// ─── helpers ─────────────────────────────────────────────────────────────────

func compensationFor(conn string, kind string) *saga.CompensationSpec {
	return &saga.CompensationSpec{
		Connection: conn,
		Operation:  connect.Operation{Kind: kind},
	}
}

// ─── tests ───────────────────────────────────────────────────────────────────

func TestSagaContext_New_StartsInStarted(t *testing.T) {
	sc := saga.New("test-saga", nil)
	if sc.State() != saga.StateStarted {
		t.Errorf("expected StateStarted, got %v", sc.State())
	}
	if sc.ID() != "test-saga" {
		t.Errorf("expected ID \"test-saga\", got %q", sc.ID())
	}
	if len(sc.Steps()) != 0 {
		t.Errorf("expected 0 steps, got %d", len(sc.Steps()))
	}
}

func TestSagaContext_Record_TransitionsToExecuting(t *testing.T) {
	sc := saga.New("r1", nil)
	sc.Record("n1", "exec", "pg", nil)

	if sc.State() != saga.StateExecuting {
		t.Errorf("expected StateExecuting after first Record, got %v", sc.State())
	}
	steps := sc.Steps()
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
	s := steps[0]
	if s.NodeID != "n1" || s.OpKind != "exec" || s.Connection != "pg" || !s.Completed {
		t.Errorf("unexpected step: %+v", s)
	}
}

func TestSagaContext_Record_MultipleSteps(t *testing.T) {
	sc := saga.New("r2", nil)
	sc.Record("n1", "exec", "pg", nil)
	sc.Record("n2", "http", "api", compensationFor("api", "http"))
	sc.Record("n3", "set", "cache", nil)

	if len(sc.Steps()) != 3 {
		t.Errorf("expected 3 steps, got %d", len(sc.Steps()))
	}
}

func TestSagaContext_Compensate_ReverseOrder(t *testing.T) {
	// Two writes; compensation should execute them in reverse (n2 before n1).
	client1 := &fakeClient{}
	client2 := &fakeClient{}
	reg := &fakeRegistry{clients: map[string]*fakeClient{
		"pg":  client1,
		"api": client2,
	}}

	sc := saga.New("co1", nil)
	sc.Record("n1", "exec", "pg", compensationFor("pg", "exec"))
	sc.Record("n2", "http", "api", compensationFor("api", "http"))

	if err := sc.Compensate(context.Background(), reg); err != nil {
		t.Fatalf("unexpected compensation error: %v", err)
	}
	if sc.State() != saga.StateCompensated {
		t.Errorf("expected StateCompensated, got %v", sc.State())
	}
	// Reverse order: api (n2) compensates first, then pg (n1).
	if len(client2.ops) != 1 {
		t.Errorf("expected 1 op on api client, got %d", len(client2.ops))
	}
	if client2.ops[0].Kind != "http" {
		t.Errorf("expected http op on api client, got %q", client2.ops[0].Kind)
	}
	if len(client1.ops) != 1 {
		t.Errorf("expected 1 op on pg client, got %d", len(client1.ops))
	}
	if client1.ops[0].Kind != "exec" {
		t.Errorf("expected exec op on pg client, got %q", client1.ops[0].Kind)
	}
}

func TestSagaContext_Compensate_SkipsNilCompensation(t *testing.T) {
	client := &fakeClient{}
	reg := &fakeRegistry{clients: map[string]*fakeClient{"pg": client}}

	sc := saga.New("sk1", nil)
	sc.Record("n1", "exec", "pg", nil)                           // no compensation
	sc.Record("n2", "exec", "pg", compensationFor("pg", "exec")) // has compensation

	if err := sc.Compensate(context.Background(), reg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only n2 has a compensation; n1's nil comp is skipped.
	if len(client.ops) != 1 {
		t.Errorf("expected 1 compensation op (n2 only), got %d", len(client.ops))
	}
}

func TestSagaContext_Compensate_FailureSetsStateFailed(t *testing.T) {
	compErr := errors.New("refund rejected")
	client := &fakeClient{err: compErr}
	reg := &fakeRegistry{clients: map[string]*fakeClient{"pg": client}}

	sc := saga.New("fail1", nil)
	sc.Record("n1", "exec", "pg", compensationFor("pg", "exec"))

	err := sc.Compensate(context.Background(), reg)
	if err == nil {
		t.Fatal("expected compensation error, got nil")
	}
	if !errors.Is(err, compErr) {
		t.Errorf("expected wrapped compErr in returned error, got %v", err)
	}
	if sc.State() != saga.StateFailed {
		t.Errorf("expected StateFailed after compensation failure, got %v", sc.State())
	}
}

func TestSagaContext_Compensate_ConnFailSetsStateFailed(t *testing.T) {
	connErr := errors.New("connection unavailable")
	reg := &fakeRegistry{err: connErr}

	sc := saga.New("fail2", nil)
	sc.Record("n1", "exec", "pg", compensationFor("pg", "exec"))

	err := sc.Compensate(context.Background(), reg)
	if err == nil {
		t.Fatal("expected error on conn failure, got nil")
	}
	if sc.State() != saga.StateFailed {
		t.Errorf("expected StateFailed on conn failure, got %v", sc.State())
	}
}

func TestSagaContext_Compensate_NoSteps_IsNoop(t *testing.T) {
	sc := saga.New("noop1", nil)
	// StateStarted, no steps recorded.
	if err := sc.Compensate(context.Background(), &fakeRegistry{}); err != nil {
		t.Fatalf("unexpected error compensating empty saga: %v", err)
	}
	// State stays Started — compensation was a no-op.
	if sc.State() != saga.StateStarted {
		t.Errorf("expected StateStarted (unchanged), got %v", sc.State())
	}
}

func TestSagaContext_Compensate_AlreadyCommitted_IsNoop(t *testing.T) {
	client := &fakeClient{}
	reg := &fakeRegistry{clients: map[string]*fakeClient{"pg": client}}

	sc := saga.New("comm1", nil)
	sc.Record("n1", "exec", "pg", compensationFor("pg", "exec"))
	sc.Commit()

	if sc.State() != saga.StateCommitted {
		t.Fatalf("expected StateCommitted after Commit, got %v", sc.State())
	}

	if err := sc.Compensate(context.Background(), reg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// State is still Committed; no I/O was performed.
	if sc.State() != saga.StateCommitted {
		t.Errorf("expected StateCommitted still, got %v", sc.State())
	}
	if len(client.ops) != 0 {
		t.Errorf("expected no ops after commit, got %d", len(client.ops))
	}
}

func TestSagaContext_Compensate_CompensationStopsAtFirstFailure(t *testing.T) {
	// Three steps with compensations; the middle one fails.
	// Only the last step (first in reverse) should be attempted + fail.
	// Earlier steps should NOT be compensated (fail-fast).
	errMid := errors.New("middle failed")
	client1 := &fakeClient{}            // n1 compensation (not reached)
	client2 := &fakeClient{err: errMid} // n2 compensation (fails)
	client3 := &fakeClient{}            // n3 compensation (attempted first — reverse order)

	reg := &fakeRegistry{clients: map[string]*fakeClient{
		"c1": client1,
		"c2": client2,
		"c3": client3,
	}}

	sc := saga.New("stop1", nil)
	sc.Record("n1", "exec", "c1", compensationFor("c1", "exec"))
	sc.Record("n2", "exec", "c2", compensationFor("c2", "exec"))
	sc.Record("n3", "exec", "c3", compensationFor("c3", "exec"))

	err := sc.Compensate(context.Background(), reg)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// n3 compensated OK (first in reverse), n2 fails, n1 not attempted.
	if len(client3.ops) != 1 {
		t.Errorf("expected c3 compensation to run, got %d ops", len(client3.ops))
	}
	if len(client2.ops) != 1 {
		t.Errorf("expected c2 compensation attempt, got %d ops", len(client2.ops))
	}
	if len(client1.ops) != 0 {
		t.Errorf("expected c1 compensation NOT to run after failure, got %d ops", len(client1.ops))
	}
	if sc.State() != saga.StateFailed {
		t.Errorf("expected StateFailed, got %v", sc.State())
	}
}

func TestSagaContext_Commit_FromStarted(t *testing.T) {
	// A saga that completes without any writes should commit from Started.
	sc := saga.New("commit-no-steps", nil)
	sc.Commit()
	if sc.State() != saga.StateCommitted {
		t.Errorf("expected StateCommitted, got %v", sc.State())
	}
}

func TestSagaContext_Steps_ReturnsCopy(t *testing.T) {
	sc := saga.New("copy1", nil)
	sc.Record("n1", "exec", "pg", nil)

	s1 := sc.Steps()
	// Mutating the returned slice must not affect the internal state.
	s1[0].NodeID = "mutated"

	s2 := sc.Steps()
	if s2[0].NodeID != "n1" {
		t.Errorf("Steps() should return a copy; internal state was mutated")
	}
}
