// Package drivers holds the concrete connect.Connector implementations, one per
// source type. Adding a new type is a new file here plus one entry in All —
// the registry, interpreter, and flow schema are unchanged (AC-8, ADR-007).
package drivers

import "nzr-rules-engine/internal/connect"

// All returns the compile-time set of connectors the engine ships. cmd/engine
// passes it to connect.New. The rest connector registers under both "rest" and
// "http" (returned as two entries) so a def of either type resolves to the same
// shared-transport driver.
func All() []connect.Connector {
	rest := newRESTConnector("rest")
	restAlias := newRESTConnector("http")
	return []connect.Connector{
		newPGConnector(),
		rest,
		restAlias,
	}
}
