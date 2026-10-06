package webhook

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"strings"
)

// genericProvider implements Provider for generic HMAC-signed webhooks.
// It supports configurable header name and algorithm (sha256/sha1).
// Default: X-Webhook-Signature header with sha256.
type genericProvider struct{}

// Name returns "generic".
func (p *genericProvider) Name() string { return ProviderGeneric }

// VerifySignature verifies a generic webhook signature.
// Supports formats:
// - Raw hex: <hex_signature>
// - Prefixed: sha256=<hex_signature> or sha1=<hex_signature>
func (p *genericProvider) VerifySignature(params SignatureParams) error {
	if params.Header == "" {
		return errors.New("missing signature header")
	}
	if len(params.Secret) == 0 {
		return errors.New("empty webhook secret")
	}

	// Determine algorithm and extract signature.
	algorithm := params.Algorithm
	signature := params.Header

	// Handle prefixed format (sha256=xxx or sha1=xxx).
	if strings.HasPrefix(params.Header, "sha256=") {
		algorithm = "sha256"
		signature = strings.TrimPrefix(params.Header, "sha256=")
	} else if strings.HasPrefix(params.Header, "sha1=") {
		algorithm = "sha1"
		signature = strings.TrimPrefix(params.Header, "sha1=")
	}

	// Default to sha256 if not specified.
	if algorithm == "" {
		algorithm = "sha256"
	}

	// Select hash function.
	var h func() hash.Hash
	switch algorithm {
	case "sha256":
		h = sha256.New
	case "sha1":
		h = sha1.New
	default:
		return fmt.Errorf("unsupported algorithm: %s", algorithm)
	}

	// Compute expected signature.
	mac := hmac.New(h, params.Secret)
	mac.Write(params.Payload)
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return errors.New("signature verification failed")
	}
	return nil
}

// ParseEvent parses a generic webhook payload. Type is extracted from $.type if present.
func (p *genericProvider) ParseEvent(payload []byte, headers map[string]string) (WebhookEvent, error) {
	var full map[string]any
	if err := json.Unmarshal(payload, &full); err != nil {
		return WebhookEvent{}, fmt.Errorf("parse payload: %w", err)
	}

	// Try to extract event type from $.type.
	eventType := ""
	if t, ok := full["type"].(string); ok {
		eventType = t
	}

	return WebhookEvent{
		Type:    eventType,
		Payload: full,
		Headers: headers,
		RawBody: payload,
	}, nil
}

// DefaultSignatureHeader is the default header name for generic webhooks.
const DefaultSignatureHeader = "X-Webhook-Signature"
