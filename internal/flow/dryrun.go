package flow

// Dry-run write-suppression (AC-14). The cross-package seam has landed:
// observ.WithDryRun / observ.IsDryRun carry the dry-run flag on the context and
// observ.CollectorFrom carries the trace collector. The action handler
// (handlers.go) checks observ.IsDryRun(ctx) and, for a write op, skips the I/O,
// records wrote:"suppressed" onto the collector, and continues the walk. This
// file now owns only the write/read classification; the former package-local
// isDryRun/withDryRun shim has been removed in favor of the observ seam.

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
