package flow

// isWriteOp reports whether a connect operation kind mutates / has a side effect
// and must therefore be suppressed under dry-run. Reads ("query","get","ping")
// still execute; everything else ("exec","set","del","http") is a write.
//
// The dry-run flag itself lives on the context via the observ seam
// (observ.WithDryRun / observ.IsDryRun); actionHandler.Exec consults it and, for
// a write op, skips the I/O and records wrote:"suppressed" (AC-14). This is the
// single source of the write-vs-read classification.
func isWriteOp(kind string) bool {
	switch kind {
	case "query", "get", "ping":
		return false
	default:
		return true
	}
}
