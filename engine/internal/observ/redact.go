package observ

import (
	"strings"
	"sync"
)

// redactMask replaces any value a Redactor scrubs. It is the only token that
// reaches a sink in place of a secret.
const redactMask = "***"

// Redactor scrubs secret material out of a field set before it reaches any sink
// (log, span, or dry-run collector). Centralizing redaction here means no caller
// can accidentally bypass it (AC-20): Logger.Emit, Span attribute setting and
// the dry-run collector all route through the same Redactor, so the automatic
// trace, the Logger node and dry-run output are covered alike.
type Redactor interface {
	// Scrub returns a copy of fields with secret values masked. The input map is
	// never mutated. A nil input yields a nil result.
	Scrub(fields map[string]any) map[string]any
	// ScrubString masks any registered secret value substring found in s (used
	// to scrub a rendered error string before it is logged).
	ScrubString(s string) string
}

// keyRedactor masks by two independent rules, per the slice-E design:
//   - deny-list by key convention: a field whose key matches a sensitive token
//     (secret, password, token, authorization, apikey, bearer, secretRef-resolved
//     values) is masked regardless of its value;
//   - value fingerprinting: a resolved secret value registered by the caller is
//     matched by value and masked even under an innocuous key.
//
// It is safe for concurrent use: registered secret values are guarded by a
// mutex, the deny-list keys are read-only after construction.
type keyRedactor struct {
	denyKeys []string // lowercased key substrings that force a mask

	mu      sync.RWMutex
	secrets map[string]struct{} // exact secret values to fingerprint-match
}

// defaultDenyKeys are the key substrings masked by convention. Comparison is
// case-insensitive and substring-based so "X-Api-Key", "authToken" and
// "user.password" all match.
var defaultDenyKeys = []string{
	"secret",
	"password",
	"passwd",
	"token",
	"authorization",
	"apikey",
	"api_key",
	"api-key",
	"bearer",
	"credential",
	"privatekey",
	"private_key",
}

// NewRedactor returns a Redactor using the default sensitive-key deny-list. Use
// RegisterSecret to additionally fingerprint resolved secret values by value.
func NewRedactor() Redactor {
	keys := make([]string, len(defaultDenyKeys))
	copy(keys, defaultDenyKeys)
	return &keyRedactor{denyKeys: keys, secrets: make(map[string]struct{})}
}

// RegisterSecret records a resolved secret value so it is masked by value even
// when it surfaces under a non-sensitive key. Empty values are ignored. The
// Registry (Slice B/D) registers values it resolves from a secretRef so they can
// never leak through a log/span/dry-run sink (AC-20). Safe for concurrent use.
func RegisterSecret(r Redactor, value string) {
	if value == "" {
		return
	}
	kr, ok := r.(*keyRedactor)
	if !ok {
		return
	}
	kr.mu.Lock()
	kr.secrets[value] = struct{}{}
	kr.mu.Unlock()
}

// Scrub implements Redactor.
func (r *keyRedactor) Scrub(fields map[string]any) map[string]any {
	if fields == nil {
		return nil
	}
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		if r.sensitiveKey(k) {
			out[k] = redactMask
			continue
		}
		out[k] = r.scrubValue(v)
	}
	return out
}

// ScrubString implements Redactor.
func (r *keyRedactor) ScrubString(s string) string {
	if s == "" {
		return s
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for secret := range r.secrets {
		if secret != "" && strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, redactMask)
		}
	}
	return s
}

// sensitiveKey reports whether key matches a deny-list token (case-insensitive
// substring).
func (r *keyRedactor) sensitiveKey(key string) bool {
	lk := strings.ToLower(key)
	for _, d := range r.denyKeys {
		if strings.Contains(lk, d) {
			return true
		}
	}
	return false
}

// scrubValue masks a value when it (or any nested string) exactly matches a
// registered secret, recursing into nested maps and slices so a secret buried in
// a structure is still caught.
func (r *keyRedactor) scrubValue(v any) any {
	switch t := v.(type) {
	case string:
		if r.isSecretValue(t) {
			return redactMask
		}
		return t
	case map[string]any:
		return r.Scrub(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = r.scrubValue(e)
		}
		return out
	default:
		return v
	}
}

// isSecretValue reports whether s was registered as a secret value.
func (r *keyRedactor) isSecretValue(s string) bool {
	if s == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.secrets[s]
	return ok
}

// nopRedactor performs no redaction; it is the fallback when a Logger/Tracer is
// constructed without one so the zero value never panics. It still returns a
// copy so callers can't mutate shared maps.
type nopRedactor struct{}

func (nopRedactor) Scrub(fields map[string]any) map[string]any {
	if fields == nil {
		return nil
	}
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		out[k] = v
	}
	return out
}

func (nopRedactor) ScrubString(s string) string { return s }
