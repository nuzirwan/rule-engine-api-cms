package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nzr-rules-engine/internal/auth"
	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/flow"
)

// fakeWebhookAdminStore is a canned WebhookAdminStore for testing.
type fakeWebhookAdminStore struct {
	fakeAdminStore // embed the existing fake for AdminStore methods

	// Webhook-specific fields:
	createWebhookVersion int
	createWebhookErr     error
	getWebhook           config.Webhook
	getWebhookErr        error
	listWebhooks         []config.WebhookSummary
	listWebhooksErr      error
	updateWebhookVersion int
	updateWebhookErr     error
	deleteWebhookErr     error
	setWebhookActiveErr  error
	getWebhookLogs       []config.WebhookLog
	getWebhookLogsErr    error

	// capture:
	lastCreatedWebhook config.Webhook
	lastUpdatedWebhook config.Webhook
	lastDeletedID      string
	lastPublishedID    string
	lastPublishedVer   int
}

func (f *fakeWebhookAdminStore) CreateWebhook(ctx context.Context, env string, w config.Webhook) (int, error) {
	f.lastCreatedWebhook = w
	return f.createWebhookVersion, f.createWebhookErr
}

func (f *fakeWebhookAdminStore) GetWebhook(ctx context.Context, env, webhookID string) (config.Webhook, error) {
	return f.getWebhook, f.getWebhookErr
}

func (f *fakeWebhookAdminStore) ListWebhooks(ctx context.Context, env string) ([]config.WebhookSummary, error) {
	return f.listWebhooks, f.listWebhooksErr
}

func (f *fakeWebhookAdminStore) UpdateWebhook(ctx context.Context, env string, w config.Webhook) (int, error) {
	f.lastUpdatedWebhook = w
	return f.updateWebhookVersion, f.updateWebhookErr
}

func (f *fakeWebhookAdminStore) DeleteWebhook(ctx context.Context, env, webhookID string) error {
	f.lastDeletedID = webhookID
	return f.deleteWebhookErr
}

func (f *fakeWebhookAdminStore) SetWebhookActive(ctx context.Context, env, webhookID string, version int) error {
	f.lastPublishedID = webhookID
	f.lastPublishedVer = version
	return f.setWebhookActiveErr
}

func (f *fakeWebhookAdminStore) GetWebhookLogs(ctx context.Context, env, webhookID string, limit int) ([]config.WebhookLog, error) {
	return f.getWebhookLogs, f.getWebhookLogsErr
}

var _ WebhookAdminStore = (*fakeWebhookAdminStore)(nil)

// webhookOperator is an OperatorAuthenticator that allows operators with webhook roles.
type webhookOperator struct {
	roles []string
}

func (wo webhookOperator) AuthenticateOperator(ctx context.Context, r *http.Request) (auth.Operator, error) {
	return auth.Operator{Subject: "op:test", Roles: wo.roles}, nil
}

