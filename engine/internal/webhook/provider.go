// Package webhook implements webhook signature verification and payload processing.
// It supports multiple webhook providers (Stripe, GitHub, Generic HMAC) and extracts
// event data into flow context via JSONPath mappings.
package webhook

// Provider handles signature verification and event parsing for a specific webhook
// source (Stripe, GitHub, generic HMAC). The receiver resolves the provider from
// the webhook config and delegates to it.
type Provider interface {
	// VerifySignature verifies the webhook payload against the signature header.
	// It returns nil on success or an error describing the verification failure.
	VerifySignature(params SignatureParams) error

	// ParseEvent parses the raw payload and headers into a structured event.
	// Stripe unwraps its envelope; GitHub extracts type from headers; generic passes through.
	ParseEvent(payload []byte, headers map[string]string) (WebhookEvent, error)

	// Name returns the provider identifier (stripe, github, generic).
	Name() string
}

// WebhookEvent is the parsed event from a webhook payload. Type is provider-specific
// (e.g. "payment.success" for Stripe, "push" for GitHub). Payload is the deserialized
// JSON body for JSONPath extraction.
type WebhookEvent struct {
	Type    string         // event type from provider ($.type for Stripe, X-GitHub-Event for GitHub)
	Payload map[string]any // deserialized JSON payload for mapping extraction
	Headers map[string]string
	RawBody []byte
}

// SignatureParams holds the inputs for signature verification. The receiver populates
// these from the request and webhook config before calling Provider.VerifySignature.
type SignatureParams struct {
	// Header is the signature header value (e.g. Stripe-Signature contents).
	Header string
	// Algorithm is the HMAC algorithm (sha256, sha1). Relevant for generic provider.
	Algorithm string
	// Secret is the raw secret bytes from SecretProvider.Resolve.
	Secret []byte
	// Payload is the raw request body (what was signed).
	Payload []byte
	// SignatureHeader is the name of the signature header (for generic provider config).
	SignatureHeader string
}

// Provider name constants match config.Provider* constants.
const (
	ProviderStripe  = "stripe"
	ProviderGitHub  = "github"
	ProviderGeneric = "generic"
)

// GetProvider returns the Provider implementation for the given name.
// Unknown providers return the generic provider as a fallback.
func GetProvider(name string) Provider {
	switch name {
	case ProviderStripe:
		return &stripeProvider{}
	case ProviderGitHub:
		return &githubProvider{}
	case ProviderGeneric:
		return &genericProvider{}
	default:
		return &genericProvider{}
	}
}
