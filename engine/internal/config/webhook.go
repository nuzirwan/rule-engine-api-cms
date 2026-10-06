package config

import "time"

// Webhook provider constants.
const (
	ProviderStripe  = "stripe"
	ProviderGitHub  = "github"
	ProviderGeneric = "generic"
)

// Webhook is one version of a webhook configuration. It binds an external event
// source (provider) to a flow via a JSONPath mapping. Secrets are stored via
// SecretRef (never inline), resolved by the SecretProvider at request time.
type Webhook struct {
	ID        string            `json:"id"`                  // stable webhook id
	Name      string            `json:"name"`                // human-readable display name
	SecretRef string            `json:"secretRef,omitempty"` // opaque pointer to secret (e.g. "env:STRIPE_WEBHOOK_SECRET")
	Provider  string            `json:"provider"`            // "stripe" | "github" | "generic"
	FlowID    string            `json:"flowId"`              // flow to trigger on match
	Mapping   map[string]string `json:"mapping,omitempty"`   // JSONPath mapping: {"flowField": "$.payload.field"}
	Filter    map[string][]string `json:"filter,omitempty"`  // filter predicates: {"event_type": ["payment.success"]}
	Env       string            `json:"env"`                 // environment this webhook belongs to
	Version   int               `json:"version"`             // version number of this configuration
}

// WebhookLog records one webhook invocation for audit/debug purposes.
type WebhookLog struct {
	ID             int64     `json:"id"`
	WebhookID      string    `json:"webhookId"`
	At             time.Time `json:"at"`
	Provider       string    `json:"provider"`
	EventType      string    `json:"eventType,omitempty"`
	FlowTriggered  string    `json:"flowTriggered,omitempty"` // flow_id if triggered, empty if filtered
	Status         string    `json:"status"`                  // "accepted" | "filtered" | "rejected" | "error"
	RequestHeaders map[string]string `json:"requestHeaders,omitempty"`
	PayloadHash    string    `json:"payloadHash,omitempty"`
	Error          string    `json:"error,omitempty"`
}

// WebhookSummary is the list response shape for GET /admin/webhooks.
type WebhookSummary struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Provider      string     `json:"provider"`
	FlowID        string     `json:"flowId"`
	Env           string     `json:"env"`
	ActiveVersion *int       `json:"activeVersion"` // nil if unpublished
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     *time.Time `json:"updatedAt,omitempty"`
}

// WebhookVersionSummary is one version in the list returned by GET /admin/webhooks/{id}/versions.
type WebhookVersionSummary struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	CreatedBy string    `json:"createdBy,omitempty"`
}

// Webhook log status constants.
const (
	WebhookStatusAccepted = "accepted" // webhook matched, flow triggered
	WebhookStatusFiltered = "filtered" // webhook matched but event filtered out
	WebhookStatusRejected = "rejected" // signature verification failed
	WebhookStatusError    = "error"    // processing error
)

