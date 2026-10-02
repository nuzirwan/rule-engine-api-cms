package connect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// defSignature is a stable content hash over the fields that decide whether a
// Reload must rebuild a key's client: Type, Settings, SecretRef, and the
// Resilience default. An unrelated edit elsewhere never churns a healthy pool
// (slice-b-connections.md §1.3). The SecretRef is a ref, never a value, so it is
// safe to hash. A rotation of the secret VALUE does not change the ref and so
// does not change this signature — rotation is handled off the Reload path.
func defSignature(def ConnectionDef) string {
	payload := struct {
		Type       string
		Settings   map[string]any
		SecretRef  string
		Resilience ResiliencePolicy
	}{
		Type:       def.Type,
		Settings:   def.Settings,
		SecretRef:  def.SecretRef,
		Resilience: def.Resilience,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		// A non-marshalable Settings is itself a change signal; fall back to a
		// value that differs from any successful hash so the key rebuilds.
		return "unhashable:" + def.Key
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
