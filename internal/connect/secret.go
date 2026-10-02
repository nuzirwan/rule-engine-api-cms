package connect

import (
	"context"
	"os"
	"strings"
)

// SecretProvider resolves a SecretRef to a Secret value. v1 ships an env-backed
// provider; the interface leaves room for Vault/SSM later with no registry
// change (dependency inversion, config-and-secrets standard). The resolved value
// is never persisted, logged, or placed in an error — only the ref/key is.
type SecretProvider interface {
	// Resolve returns the current secret value for a ref (e.g. "env:PG_ORDERS_PW").
	Resolve(ctx context.Context, ref string) (Secret, error)
	// Watch notifies on rotation so the registry can refresh without a version
	// bump. The env provider does not rotate; it returns a no-op stop func.
	Watch(ctx context.Context, ref string, onChange func()) (stop func(), err error)
}

// Secret is an opaque secret value. Its String and MarshalJSON redact to "***"
// so a Secret can never leak through logs, traces, or JSON. Only a driver's Open
// reads the raw bytes via Reveal.
type Secret struct {
	v []byte
}

// NewSecret wraps raw bytes in a redacting Secret.
func NewSecret(v []byte) Secret { return Secret{v: v} }

// String redacts the value so a Secret is safe to interpolate into a log line.
func (s Secret) String() string { return "***" }

// MarshalJSON redacts the value so a Secret serializes as "***".
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"***"`), nil }

// Reveal returns the raw secret bytes. Only a driver Open should call this.
func (s Secret) Reveal() []byte { return s.v }

// IsZero reports whether the secret carries no value.
func (s Secret) IsZero() bool { return len(s.v) == 0 }

// envProvider resolves "env:NAME" refs from the process environment. A bare ref
// with no scheme is treated as an env var name. It is the v1 SecretProvider.
type envProvider struct{}

// NewEnvSecretProvider returns the env-backed SecretProvider.
func NewEnvSecretProvider() SecretProvider { return envProvider{} }

// Resolve implements SecretProvider. An empty ref yields an empty Secret (a
// connection may legitimately have no secret). A declared "env:NAME" ref whose
// variable is unset is a Validation error (fail fast per config-and-secrets).
func (envProvider) Resolve(ctx context.Context, ref string) (Secret, error) {
	if ref == "" {
		return Secret{}, nil
	}
	name := ref
	if strings.HasPrefix(ref, "env:") {
		name = strings.TrimPrefix(ref, "env:")
	} else if i := strings.IndexByte(ref, ':'); i >= 0 {
		// A non-env scheme is not supported by the v1 provider.
		return Secret{}, newErr(Validation, "", "", "unsupported secret scheme in ref")
	}
	if name == "" {
		return Secret{}, newErr(Validation, "", "", "empty env secret name")
	}
	val, ok := os.LookupEnv(name)
	if !ok {
		return Secret{}, newErr(Validation, "", "", "env secret not set")
	}
	return NewSecret([]byte(val)), nil
}

// Watch implements SecretProvider. The env provider does not observe rotation,
// so it returns a no-op stop func and never calls onChange.
func (envProvider) Watch(ctx context.Context, ref string, onChange func()) (func(), error) {
	return func() {}, nil
}
