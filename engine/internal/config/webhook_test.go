package config

import (
	"testing"
)

// TestWebhookProviderConstants verifies the provider constants are correctly defined.
func TestWebhookProviderConstants(t *testing.T) {
	tests := []struct {
		name     string
		constant string
		want     string
	}{
		{"ProviderStripe", ProviderStripe, "stripe"},
		{"ProviderGitHub", ProviderGitHub, "github"},
		{"ProviderGeneric", ProviderGeneric, "generic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.constant != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, tt.constant, tt.want)
			}
		})
	}
}

// TestWebhookStatusConstants verifies the webhook log status constants.
func TestWebhookStatusConstants(t *testing.T) {
	tests := []struct {
		name     string
		constant string
		want     string
	}{
		{"WebhookStatusAccepted", WebhookStatusAccepted, "accepted"},
		{"WebhookStatusFiltered", WebhookStatusFiltered, "filtered"},
		{"WebhookStatusRejected", WebhookStatusRejected, "rejected"},
		{"WebhookStatusError", WebhookStatusError, "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.constant != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, tt.constant, tt.want)
			}
		})
	}
}

// TestWebhookStructFields verifies the Webhook struct can hold all required fields.
func TestWebhookStructFields(t *testing.T) {
	w := Webhook{
		ID:        "stripe-payment",
		Name:      "Stripe Payment Webhook",
		SecretRef: "env:STRIPE_WEBHOOK_SECRET",
		Provider:  ProviderStripe,
		FlowID:    "payment-flow",
		Mapping: map[string]string{
			"orderId": "$.data.object.metadata.order_id",
			"amount":  "$.data.object.amount",
		},
		Filter: map[string][]string{
			"event_type": {"payment_intent.succeeded", "payment_intent.failed"},
		},
		Env:     "production",
		Version: 1,
	}

	// Verify all fields are set correctly.
	if w.ID != "stripe-payment" {
		t.Errorf("ID = %q, want %q", w.ID, "stripe-payment")
	}
	if w.Name != "Stripe Payment Webhook" {
		t.Errorf("Name = %q, want %q", w.Name, "Stripe Payment Webhook")
	}
	if w.SecretRef != "env:STRIPE_WEBHOOK_SECRET" {
		t.Errorf("SecretRef = %q, want %q", w.SecretRef, "env:STRIPE_WEBHOOK_SECRET")
	}
	if w.Provider != ProviderStripe {
		t.Errorf("Provider = %q, want %q", w.Provider, ProviderStripe)
	}
	if w.FlowID != "payment-flow" {
		t.Errorf("FlowID = %q, want %q", w.FlowID, "payment-flow")
	}
	if len(w.Mapping) != 2 {
		t.Errorf("Mapping length = %d, want 2", len(w.Mapping))
	}
	if w.Mapping["orderId"] != "$.data.object.metadata.order_id" {
		t.Errorf("Mapping[orderId] = %q, want %q", w.Mapping["orderId"], "$.data.object.metadata.order_id")
	}
	if len(w.Filter) != 1 {
		t.Errorf("Filter length = %d, want 1", len(w.Filter))
	}
	if len(w.Filter["event_type"]) != 2 {
		t.Errorf("Filter[event_type] length = %d, want 2", len(w.Filter["event_type"]))
	}
	if w.Env != "production" {
		t.Errorf("Env = %q, want %q", w.Env, "production")
	}
	if w.Version != 1 {
		t.Errorf("Version = %d, want 1", w.Version)
	}
}

