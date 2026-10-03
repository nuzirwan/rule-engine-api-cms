// Package decision is the ZEN decision seam (Slice C, lld-contracts.md): the
// flow interpreter evaluates condition/decision nodes through the Evaluator
// interface, never by importing the zen-go binding directly.
//
// This file declares only the Evaluator seam that the pure flow core (Slice A)
// needs to compile and be unit-tested against a fake. The concrete Engine, the
// (id,version) compiled cache, the cgo/!cgo split and the JDMLoader port are
// added by Slice C (FEAT-002) in separate files in this same package — they must
// NOT redeclare the Evaluator interface defined here.
package decision

import "context"

// Evaluator runs a JDM (by id) over a snapshot of ctx input and returns the
// decision output as JSON decoded into a map. Env rides on ctx (per-env
// isolation, ADR-006); the signature is unchanged by it.
type Evaluator interface {
	Evaluate(ctx context.Context, jdmID string, input map[string]any) (map[string]any, error)
}
