package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/flow"
)

// fakeWebhookStore implements WebhookStore for testing.
type fakeWebhookStore struct {
	webhooks map[string]config.Webhook
	flows    map[string]config.FlowVersion
	logs     []config.WebhookLog
}

func newFakeWebhookStore() *fakeWebhookStore {
	return &fakeWebhookStore{
		webhooks: make(map[string]config.Webhook),
		flows:    make(map[string]config.FlowVersion),
		logs:     make([]config.WebhookLog, 0),
	}
}

func (s *fakeWebhookStore) GetActiveWebhook(ctx context.Context, env, webhookID string) (config.Webhook, error) {
	w, ok := s.webhooks[webhookID]
	if !ok {
		return config.Webhook{}, config.ErrNotFound
	}
	return w, nil
}

func (s *fakeWebhookStore) LogWebhook(ctx context.Context, env string, log config.WebhookLog) (int64, error) {
	s.logs = append(s.logs, log)
	return int64(len(s.logs)), nil
}

func (s *fakeWebhookStore) GetActiveFlowByID(ctx context.Context, env, flowID string) (config.FlowVersion, error) {
	fv, ok := s.flows[flowID]
	if !ok {
		return config.FlowVersion{}, config.ErrNotFound
	}
	return fv, nil
}

// fakeSecretProvider implements connect.SecretProvider for testing.
type fakeSecretProvider struct {
	secrets map[string][]byte
}

func (p *fakeSecretProvider) Resolve(ctx context.Context, ref string) (connect.Secret, error) {
	if v, ok := p.secrets[ref]; ok {
		return connect.NewSecret(v), nil
	}
	return connect.Secret{}, nil
}

func (p *fakeSecretProvider) Watch(ctx context.Context, ref string, onChange func()) (func(), error) {
	return func() {}, nil
}

