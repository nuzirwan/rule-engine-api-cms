package config

import (
	"encoding/json"
	"errors"
	"testing"

	"nzr-rules-engine/internal/flow"
)

// TestDecodeTreeMalformed proves a malformed stored tree is a Validation error
// (reject on load), never a panic — the unit half of AC-15.
func TestDecodeTreeMalformed(t *testing.T) {
	cases := map[string][]byte{
		"not json":    []byte(`{not valid json`),
		"empty type":  []byte(`{"id":"x","type":""}`),
		"wrong shape": []byte(`"a string, not a node"`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeTree(raw)
			if err == nil {
				t.Fatalf("decodeTree(%s) = nil error; want Validation", name)
			}
			var ce *ConfigError
			if !errors.As(err, &ce) || ce.Class != Validation {
				t.Fatalf("decodeTree(%s) class = %v; want Validation", name, err)
			}
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("decodeTree(%s) does not match ErrValidation: %v", name, err)
			}
		})
	}
}

// TestDecodeTreeValid round-trips a good tree.
func TestDecodeTreeValid(t *testing.T) {
	src := flow.Node{ID: "trigger", Type: flow.TypeTrigger}
	raw, _ := json.Marshal(src)
	got, err := decodeTree(raw)
	if err != nil {
		t.Fatalf("decodeTree: %v", err)
	}
	if got.ID != "trigger" || got.Type != flow.TypeTrigger {
		t.Fatalf("decodeTree round-trip = %+v; want trigger", got)
	}
}

// TestFlowEncodeDecodeRoundTrip proves the cache wire shape survives a round-trip.
func TestFlowEncodeDecodeRoundTrip(t *testing.T) {
	fv := FlowVersion{
		FlowID:  "orders",
		Version: 3,
		Method:  "GET",
		Path:    "/orders/{id}",
		Tree:    flow.Node{ID: "trigger", Type: flow.TypeTrigger},
		Fixtures: []FlowFixture{
			{Name: "ok", Input: map[string]any{"id": float64(1)}},
		},
	}
	b, err := encodeFlow(fv)
	if err != nil {
		t.Fatalf("encodeFlow: %v", err)
	}
	got, err := decodeFlow(b)
	if err != nil {
		t.Fatalf("decodeFlow: %v", err)
	}
	if got.FlowID != fv.FlowID || got.Version != fv.Version || got.Path != fv.Path {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if len(got.Fixtures) != 1 || got.Fixtures[0].Name != "ok" {
		t.Fatalf("fixtures lost in round-trip: %+v", got.Fixtures)
	}
}

// TestDecodeFlowMalformed proves bad cached bytes are Validation, not a 500.
func TestDecodeFlowMalformed(t *testing.T) {
	_, err := decodeFlow([]byte(`{bad`))
	var ce *ConfigError
	if !errors.As(err, &ce) || ce.Class != Validation {
		t.Fatalf("decodeFlow(bad) = %v; want Validation", err)
	}
}

// TestChecksumStable proves the checksum anchors identical bytes to the same hash
// (the contract-test anchor for an immutable version).
func TestChecksumStable(t *testing.T) {
	a := checksum([]byte(`{"id":"x"}`))
	b := checksum([]byte(`{"id":"x"}`))
	c := checksum([]byte(`{"id":"y"}`))
	if a != b {
		t.Fatal("checksum not stable for identical bytes")
	}
	if a == c {
		t.Fatal("checksum collided for different bytes")
	}
}
