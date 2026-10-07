# LLD — Slice H: Webhook Receiver (external event ingestion)

Package: `internal/webhook` · Module: `nzr-rules-engine` · Go 1.22+
Status: **implemented** · Last updated: 2026-10-07

The webhook package provides external event ingestion from third-party services, verifying
signatures, filtering events, and mapping payloads before triggering flows.

---

## 1. Package responsibilities

`internal/webhook` is the home for:

- **Provider abstraction** — pluggable webhook providers (GitHub, Stripe, generic).
- **Signature verification** — per-provider cryptographic signature validation.
- **Event filtering** — `MatchesFilter` / `MatchesFilterFromMap` to select relevant events.
- **Payload mapping** — `MapPayload` transforms incoming payloads to flow input format.

### Explicit non-responsibilities

- Flow execution → Slice A (`internal/flow`)
- HTTP routing → Slice E (`internal/httpapi`)
- Config storage → Slice D (`internal/config`)

---

## 2. Provider interface

```go
// Provider handles webhook ingestion for a specific source (GitHub, Stripe, etc.).
type Provider interface {
    // Name returns the provider identifier (e.g., "github", "stripe", "generic").
    Name() string
    
    // VerifySignature validates the webhook signature.
    // Returns error if signature is invalid or missing.
    VerifySignature(ctx context.Context, req *http.Request, secret []byte) error
    
    // ParsePayload extracts and parses the webhook payload.
    ParsePayload(ctx context.Context, req *http.Request) (map[string]any, error)
    
    // EventType extracts the event type from the request (header or payload).
    EventType(req *http.Request) string
}
```

---

## 3. Built-in providers

### 3.1 GitHub (`github.go`)

```go
type githubProvider struct{}

// VerifySignature validates X-Hub-Signature-256 using HMAC-SHA256.
func (p *githubProvider) VerifySignature(ctx context.Context, req *http.Request, secret []byte) error

// EventType returns X-GitHub-Event header value.
func (p *githubProvider) EventType(req *http.Request) string
```

Supported events: `push`, `pull_request`, `issues`, `release`, etc.

### 3.2 Stripe (`stripe.go`)

```go
type stripeProvider struct{}

// VerifySignature validates Stripe-Signature header using their timestamp + HMAC scheme.
func (p *stripeProvider) VerifySignature(ctx context.Context, req *http.Request, secret []byte) error

// EventType extracts type from parsed payload.
func (p *stripeProvider) EventType(req *http.Request) string
```

### 3.3 Generic (`generic.go`)

```go
type genericProvider struct {
    signatureHeader string
    signatureScheme string // "hmac-sha256", "hmac-sha1", "none"
}

// VerifySignature uses configurable header and scheme.
func (p *genericProvider) VerifySignature(ctx context.Context, req *http.Request, secret []byte) error
```

---

## 4. Event filtering

```go
// MatchesFilter checks if an event matches the configured filter criteria.
// Filter supports:
// - Event type matching: {"event": "push"} or {"event": ["push", "release"]}
// - Path matching: {"repository.full_name": "org/repo"}
// - Wildcard: {"branch": "refs/heads/*"}
func MatchesFilter(event map[string]any, filter map[string]any) bool

// MatchesFilterFromMap is a convenience wrapper when filter is already parsed.
func MatchesFilterFromMap(event, filter map[string]any) bool
```

---

## 5. Payload mapping

```go
// MapPayload transforms the raw webhook payload into flow input format.
// Mapping spec example:
// {
//   "commit_sha": "$.head_commit.id",
//   "repo": "$.repository.full_name",
//   "branch": "$.ref"
// }
func MapPayload(payload map[string]any, mapping map[string]string) (map[string]any, error)
```

---

## 6. Webhook configuration

```go
// WebhookConfig defines a webhook endpoint configuration.
type WebhookConfig struct {
    ID        string            `json:"id"`
    Provider  string            `json:"provider"`  // "github", "stripe", "generic"
    SecretRef string            `json:"secretRef"` // reference to signing secret
    Filter    map[string]any    `json:"filter"`    // event filter criteria
    Mapping   map[string]string `json:"mapping"`   // payload field mapping
    FlowID    string            `json:"flowId"`    // flow to trigger
    Enabled   bool              `json:"enabled"`
}
```

---

## 7. File structure

```
internal/webhook/
  provider.go     // Provider interface
  github.go       // GitHub provider
  stripe.go       // Stripe provider
  generic.go      // Generic/configurable provider
  filter.go       // MatchesFilter, MatchesFilterFromMap
  mapping.go      // MapPayload
```

---

## 8. Acceptance criteria

| AC | Description |
|----|-------------|
| AC-34 | Webhook receiver verifies signatures and triggers flows |
| | - Invalid signature returns 401 |
| | - Filtered-out events return 200 (acknowledged, not processed) |
| | - Matched events trigger the configured flow |
| | - Payload mapping transforms input before flow execution |

---

## 9. Verification (2026-10-07)

```bash
ls engine/internal/webhook/
# Output: filter.go generic.go github.go mapping.go provider.go stripe.go

grep -n "type.*Provider" engine/internal/webhook/provider.go
# Confirms: Provider interface with VerifySignature

grep -n "MatchesFilter\|MapPayload" engine/internal/webhook/*.go
# Confirms: filter.go and mapping.go implementations exist
```