func TestExtractWebhookID(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/webhooks/abc123", "abc123"},
		{"/webhooks/abc123/", "abc123"},
		{"/webhooks/wh_test_id", "wh_test_id"},
		{"/webhooks/", ""},
		{"/webhooks", ""},
	}

	for _, tc := range tests {
		got := extractWebhookID(tc.path)
		if got != tc.want {
			t.Errorf("extractWebhookID(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestWebhookHandlerNotFound(t *testing.T) {
	store := newFakeWebhookStore()
	secrets := &fakeSecretProvider{secrets: map[string][]byte{}}

	handler := webhookHandler(store, flow.New(), Deps{}, secrets, nil)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/nonexistent", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	// Verify log was created.
	if len(store.logs) != 1 {
		t.Errorf("logs = %d, want 1", len(store.logs))
	}
}

func TestWebhookHandlerBadSignature(t *testing.T) {
	store := newFakeWebhookStore()
	store.webhooks["wh_test"] = config.Webhook{
		ID:        "wh_test",
		Provider:  config.ProviderStripe,
		SecretRef: "env:STRIPE_SECRET",
		FlowID:    "flow_1",
	}

	secrets := &fakeSecretProvider{
		secrets: map[string][]byte{
			"env:STRIPE_SECRET": []byte("whsec_test_secret"),
		},
	}

	handler := webhookHandler(store, flow.New(), Deps{}, secrets, nil)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/wh_test", bytes.NewReader([]byte(`{"type":"test"}`)))
	req.Header.Set("Stripe-Signature", "t=12345,v1=invalid")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	// Verify rejection was logged.
	if len(store.logs) != 1 || store.logs[0].Status != config.WebhookStatusRejected {
		t.Errorf("expected rejected log, got %+v", store.logs)
	}
}

func TestWebhookHandlerFiltered(t *testing.T) {
	store := newFakeWebhookStore()
	store.webhooks["wh_test"] = config.Webhook{
		ID:       "wh_test",
		Provider: config.ProviderGeneric,
		FlowID:   "flow_1",
		Filter: map[string][]string{
			"$.type": {"allowed.event"},
		},
	}

	handler := webhookHandler(store, flow.New(), Deps{}, &fakeSecretProvider{}, nil)

	payload := []byte(`{"type":"filtered.event"}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/wh_test", bytes.NewReader(payload))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (filtered should return 200)", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "filtered" {
		t.Errorf("response status = %v, want 'filtered'", resp["status"])
	}

	// Verify filter log.
	if len(store.logs) != 1 || store.logs[0].Status != config.WebhookStatusFiltered {
		t.Errorf("expected filtered log, got %+v", store.logs)
	}
}

func TestWebhookHandlerSuccess(t *testing.T) {
	// Create a minimal flow: trigger -> response.
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)}
	trig := flow.Node{
		ID:       "t",
		Type:     flow.TypeTrigger,
		Spec:     json.RawMessage(`{"method":"POST","path":"/trigger","input":{}}`),
		Children: []flow.Node{resp},
	}

	store := newFakeWebhookStore()
	store.webhooks["wh_test"] = config.Webhook{
		ID:       "wh_test",
		Provider: config.ProviderGeneric,
		FlowID:   "flow_1",
		Mapping: map[string]string{
			"eventId": "$.id",
		},
	}
	store.flows["flow_1"] = config.FlowVersion{
		FlowID:  "flow_1",
		Version: 1,
		Tree:    trig,
	}

	handler := webhookHandler(store, flow.New(), Deps{}, &fakeSecretProvider{}, nil)

	payload := []byte(`{"id":"evt_123","type":"test.event"}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/wh_test", bytes.NewReader(payload))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify success log.
	if len(store.logs) != 1 || store.logs[0].Status != config.WebhookStatusAccepted {
		t.Errorf("expected accepted log, got %+v", store.logs)
	}
}

func TestWebhookHandlerStripeSignature(t *testing.T) {
	secret := []byte("whsec_test_secret")
	payload := []byte(`{"id":"evt_123","type":"payment_intent.succeeded"}`)

	// Create valid Stripe signature.
	ts := time.Now().Unix()
	signedPayload := fmt.Sprintf("%d.%s", ts, payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signedPayload))
	validSig := hex.EncodeToString(mac.Sum(nil))
	sigHeader := fmt.Sprintf("t=%d,v1=%s", ts, validSig)

	// Minimal flow.
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)}
	trig := flow.Node{
		ID:       "t",
		Type:     flow.TypeTrigger,
		Spec:     json.RawMessage(`{"method":"POST","path":"/trigger","input":{}}`),
		Children: []flow.Node{resp},
	}

	store := newFakeWebhookStore()
	store.webhooks["wh_stripe"] = config.Webhook{
		ID:        "wh_stripe",
		Provider:  config.ProviderStripe,
		SecretRef: "env:STRIPE_SECRET",
		FlowID:    "flow_1",
	}
	store.flows["flow_1"] = config.FlowVersion{FlowID: "flow_1", Version: 1, Tree: trig}

	secrets := &fakeSecretProvider{
		secrets: map[string][]byte{
			"env:STRIPE_SECRET": secret,
		},
	}

	handler := webhookHandler(store, flow.New(), Deps{}, secrets, nil)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/wh_stripe", bytes.NewReader(payload))
	req.Header.Set("Stripe-Signature", sigHeader)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestWebhookHandlerGitHubSignature(t *testing.T) {
	secret := []byte("github_secret")
	payload := []byte(`{"action":"opened","number":42}`)

	// Create valid GitHub signature.
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	sigHeader := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	// Minimal flow.
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)}
	trig := flow.Node{
		ID:       "t",
		Type:     flow.TypeTrigger,
		Spec:     json.RawMessage(`{"method":"POST","path":"/trigger","input":{}}`),
		Children: []flow.Node{resp},
	}

	store := newFakeWebhookStore()
	store.webhooks["wh_github"] = config.Webhook{
		ID:        "wh_github",
		Provider:  config.ProviderGitHub,
		SecretRef: "env:GITHUB_SECRET",
		FlowID:    "flow_1",
	}
	store.flows["flow_1"] = config.FlowVersion{FlowID: "flow_1", Version: 1, Tree: trig}

	secrets := &fakeSecretProvider{
		secrets: map[string][]byte{
			"env:GITHUB_SECRET": secret,
		},
	}

	handler := webhookHandler(store, flow.New(), Deps{}, secrets, nil)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/wh_github", bytes.NewReader(payload))
	req.Header.Set("X-Hub-Signature-256", sigHeader)
	req.Header.Set("X-GitHub-Event", "pull_request")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestRedactHeaders(t *testing.T) {
	headers := map[string]string{
		"Content-Type":        "application/json",
		"Stripe-Signature":    "sensitive",
		"X-Secret-Key":        "also-sensitive",
		"Authorization":       "Bearer token",
		"X-Request-Id":        "req-123",
	}

	redacted := redactHeaders(headers)

	if redacted["Content-Type"] != "application/json" {
		t.Errorf("Content-Type should not be redacted")
	}
	if redacted["X-Request-Id"] != "req-123" {
		t.Errorf("X-Request-Id should not be redacted")
	}
	if redacted["Stripe-Signature"] != "[REDACTED]" {
		t.Errorf("Stripe-Signature should be redacted")
	}
	if redacted["X-Secret-Key"] != "[REDACTED]" {
		t.Errorf("X-Secret-Key should be redacted")
	}
	if redacted["Authorization"] != "[REDACTED]" {
		t.Errorf("Authorization should be redacted")
	}
}

func TestHashPayload(t *testing.T) {
	payload := []byte(`{"test":"data"}`)
	hash := hashPayload(payload)

	if len(hash) != 64 { // SHA-256 produces 32 bytes = 64 hex chars
		t.Errorf("hash length = %d, want 64", len(hash))
	}

	// Same payload should produce same hash.
	hash2 := hashPayload(payload)
	if hash != hash2 {
		t.Errorf("hash not deterministic")
	}
}