// newWebhookAdminTestHandler builds a handler with webhook admin support.
func newWebhookAdminTestHandler(t *testing.T, store *fakeWebhookAdminStore, roles []string) http.Handler {
	t.Helper()
	h, err := NewHandler(
		fakeStore{routes: []config.RouteInfo{}},
		flow.New(),
		Deps{Admin: store, OperAuth: webhookOperator{roles: roles}},
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

// TestWebhookAdminListWebhooks proves GET /admin/webhooks returns the list of webhooks.
func TestWebhookAdminListWebhooks(t *testing.T) {
	v := 2
	store := &fakeWebhookAdminStore{
		listWebhooks: []config.WebhookSummary{
			{ID: "stripe-orders", Name: "Stripe Orders", Provider: "stripe", FlowID: "process-payment", ActiveVersion: &v},
			{ID: "github-deploy", Name: "GitHub Deploy", Provider: "github", FlowID: "deploy-flow"},
		},
	}
	h := newWebhookAdminTestHandler(t, store, []string{"webhook.read", "webhook.write"})

	rec := doJSON(t, h, http.MethodGet, "/admin/webhooks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list webhooks status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	webhooks, ok := out["webhooks"].([]any)
	if !ok || len(webhooks) != 2 {
		t.Fatalf("list webhooks body = %v want 2 webhooks", out)
	}

	// Error case.
	store2 := &fakeWebhookAdminStore{listWebhooksErr: config.ErrUpstream}
	h2 := newWebhookAdminTestHandler(t, store2, []string{"webhook.read"})
	rec = doJSON(t, h2, http.MethodGet, "/admin/webhooks", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("list webhooks error status = %d want 502", rec.Code)
	}
}

// TestWebhookAdminGetWebhook proves GET /admin/webhooks/{id} returns a single webhook.
func TestWebhookAdminGetWebhook(t *testing.T) {
	store := &fakeWebhookAdminStore{
		getWebhook: config.Webhook{
			ID:        "stripe-orders",
			Name:      "Stripe Orders",
			SecretRef: "env:STRIPE_SECRET",
			Provider:  "stripe",
			FlowID:    "process-payment",
			Version:   3,
			Mapping:   map[string]string{"amount": "$.data.object.amount"},
			Filter:    map[string][]string{"type": {"payment_intent.succeeded"}},
		},
	}
	h := newWebhookAdminTestHandler(t, store, []string{"webhook.read"})

	rec := doJSON(t, h, http.MethodGet, "/admin/webhooks/stripe-orders", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get webhook status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["id"] != "stripe-orders" || out["provider"] != "stripe" {
		t.Fatalf("get webhook body = %v want id stripe-orders, provider stripe", out)
	}

	// Not found case.
	store2 := &fakeWebhookAdminStore{getWebhookErr: config.ErrNotFound}
	h2 := newWebhookAdminTestHandler(t, store2, []string{"webhook.read"})
	rec = doJSON(t, h2, http.MethodGet, "/admin/webhooks/nonexistent", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get webhook not found status = %d want 404", rec.Code)
	}
}

// TestWebhookAdminCreateWebhook proves POST /admin/webhooks creates a webhook.
func TestWebhookAdminCreateWebhook(t *testing.T) {
	store := &fakeWebhookAdminStore{createWebhookVersion: 1}
	h := newWebhookAdminTestHandler(t, store, []string{"webhook.write"})

	body := `{
		"id": "stripe-orders",
		"name": "Stripe Orders",
		"secretRef": "env:STRIPE_SECRET",
		"provider": "stripe",
		"flowId": "process-payment",
		"mapping": {"amount": "$.data.object.amount"},
		"filter": {"type": ["payment_intent.succeeded"]}
	}`
	rec := doJSON(t, h, http.MethodPost, "/admin/webhooks", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create webhook status = %d want 201 (body %s)", rec.Code, rec.Body.String())
	}

	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["id"] != "stripe-orders" || out["version"] != float64(1) {
		t.Fatalf("create webhook body = %v want id stripe-orders, version 1", out)
	}

	// Verify captured webhook.
	if store.lastCreatedWebhook.Name != "Stripe Orders" {
		t.Fatalf("captured webhook name = %q want 'Stripe Orders'", store.lastCreatedWebhook.Name)
	}

	// Default provider to generic.
	store2 := &fakeWebhookAdminStore{createWebhookVersion: 1}
	h2 := newWebhookAdminTestHandler(t, store2, []string{"webhook.write"})
	body2 := `{"id": "generic-hook", "name": "Generic", "secretRef": "env:SECRET", "flowId": "flow1"}`
	rec = doJSON(t, h2, http.MethodPost, "/admin/webhooks", body2)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create generic webhook status = %d want 201", rec.Code)
	}
	if store2.lastCreatedWebhook.Provider != "generic" {
		t.Fatalf("default provider = %q want 'generic'", store2.lastCreatedWebhook.Provider)
	}

	// Missing required fields.
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"secretRef": "env:X", "flowId": "f"}`, "name is required"},
		{`{"id": "x", "name": "N", "flowId": "f"}`, "secretRef is required"},
		{`{"id": "x", "name": "N", "secretRef": "env:X"}`, "flowId is required"},
		{`{"name": "N", "secretRef": "env:X", "flowId": "f"}`, "id is required"},
	} {
		rec := doJSON(t, h, http.MethodPost, "/admin/webhooks", tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("missing field body %q status = %d want 400", tc.body, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("missing field body %q error = %q want %q", tc.body, rec.Body.String(), tc.want)
		}
	}
}

// TestWebhookAdminUpdateWebhook proves PUT /admin/webhooks/{id} updates a webhook.
func TestWebhookAdminUpdateWebhook(t *testing.T) {
	store := &fakeWebhookAdminStore{
		getWebhook: config.Webhook{
			ID:        "stripe-orders",
			Name:      "Stripe Orders",
			SecretRef: "env:OLD_SECRET",
			Provider:  "stripe",
			FlowID:    "old-flow",
			Version:   1,
		},
		updateWebhookVersion: 2,
	}
	h := newWebhookAdminTestHandler(t, store, []string{"webhook.write"})

	// Partial update: only name and secretRef.
	body := `{"name": "Updated Name", "secretRef": "env:NEW_SECRET"}`
	rec := doJSON(t, h, http.MethodPut, "/admin/webhooks/stripe-orders", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("update webhook status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["version"] != float64(2) {
		t.Fatalf("update webhook body = %v want version 2", out)
	}

	// Verify merged update.
	if store.lastUpdatedWebhook.Name != "Updated Name" {
		t.Fatalf("updated name = %q want 'Updated Name'", store.lastUpdatedWebhook.Name)
	}
	if store.lastUpdatedWebhook.SecretRef != "env:NEW_SECRET" {
		t.Fatalf("updated secretRef = %q want 'env:NEW_SECRET'", store.lastUpdatedWebhook.SecretRef)
	}
	// Unchanged fields should be preserved.
	if store.lastUpdatedWebhook.FlowID != "old-flow" {
		t.Fatalf("preserved flowId = %q want 'old-flow'", store.lastUpdatedWebhook.FlowID)
	}

	// Not found case.
	store2 := &fakeWebhookAdminStore{getWebhookErr: config.ErrNotFound}
	h2 := newWebhookAdminTestHandler(t, store2, []string{"webhook.write"})
	rec = doJSON(t, h2, http.MethodPut, "/admin/webhooks/nonexistent", `{"name": "X"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("update webhook not found status = %d want 404", rec.Code)
	}
}

// TestWebhookAdminDeleteWebhook proves DELETE /admin/webhooks/{id} deletes a webhook.
func TestWebhookAdminDeleteWebhook(t *testing.T) {
	store := &fakeWebhookAdminStore{}
	h := newWebhookAdminTestHandler(t, store, []string{"webhook.write"})

	req := httptest.NewRequest(http.MethodDelete, "/admin/webhooks/stripe-orders", nil)
	req.Header.Set("Authorization", "Bearer x")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete webhook status = %d want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if store.lastDeletedID != "stripe-orders" {
		t.Fatalf("deleted id = %q want 'stripe-orders'", store.lastDeletedID)
	}

	// Not found case.
	store2 := &fakeWebhookAdminStore{deleteWebhookErr: config.ErrNotFound}
	h2 := newWebhookAdminTestHandler(t, store2, []string{"webhook.write"})
	req = httptest.NewRequest(http.MethodDelete, "/admin/webhooks/nonexistent", nil)
	req.Header.Set("Authorization", "Bearer x")
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete webhook not found status = %d want 404", rec.Code)
	}
}

// TestWebhookAdminPublishWebhook proves POST /admin/webhooks/{id}/publish activates a version.
func TestWebhookAdminPublishWebhook(t *testing.T) {
	store := &fakeWebhookAdminStore{}
	h := newWebhookAdminTestHandler(t, store, []string{"webhook.write"})

	body := `{"version": 3}`
	rec := doJSON(t, h, http.MethodPost, "/admin/webhooks/stripe-orders/publish", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("publish webhook status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["action"] != "publish" || out["activeVersion"] != float64(3) {
		t.Fatalf("publish webhook body = %v want action publish, activeVersion 3", out)
	}
	if store.lastPublishedID != "stripe-orders" || store.lastPublishedVer != 3 {
		t.Fatalf("published id=%q ver=%d want stripe-orders/3", store.lastPublishedID, store.lastPublishedVer)
	}

	// Missing version.
	rec = doJSON(t, h, http.MethodPost, "/admin/webhooks/stripe-orders/publish", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("publish no version status = %d want 400", rec.Code)
	}

	// Not found case.
	store2 := &fakeWebhookAdminStore{setWebhookActiveErr: config.ErrNotFound}
	h2 := newWebhookAdminTestHandler(t, store2, []string{"webhook.write"})
	rec = doJSON(t, h2, http.MethodPost, "/admin/webhooks/nonexistent/publish", `{"version":1}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("publish webhook not found status = %d want 404", rec.Code)
	}
}

// TestWebhookAdminGetWebhookLogs proves GET /admin/webhooks/{id}/logs returns invocation logs.
func TestWebhookAdminGetWebhookLogs(t *testing.T) {
	store := &fakeWebhookAdminStore{
		getWebhookLogs: []config.WebhookLog{
			{ID: 1, WebhookID: "stripe-orders", Provider: "stripe", EventType: "payment_intent.succeeded", Status: "accepted", At: time.Now()},
			{ID: 2, WebhookID: "stripe-orders", Provider: "stripe", EventType: "payment_intent.failed", Status: "filtered", At: time.Now()},
		},
	}
	h := newWebhookAdminTestHandler(t, store, []string{"webhook.read"})

	rec := doJSON(t, h, http.MethodGet, "/admin/webhooks/stripe-orders/logs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get webhook logs status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	logs, ok := out["logs"].([]any)
	if !ok || len(logs) != 2 {
		t.Fatalf("get webhook logs body = %v want 2 logs", out)
	}

	// Verify logs don't contain requestHeaders (for security).
	for _, log := range logs {
		m := log.(map[string]any)
		if _, hasHeaders := m["requestHeaders"]; hasHeaders {
			t.Fatalf("logs should not contain requestHeaders: %v", m)
		}
	}

	// With limit param.
	rec = doJSON(t, h, http.MethodGet, "/admin/webhooks/stripe-orders/logs?limit=50", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get webhook logs with limit status = %d want 200", rec.Code)
	}
}

// TestWebhookAdminRBAC proves RBAC enforcement for webhook routes.
func TestWebhookAdminRBAC(t *testing.T) {
	store := &fakeWebhookAdminStore{
		listWebhooks:         []config.WebhookSummary{},
		getWebhook:           config.Webhook{ID: "test"},
		createWebhookVersion: 1,
	}

	// Read-only operator can GET but not POST/PUT/DELETE.
	hRead := newWebhookAdminTestHandler(t, store, []string{"webhook.read"})
	rec := doJSON(t, hRead, http.MethodGet, "/admin/webhooks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("read-only list status = %d want 200", rec.Code)
	}
	rec = doJSON(t, hRead, http.MethodPost, "/admin/webhooks",
		`{"id":"x","name":"N","secretRef":"env:X","flowId":"f"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("read-only create status = %d want 403", rec.Code)
	}

	// Write-only operator can POST/PUT/DELETE but not GET (well, actually the
	// current RBAC returns flow.read for GET on non-webhook paths, but we want
	// webhook.read for webhook paths).
	hWrite := newWebhookAdminTestHandler(t, store, []string{"webhook.write"})
	rec = doJSON(t, hWrite, http.MethodPost, "/admin/webhooks",
		`{"id":"x","name":"N","secretRef":"env:X","flowId":"f"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("write-only create status = %d want 201", rec.Code)
	}
	rec = doJSON(t, hWrite, http.MethodGet, "/admin/webhooks", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("write-only list status = %d want 403", rec.Code)
	}

	// No roles operator should be forbidden for all.
	hNone := newWebhookAdminTestHandler(t, store, []string{})
	rec = doJSON(t, hNone, http.MethodGet, "/admin/webhooks", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no-role list status = %d want 403", rec.Code)
	}
}

// TestWebhookAdminDisabledPlane proves that without operator auth, webhook admin returns 503.
func TestWebhookAdminDisabledPlane(t *testing.T) {
	store := &fakeWebhookAdminStore{}
	h, err := NewHandler(
		fakeStore{routes: []config.RouteInfo{}},
		flow.New(),
		Deps{Admin: store, OperAuth: nil}, // nil OperAuth = plane disabled
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	rec := doJSON(t, h, http.MethodGet, "/admin/webhooks", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled plane status = %d want 503", rec.Code)
	}
}

// TestWebhookAdminUnknownProvider proves unknown provider is rejected.
func TestWebhookAdminUnknownProvider(t *testing.T) {
	store := &fakeWebhookAdminStore{createWebhookVersion: 1}
	h := newWebhookAdminTestHandler(t, store, []string{"webhook.write"})

	body := `{"id":"x","name":"N","secretRef":"env:X","flowId":"f","provider":"unknown"}`
	rec := doJSON(t, h, http.MethodPost, "/admin/webhooks", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown provider status = %d want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unknown provider") {
		t.Fatalf("error should mention unknown provider: %s", rec.Body.String())
	}
}
