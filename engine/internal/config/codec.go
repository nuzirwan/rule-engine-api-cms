package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/flow"
)

// This file owns the encode/decode seam shared by the Postgres store and the
// Valkey cache. Two rules drive it:
//
//  1. Defensive decode (AC-15, §8): decoding a stored or cached config value is
//     wrapped so a malformed tree/jdm yields a classified Validation error, never
//     a panic and never a 500. A bad config row is rejected on load.
//  2. Cache wire shape is versioned by the key's `v1` segment (keys.go). The
//     encoded bytes here are the body stored under those keys.

// cachedFlow is the Valkey wire shape for a resolved flow version. It is a plain
// JSON encoding of the FlowVersion fields the resolver needs; the key's version
// segment (keys.go) guards against decoding an old shape under a new reader.
type cachedFlow struct {
	FlowID   string        `json:"flowId"`
	Version  int           `json:"version"`
	Method   string        `json:"method"`
	Path     string        `json:"path"`
	Tree     flow.Node     `json:"tree"`
	Fixtures []FlowFixture `json:"fixtures,omitempty"`
}

// cachedJDM is the Valkey wire shape for a JDM: its raw bytes plus version.
type cachedJDM struct {
	JDM     []byte `json:"jdm"`
	Version int    `json:"version"`
}

// encodeFlow renders a FlowVersion for the cache. Encoding is on the write path
// (store already holds a valid value) so a failure here is Internal, not a bad
// config row.
func encodeFlow(fv FlowVersion) ([]byte, error) {
	b, err := json.Marshal(cachedFlow{
		FlowID:   fv.FlowID,
		Version:  fv.Version,
		Method:   fv.Method,
		Path:     fv.Path,
		Tree:     fv.Tree,
		Fixtures: fv.Fixtures,
	})
	if err != nil {
		return nil, wrapErr(Internal, "encode flow version for cache", err)
	}
	return b, nil
}

// decodeFlow parses cache/store bytes into a FlowVersion. A decode failure is a
// Validation error (reject on load, AC-15) carrying no stack-unwinding panic.
func decodeFlow(b []byte) (FlowVersion, error) {
	var cf cachedFlow
	if err := json.Unmarshal(b, &cf); err != nil {
		return FlowVersion{}, wrapErr(Validation, "decode flow version (bad config)", err)
	}
	return FlowVersion{
		FlowID:   cf.FlowID,
		Version:  cf.Version,
		Method:   cf.Method,
		Path:     cf.Path,
		Tree:     cf.Tree,
		Fixtures: cf.Fixtures,
	}, nil
}

// decodeTree defensively parses a stored jsonb tree into a flow.Node. Used on the
// Postgres read path; a malformed tree is Validation, never a 500 (AC-15).
func decodeTree(raw []byte) (flow.Node, error) {
	var n flow.Node
	if err := json.Unmarshal(raw, &n); err != nil {
		return flow.Node{}, wrapErr(Validation, "decode flow tree (bad config)", err)
	}
	if n.Type == "" {
		return flow.Node{}, newErr(Validation, "flow tree root has no type (bad config)")
	}
	return n, nil
}

// encodeJDM renders a JDM for the cache.
func encodeJDM(jdm []byte, version int) ([]byte, error) {
	b, err := json.Marshal(cachedJDM{JDM: jdm, Version: version})
	if err != nil {
		return nil, wrapErr(Internal, "encode jdm for cache", err)
	}
	return b, nil
}

// decodeJDM parses cached JDM bytes. A decode failure is Validation (AC-15).
func decodeJDM(b []byte) ([]byte, int, error) {
	var cj cachedJDM
	if err := json.Unmarshal(b, &cj); err != nil {
		return nil, 0, wrapErr(Validation, "decode jdm (bad config)", err)
	}
	return cj.JDM, cj.Version, nil
}

// encodeConns renders the whole-env connection list for the cache.
func encodeConns(defs []connect.ConnectionDef) ([]byte, error) {
	b, err := json.Marshal(defs)
	if err != nil {
		return nil, wrapErr(Internal, "encode connections for cache", err)
	}
	return b, nil
}

// decodeConns parses the cached connection list. A decode failure is Validation.
func decodeConns(b []byte) ([]connect.ConnectionDef, error) {
	var defs []connect.ConnectionDef
	if err := json.Unmarshal(b, &defs); err != nil {
		return nil, wrapErr(Validation, "decode connections (bad config)", err)
	}
	return defs, nil
}

// checksum returns the sha256 hex of raw. It anchors the contract test for an
// immutable version (the stored checksum must match the stored body — a Strapi
// change that drifts the shape is caught here, R11 / §3).
func checksum(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
