package observ

import (
	"strings"
	"testing"
)

// TestRedactorScrubsByKey proves the deny-list masks sensitive keys regardless
// of value, case-insensitively and as a substring.
func TestRedactorScrubsByKey(t *testing.T) {
	r := NewRedactor()
	cases := []struct {
		key    string
		masked bool
	}{
		{"password", true},
		{"Password", true},
		{"user.password", true},
		{"Authorization", true},
		{"X-Api-Key", true},
		{"apiKey", true},
		{"bearer_token", true},
		{"credential", true},
		{"private_key", true},
		{"sub", false},
		{"flow_id", false},
		{"environment", false},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			out := r.Scrub(map[string]any{c.key: "sensitive-value"})
			got := out[c.key]
			if c.masked && got != redactMask {
				t.Fatalf("key %q = %v; want masked", c.key, got)
			}
			if !c.masked && got != "sensitive-value" {
				t.Fatalf("key %q = %v; want unmasked", c.key, got)
			}
		})
	}
}

// TestRedactorScrubsByValue proves a registered secret value is masked even
// under an innocuous key, including nested in maps and slices.
func TestRedactorScrubsByValue(t *testing.T) {
	r := NewRedactor()
	const secret = "s3cr3t-token-abc"
	RegisterSecret(r, secret)

	fields := map[string]any{
		"innocuous": secret,
		"nested": map[string]any{
			"deep": secret,
		},
		"list":  []any{"ok", secret},
		"other": "fine",
	}
	out := r.Scrub(fields)

	if out["innocuous"] != redactMask {
		t.Fatalf("value under innocuous key not masked: %v", out["innocuous"])
	}
	if nm := out["nested"].(map[string]any); nm["deep"] != redactMask {
		t.Fatalf("nested secret not masked: %v", nm["deep"])
	}
	if lst := out["list"].([]any); lst[1] != redactMask || lst[0] != "ok" {
		t.Fatalf("secret in slice not masked: %v", lst)
	}
	if out["other"] != "fine" {
		t.Fatalf("non-secret value altered: %v", out["other"])
	}
}

// TestRedactorScrubString proves a registered secret substring is removed from a
// rendered string (used for error strings before they reach a sink).
func TestRedactorScrubString(t *testing.T) {
	r := NewRedactor()
	const secret = "p@ssw0rd"
	RegisterSecret(r, secret)
	got := r.ScrubString("connect failed for " + secret + " end")
	if strings.Contains(got, secret) {
		t.Fatalf("secret leaked in string: %q", got)
	}
	if !strings.Contains(got, redactMask) {
		t.Fatalf("mask not present: %q", got)
	}
}

// TestRedactorDoesNotMutateInput proves Scrub returns a copy.
func TestRedactorDoesNotMutateInput(t *testing.T) {
	r := NewRedactor()
	in := map[string]any{"password": "x"}
	_ = r.Scrub(in)
	if in["password"] != "x" {
		t.Fatalf("input mutated: %v", in["password"])
	}
}
