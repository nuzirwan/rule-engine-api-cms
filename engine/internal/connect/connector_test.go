package connect

import (
	"context"
	"testing"
)

// TestWithSecretsSecretsFromRoundTrip proves the WithSecrets/SecretsFrom context
// helpers round-trip correctly: a map placed on context can be retrieved.
func TestWithSecretsSecretsFromRoundTrip(t *testing.T) {
	ctx := context.Background()

	// Initially, no secrets on context
	_, ok := SecretsFrom(ctx)
	if ok {
		t.Fatal("SecretsFrom returned ok=true on empty context; want false")
	}

	// Place secrets on context
	secrets := map[string]Secret{
		"password": NewPlainSecret("pw123"),
		"apiKey":   NewPlainSecret("key456"),
	}
	ctx = WithSecrets(ctx, secrets)

	// Retrieve secrets
	got, ok := SecretsFrom(ctx)
	if !ok {
		t.Fatal("SecretsFrom returned ok=false after WithSecrets; want true")
	}
	if len(got) != 2 {
		t.Fatalf("got %d secrets; want 2", len(got))
	}
	if string(got["password"].Reveal()) != "pw123" {
		t.Errorf("password = %s; want pw123", got["password"].Reveal())
	}
	if string(got["apiKey"].Reveal()) != "key456" {
		t.Errorf("apiKey = %s; want key456", got["apiKey"].Reveal())
	}
}

// TestWithSecretSecretFromRoundTrip proves the existing WithSecret/SecretFrom
// helpers still work correctly after adding multi-secret support.
func TestWithSecretSecretFromRoundTrip(t *testing.T) {
	ctx := context.Background()

	// Initially, no secret on context
	_, ok := SecretFrom(ctx)
	if ok {
		t.Fatal("SecretFrom returned ok=true on empty context; want false")
	}

	// Place single secret on context
	sec := NewPlainSecret("single-secret")
	ctx = WithSecret(ctx, sec)

	// Retrieve secret
	got, ok := SecretFrom(ctx)
	if !ok {
		t.Fatal("SecretFrom returned ok=false after WithSecret; want true")
	}
	if string(got.Reveal()) != "single-secret" {
		t.Errorf("secret = %s; want single-secret", got.Reveal())
	}
}

// TestBothSecretContextsCoexist proves that WithSecret and WithSecrets can both
// be on the same context without interfering with each other.
func TestBothSecretContextsCoexist(t *testing.T) {
	ctx := context.Background()

	// Place both single and multi secrets
	singleSec := NewPlainSecret("single")
	multiSec := map[string]Secret{
		"password": NewPlainSecret("multi-pw"),
		"apiKey":   NewPlainSecret("multi-key"),
	}

	ctx = WithSecret(ctx, singleSec)
	ctx = WithSecrets(ctx, multiSec)

	// Both should be retrievable
	gotSingle, ok := SecretFrom(ctx)
	if !ok {
		t.Fatal("SecretFrom returned ok=false; want true")
	}
	if string(gotSingle.Reveal()) != "single" {
		t.Errorf("single secret = %s; want single", gotSingle.Reveal())
	}

	gotMulti, ok := SecretsFrom(ctx)
	if !ok {
		t.Fatal("SecretsFrom returned ok=false; want true")
	}
	if len(gotMulti) != 2 {
		t.Fatalf("got %d secrets; want 2", len(gotMulti))
	}
	if string(gotMulti["password"].Reveal()) != "multi-pw" {
		t.Errorf("password = %s; want multi-pw", gotMulti["password"].Reveal())
	}
}

// secretCapturingConnector captures the secrets from context when Open is called,
// allowing tests to verify what secrets were passed to a connector.
type secretCapturingConnector struct {
	typ            string
	capturedSingle Secret
	capturedMulti  map[string]Secret
	singleOK       bool
	multiOK        bool
}

func newSecretCapturingConnector(typ string) *secretCapturingConnector {
	return &secretCapturingConnector{typ: typ}
}

func (c *secretCapturingConnector) Type() string             { return c.typ }
func (c *secretCapturingConnector) Lifecycle() Lifecycle     { return LifecyclePooled }
func (c *secretCapturingConnector) Capabilities() Capability { return CapQueryExec }

func (c *secretCapturingConnector) Open(ctx context.Context, def ConnectionDef) (Client, error) {
	c.capturedSingle, c.singleOK = SecretFrom(ctx)
	c.capturedMulti, c.multiOK = SecretsFrom(ctx)
	return &fakeClient{key: def.Key}, nil
}

// TestRegistryResolvesMultipleSecrets proves the registry resolves SecretRefs
// and places them on context via WithSecrets.
func TestRegistryResolvesMultipleSecrets(t *testing.T) {
	// Set env vars for secrets
	t.Setenv("TEST_PASSWORD", "secret-pw")
	t.Setenv("TEST_API_KEY", "secret-key")

	conn := newSecretCapturingConnector("capturing")
	def := ConnectionDef{
		Key:  "multi",
		Type: "capturing",
		SecretRefs: map[string]string{
			"password": "env:TEST_PASSWORD",
			"apiKey":   "env:TEST_API_KEY",
		},
	}

	_, err := newEagerRegistry([]Connector{conn}, []ConnectionDef{def}, NewEnvSecretProvider(), nil, nil)
	if err != nil {
		t.Fatalf("newEagerRegistry: %v", err)
	}

	// Verify multi-secrets were passed
	if !conn.multiOK {
		t.Fatal("SecretsFrom in connector returned ok=false; want true")
	}
	if len(conn.capturedMulti) != 2 {
		t.Fatalf("connector received %d secrets; want 2", len(conn.capturedMulti))
	}
	if string(conn.capturedMulti["password"].Reveal()) != "secret-pw" {
		t.Errorf("password = %s; want secret-pw", conn.capturedMulti["password"].Reveal())
	}
	if string(conn.capturedMulti["apiKey"].Reveal()) != "secret-key" {
		t.Errorf("apiKey = %s; want secret-key", conn.capturedMulti["apiKey"].Reveal())
	}

	// Verify backward compat: password key also set via WithSecret
	if !conn.singleOK {
		t.Fatal("SecretFrom in connector returned ok=false; want true (backward compat)")
	}
	if string(conn.capturedSingle.Reveal()) != "secret-pw" {
		t.Errorf("single secret = %s; want secret-pw (password key)", conn.capturedSingle.Reveal())
	}
}

