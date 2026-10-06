package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// stripeProvider implements Provider for Stripe webhooks.
// Stripe signs webhooks with HMAC-SHA256 using the format:
// Stripe-Signature: t=timestamp,v1=signature[,v0=signature]
type stripeProvider struct{}

// Name returns "stripe".
func (p *stripeProvider) Name() string { return ProviderStripe }

// VerifySignature verifies a Stripe webhook signature.
// Stripe format: t=<timestamp>,v1=<signature>
// The signed payload is: <timestamp>.<raw_body>
func (p *stripeProvider) VerifySignature(params SignatureParams) error {
	if params.Header == "" {
		return errors.New("missing Stripe-Signature header")
	}
	if len(params.Secret) == 0 {
		return errors.New("empty webhook secret")
	}

	// Parse the Stripe-Signature header.
	parts := strings.Split(params.Header, ",")
	var timestamp string
	var signatures []string

	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key, val := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		switch key {
		case "t":
			timestamp = val
		case "v1":
			signatures = append(signatures, val)
		}
	}

	if timestamp == "" {
		return errors.New("missing timestamp in Stripe-Signature")
	}
	if len(signatures) == 0 {
		return errors.New("missing v1 signature in Stripe-Signature")
	}

	// Validate timestamp is not too old (5 minute tolerance like Stripe SDK).
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid timestamp: %w", err)
	}
	age := time.Since(time.Unix(ts, 0))
	if age > 5*time.Minute || age < -5*time.Minute {
		return fmt.Errorf("timestamp outside tolerance: %v", age)
	}

	// Compute expected signature: HMAC-SHA256(secret, timestamp.payload).
	signedPayload := timestamp + "." + string(params.Payload)
	mac := hmac.New(sha256.New, params.Secret)
	mac.Write([]byte(signedPayload))
	expected := hex.EncodeToString(mac.Sum(nil))

	// Check if any of the v1 signatures match (Stripe may send multiple for key rotation).
	for _, sig := range signatures {
		if hmac.Equal([]byte(expected), []byte(sig)) {
			return nil
		}
	}
	return errors.New("signature verification failed")
}

// ParseEvent parses a Stripe webhook payload. Stripe sends an event envelope with:
// {
//   "id": "evt_...",
//   "type": "payment_intent.succeeded",
//   "data": { "object": { ... } }
// }
func (p *stripeProvider) ParseEvent(payload []byte, headers map[string]string) (WebhookEvent, error) {
	var event struct {
		ID   string         `json:"id"`
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return WebhookEvent{}, fmt.Errorf("parse Stripe event: %w", err)
	}

	// Parse the full payload for JSONPath mapping.
	var full map[string]any
	if err := json.Unmarshal(payload, &full); err != nil {
		return WebhookEvent{}, fmt.Errorf("parse Stripe payload: %w", err)
	}

	return WebhookEvent{
		Type:    event.Type,
		Payload: full,
		Headers: headers,
		RawBody: payload,
	}, nil
}
