package webhook

import "errors"

// Sentinel errors for webhook processing.
var (
	// ErrBadSignature indicates the webhook signature verification failed.
	ErrBadSignature = errors.New("invalid webhook signature")
	// ErrMissingSignature indicates the signature header is missing.
	ErrMissingSignature = errors.New("missing signature header")
	// ErrInvalidPayload indicates the webhook payload could not be parsed.
	ErrInvalidPayload = errors.New("invalid webhook payload")
	// ErrWebhookNotFound indicates the webhook_id does not exist.
	ErrWebhookNotFound = errors.New("webhook not found")
	// ErrFiltered indicates the event was filtered out (not an error, just no-op).
	ErrFiltered = errors.New("event filtered out")
)