// TestRegistryLegacySecretRefFallback proves that when SecretRefs is empty but
// SecretRef is set, the registry falls back to the legacy single-secret behavior
// and also sets the multi-secret map with "password" key.
func TestRegistryLegacySecretRefFallback(t *testing.T) {
	t.Setenv("TEST_LEGACY_SECRET", "legacy-value")

	conn := newSecretCapturingConnector("capturing")
	def := ConnectionDef{
		Key:       "legacy",
		Type:      "capturing",
		SecretRef: "env:TEST_LEGACY_SECRET",
		// SecretRefs is empty/nil
	}

	_, err := newEagerRegistry([]Connector{conn}, []ConnectionDef{def}, NewEnvSecretProvider(), nil, nil)
	if err != nil {
		t.Fatalf("newEagerRegistry: %v", err)
	}

	// Verify single secret was passed (legacy behavior)
	if !conn.singleOK {
		t.Fatal("SecretFrom in connector returned ok=false; want true")
	}
	if string(conn.capturedSingle.Reveal()) != "legacy-value" {
		t.Errorf("single secret = %s; want legacy-value", conn.capturedSingle.Reveal())
	}

	// Verify multi-secrets map also set with "password" key (forward compat)
	if !conn.multiOK {
		t.Fatal("SecretsFrom in connector returned ok=false; want true")
	}
	if len(conn.capturedMulti) != 1 {
		t.Fatalf("connector received %d secrets; want 1", len(conn.capturedMulti))
	}
	if string(conn.capturedMulti["password"].Reveal()) != "legacy-value" {
		t.Errorf("password = %s; want legacy-value", conn.capturedMulti["password"].Reveal())
	}
}

// TestRegistrySecretRefsPrefersOverSecretRef proves that when both SecretRefs
// and SecretRef are set, SecretRefs takes precedence.
func TestRegistrySecretRefsPrefersOverSecretRef(t *testing.T) {
	t.Setenv("TEST_NEW_PASSWORD", "new-pw")
	t.Setenv("TEST_OLD_PASSWORD", "old-pw")

	conn := newSecretCapturingConnector("capturing")
	def := ConnectionDef{
		Key:       "both",
		Type:      "capturing",
		SecretRef: "env:TEST_OLD_PASSWORD", // legacy field
		SecretRefs: map[string]string{ // new field takes precedence
			"password": "env:TEST_NEW_PASSWORD",
		},
	}

	_, err := newEagerRegistry([]Connector{conn}, []ConnectionDef{def}, NewEnvSecretProvider(), nil, nil)
	if err != nil {
		t.Fatalf("newEagerRegistry: %v", err)
	}

	// Verify SecretRefs wins
	if string(conn.capturedMulti["password"].Reveal()) != "new-pw" {
		t.Errorf("password = %s; want new-pw (SecretRefs should win)", conn.capturedMulti["password"].Reveal())
	}
	if string(conn.capturedSingle.Reveal()) != "new-pw" {
		t.Errorf("single secret = %s; want new-pw (from SecretRefs.password)", conn.capturedSingle.Reveal())
	}
}

// TestRegistryNoSecretsOK proves a connection with no secrets still works.
func TestRegistryNoSecretsOK(t *testing.T) {
	conn := newSecretCapturingConnector("capturing")
	def := ConnectionDef{
		Key:  "nosecret",
		Type: "capturing",
		// No SecretRef, no SecretRefs
	}

	_, err := newEagerRegistry([]Connector{conn}, []ConnectionDef{def}, NewEnvSecretProvider(), nil, nil)
	if err != nil {
		t.Fatalf("newEagerRegistry: %v", err)
	}

	// Both should be empty/not-ok
	if conn.singleOK {
		t.Error("SecretFrom returned ok=true; want false for no-secret connection")
	}
	if conn.multiOK {
		t.Error("SecretsFrom returned ok=true; want false for no-secret connection")
	}
}

// TestRegistryMultiSecretResolutionFailure proves that if one secret ref fails
// to resolve, the whole connection fails.
func TestRegistryMultiSecretResolutionFailure(t *testing.T) {
	t.Setenv("TEST_VALID", "valid")
	// TEST_INVALID is NOT set

	conn := newSecretCapturingConnector("capturing")
	def := ConnectionDef{
		Key:  "partial",
		Type: "capturing",
		SecretRefs: map[string]string{
			"valid":   "env:TEST_VALID",
			"invalid": "env:TEST_INVALID", // not set
		},
	}

	_, err := newEagerRegistry([]Connector{conn}, []ConnectionDef{def}, NewEnvSecretProvider(), nil, nil)
	if err == nil {
		t.Fatal("expected error when one secret ref fails to resolve")
	}
}
