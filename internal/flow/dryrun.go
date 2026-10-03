package flow

import "context"

// dryRunKey is the context key for the flow-local dry-run flag.
//
// CASE (b) SHIM (see docs/.agents/tasks/flow-nodes/plan.md): the cross-package
// seam observ.WithDryRun / observ.IsDryRun is NOT present on mainline yet (the
// observ sibling workflow has not landed it), and internal/observ is off-limits
// to this worktree. Inventing observ.IsDryRun here would fail the CGO build.
// So the dry-run suppression LOGIC lives here, behind a package-internal flag,
// so internal/flow builds green and AC-14 is testable now.
//
// TODO(observ-seam): once observ.IsDryRun lands on mainline, the stitch step
// swaps isDryRun below to delegate to observ.IsDryRun(ctx) (a one-line change,
// identical behavior) and this shim is deleted. Until then AC-14 stays
// needs-verification against the real seam rather than dismissed.
type dryRunKey struct{}

// withDryRun returns a context flagged as a dry run. It is the test/entry seam
// standing in for observ.WithDryRun until that seam lands on mainline.
func withDryRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, dryRunKey{}, true)
}

// isDryRun reports whether the context carries the flow-local dry-run flag. Once
// observ.IsDryRun exists this delegates to it instead (see TODO above).
func isDryRun(ctx context.Context) bool {
	v, _ := ctx.Value(dryRunKey{}).(bool)
	return v
}

// isWriteOp reports whether a connect operation kind mutates / has a side effect
// and must therefore be suppressed under dry-run. Reads ("query","get","ping")
// still execute; everything else ("exec","set","del","http") is a write.
func isWriteOp(kind string) bool {
	switch kind {
	case "query", "get", "ping":
		return false
	default:
		return true
	}
}
