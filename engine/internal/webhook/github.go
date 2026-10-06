package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// githubProvider implements Provider for GitHub webhooks.
// GitHub signs webhooks with HMAC-SHA256 using the format:
// X-Hub-Signature-256: sha256=<hex_signature>
// Event type is sent in X-GitHub-Event header.
type githubProvider struct{}

// Name returns "github".
func (p *githubProvider) Name() string { return ProviderGitHub }

// VerifySignature verifies a GitHub webhook signature.
// GitHub format: sha256=<hex_signature>
func (p *githubProvider) VerifySignature(params SignatureParams) error {
	if params.Header == "" {
		return errors.New("missing X-Hub-Signature-256 header")
	}
	if len(params.Secret) == 0 {
		return errors.New("empty webhook secret")
	}

	// Parse the signature format: sha256=<hex>
	if !strings.HasPrefix(params.Header, "sha256=") {
		return errors.New("invalid signature format: expected sha256= prefix")
	}
	signature := strings.TrimPrefix(params.Header, "sha256=")

	// Compute expected signature.
	mac := hmac.New(sha256.New, params.Secret)
	mac.Write(params.Payload)
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return errors.New("signature verification failed")
	}
	return nil
}

// ParseEvent parses a GitHub webhook payload. Event type comes from headers.
func (p *githubProvider) ParseEvent(payload []byte, headers map[string]string) (WebhookEvent, error) {
	// Event type is in X-GitHub-Event header.
	eventType := headers["X-GitHub-Event"]
	if eventType == "" {
		eventType = headers["x-github-event"] // case-insensitive fallback
	}

	// Parse payload as generic JSON.
	var full map[string]any
	if err := json.Unmarshal(payload, &full); err != nil {
		return WebhookEvent{}, fmt.Errorf("parse GitHub payload: %w", err)
	}

	return WebhookEvent{
		Type:    eventType,
		Payload: full,
		Headers: headers,
		RawBody: payload,
	}, nil
}