// TestWebhookLogStructFields verifies the WebhookLog struct can hold all required fields.
func TestWebhookLogStructFields(t *testing.T) {
	log := WebhookLog{
		ID:            1,
		WebhookID:     "stripe-payment",
		Provider:      ProviderStripe,
		EventType:     "payment_intent.succeeded",
		FlowTriggered: "payment-flow",
		Status:        WebhookStatusAccepted,
		RequestHeaders: map[string]string{
			"stripe-signature": "t=123,v1=abc",
			"content-type":     "application/json",
		},
		PayloadHash: "sha256:abc123",
		Error:       "",
	}

	if log.ID != 1 {
		t.Errorf("ID = %d, want 1", log.ID)
	}
	if log.WebhookID != "stripe-payment" {
		t.Errorf("WebhookID = %q, want %q", log.WebhookID, "stripe-payment")
	}
	if log.Provider != ProviderStripe {
		t.Errorf("Provider = %q, want %q", log.Provider, ProviderStripe)
	}
	if log.EventType != "payment_intent.succeeded" {
		t.Errorf("EventType = %q, want %q", log.EventType, "payment_intent.succeeded")
	}
	if log.FlowTriggered != "payment-flow" {
		t.Errorf("FlowTriggered = %q, want %q", log.FlowTriggered, "payment-flow")
	}
	if log.Status != WebhookStatusAccepted {
		t.Errorf("Status = %q, want %q", log.Status, WebhookStatusAccepted)
	}
	if len(log.RequestHeaders) != 2 {
		t.Errorf("RequestHeaders length = %d, want 2", len(log.RequestHeaders))
	}
	if log.PayloadHash != "sha256:abc123" {
		t.Errorf("PayloadHash = %q, want %q", log.PayloadHash, "sha256:abc123")
	}
}

// TestWebhookSummaryStructFields verifies the WebhookSummary struct.
func TestWebhookSummaryStructFields(t *testing.T) {
	activeVersion := 2
	ws := WebhookSummary{
		ID:            "stripe-payment",
		Name:          "Stripe Payment Webhook",
		Provider:      ProviderStripe,
		FlowID:        "payment-flow",
		Env:           "production",
		ActiveVersion: &activeVersion,
	}

	if ws.ID != "stripe-payment" {
		t.Errorf("ID = %q, want %q", ws.ID, "stripe-payment")
	}
	if ws.Name != "Stripe Payment Webhook" {
		t.Errorf("Name = %q, want %q", ws.Name, "Stripe Payment Webhook")
	}
	if ws.ActiveVersion == nil || *ws.ActiveVersion != 2 {
		t.Errorf("ActiveVersion = %v, want %d", ws.ActiveVersion, 2)
	}

	// Test nil ActiveVersion (unpublished).
	wsUnpublished := WebhookSummary{
		ID:            "new-webhook",
		Name:          "New Webhook",
		Provider:      ProviderGeneric,
		ActiveVersion: nil,
	}
	if wsUnpublished.ActiveVersion != nil {
		t.Errorf("unpublished ActiveVersion = %v, want nil", wsUnpublished.ActiveVersion)
	}
}

// TestWebhookVersionSummaryStructFields verifies the WebhookVersionSummary struct.
func TestWebhookVersionSummaryStructFields(t *testing.T) {
	vs := WebhookVersionSummary{
		Version:   3,
		CreatedBy: "admin@example.com",
	}

	if vs.Version != 3 {
		t.Errorf("Version = %d, want 3", vs.Version)
	}
	if vs.CreatedBy != "admin@example.com" {
		t.Errorf("CreatedBy = %q, want %q", vs.CreatedBy, "admin@example.com")
	}
}

// TestNullIfEmpty verifies the nullIfEmpty helper function.
func TestNullIfEmpty(t *testing.T) {
	// Empty string returns nil.
	if result := nullIfEmpty(""); result != nil {
		t.Errorf("nullIfEmpty(\"\") = %v, want nil", result)
	}

	// Non-empty string returns pointer to the string.
	result := nullIfEmpty("test")
	if result == nil {
		t.Error("nullIfEmpty(\"test\") = nil, want non-nil")
	} else if *result != "test" {
		t.Errorf("nullIfEmpty(\"test\") = %q, want %q", *result, "test")
	}
}

