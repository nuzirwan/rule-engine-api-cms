package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/webhook"
)

// WebhookStore is the subset of config store needed by the webhook receiver.
type WebhookStore interface {
	GetActiveWebhook(ctx context.Context, env, webhookID string) (config.Webhook, error)
	LogWebhook(ctx context.Context, env string, log config.WebhookLog) (int64, error)
	GetActiveFlowByID(ctx context.Context, env, flowID string) (config.FlowVersion, error)
}

// webhookReceiver handles incoming webhook requests. It looks up the webhook
// config, verifies the signature, extracts the payload, and triggers the
// configured flow.
type webhookReceiver struct {
	store   WebhookStore
	interp  *flow.Interpreter
	deps    Deps
	secrets connect.SecretProvider
}

// newWebhookReceiver creates a webhook receiver with the given dependencies.
func newWebhookReceiver(store WebhookStore, interp *flow.Interpreter, deps Deps, secrets connect.SecretProvider) *webhookReceiver {
	return &webhookReceiver{
		store:   store,
		interp:  interp,
		deps:    deps,
		secrets: secrets,
	}
}

// webhookHandler returns an http.HandlerFunc for POST /webhooks/{webhook_id}.
// The endpoint is public (no operator auth) but rate-limited.
func webhookHandler(store WebhookStore, interp *flow.Interpreter, deps Deps, secrets connect.SecretProvider, flowStore Store) http.HandlerFunc {
	receiver := newWebhookReceiver(store, interp, deps, secrets)

	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Parse webhook_id from path: /webhooks/{webhook_id}
		webhookID := extractWebhookID(r.URL.Path)
		if webhookID == "" {
			writeError(w, http.StatusBadRequest, "missing webhook_id")
			return
		}

		// Read the raw body for signature verification.
		body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10MB limit
		if err != nil {
			logWebhook(ctx, store, webhookID, "", "", config.WebhookStatusError, nil, "", err.Error())
			writeError(w, http.StatusBadRequest, "failed to read request body")
			return
		}

		// Extract headers for logging and provider use.
		headers := extractHeaders(r)

		// Get webhook configuration.
		wh, err := store.GetActiveWebhook(ctx, defaultEnv, webhookID)
		if err != nil {
			if errors.Is(err, config.ErrNotFound) {
				logWebhook(ctx, store, webhookID, "", "", config.WebhookStatusError, headers, hashPayload(body), "webhook not found")
				writeError(w, http.StatusNotFound, "webhook not found")
				return
			}
			logWebhook(ctx, store, webhookID, "", "", config.WebhookStatusError, headers, hashPayload(body), err.Error())
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Resolve secret for signature verification.
		var secretBytes []byte
		if wh.SecretRef != "" {
			secret, err := secrets.Resolve(ctx, wh.SecretRef)
			if err != nil {
				logWebhook(ctx, store, webhookID, wh.Provider, "", config.WebhookStatusError, headers, hashPayload(body), "secret resolution failed")
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			secretBytes = secret.Reveal()
		}

		// Get provider and verify signature.
		provider := webhook.GetProvider(wh.Provider)
		sigHeader := getSignatureHeader(r, wh.Provider)

		if len(secretBytes) > 0 {
			err = provider.VerifySignature(webhook.SignatureParams{
				Header:    sigHeader,
				Algorithm: "sha256",
				Secret:    secretBytes,
				Payload:   body,
			})
			if err != nil {
				logWebhook(ctx, store, webhookID, wh.Provider, "", config.WebhookStatusRejected, redactHeaders(headers), hashPayload(body), "signature verification failed")
				writeError(w, http.StatusUnauthorized, "invalid signature")
				return
			}
		}

		// Parse event from payload.
		event, err := provider.ParseEvent(body, headers)
		if err != nil {
			logWebhook(ctx, store, webhookID, wh.Provider, "", config.WebhookStatusError, redactHeaders(headers), hashPayload(body), "payload parse failed: "+err.Error())
			writeError(w, http.StatusBadRequest, "invalid payload")
			return
		}

		// Check filter - if event doesn't match, ack without triggering flow.
		if !webhook.MatchesFilter(body, wh.Filter) {
			logWebhook(ctx, store, webhookID, wh.Provider, event.Type, config.WebhookStatusFiltered, redactHeaders(headers), hashPayload(body), "")
			writeJSON(w, http.StatusOK, map[string]any{"status": "filtered"})
			return
		}

		// Build flow context from mapping.
		input := webhook.MapPayload(body, wh.Mapping)

		// Add standard webhook context.
		input["webhook"] = map[string]any{
			"id":        webhookID,
			"provider":  wh.Provider,
			"eventType": event.Type,
		}
		input["payload"] = event.Payload

		// Resolve the flow to trigger.
		fv, err := receiver.resolveFlow(ctx, wh.FlowID)
		if err != nil {
			logWebhook(ctx, store, webhookID, wh.Provider, event.Type, config.WebhookStatusError, redactHeaders(headers), hashPayload(body), "flow not found: "+wh.FlowID)
			writeError(w, http.StatusInternalServerError, "flow not found")
			return
		}

		// Build the flow context.
		c := flow.NewCtx(requestID(r), traceID(r), defaultEnv, input)

		// Stamp env on ctx for per-env JDM resolution.
		ctx = decision.WithEnv(ctx, defaultEnv)

		// Per-request deps.
		runDeps := flow.Deps{
			Conns:  newTemplatingRegistry(deps.Conns, c),
			Decide: newBranchingEvaluator(deps.Decide),
			Trace:  deps.Trace,
			Log:    deps.Log,
		}

		// Run the flow.
		if err := interp.Run(ctx, &fv.Tree, flow.Version{FlowID: fv.FlowID, Version: fv.Version}, c, runDeps); err != nil {
			logWebhook(ctx, store, webhookID, wh.Provider, event.Type, config.WebhookStatusError, redactHeaders(headers), hashPayload(body), "flow error: "+err.Error())
			status, msg := statusForFlow(err)
			writeError(w, status, msg)
			return
		}

		// Log successful webhook processing.
		logWebhook(ctx, store, webhookID, wh.Provider, event.Type, config.WebhookStatusAccepted, redactHeaders(headers), hashPayload(body), "")

		// Return flow response or simple ack.
		if len(c.Response) > 0 {
			writeJSON(w, http.StatusOK, c.Response)
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"status": "accepted"})
		}
	}
}

// resolveFlow resolves the active flow version for the webhook's target flow.
// This is different from genericFlowHandler which resolves by method+path.
func (wr *webhookReceiver) resolveFlow(ctx context.Context, flowID string) (config.FlowVersion, error) {
	return wr.store.GetActiveFlowByID(ctx, defaultEnv, flowID)
}

// extractWebhookID extracts the webhook_id from the URL path.
// Path format: /webhooks/{webhook_id}
func extractWebhookID(path string) string {
	// Trim prefix and any trailing slash.
	path = strings.TrimPrefix(path, "/webhooks/")
	path = strings.TrimSuffix(path, "/")

	// Handle any additional path segments (shouldn't have any).
	if idx := strings.Index(path, "/"); idx >= 0 {
		path = path[:idx]
	}
	return path
}

// getSignatureHeader returns the signature header value based on provider.
func getSignatureHeader(r *http.Request, provider string) string {
	switch provider {
	case config.ProviderStripe:
		return r.Header.Get("Stripe-Signature")
	case config.ProviderGitHub:
		return r.Header.Get("X-Hub-Signature-256")
	default:
		// Generic: try common header names.
		if h := r.Header.Get("X-Webhook-Signature"); h != "" {
			return h
		}
		if h := r.Header.Get("X-Signature"); h != "" {
			return h
		}
		return r.Header.Get("Signature")
	}
}

// extractHeaders extracts relevant headers from the request.
func extractHeaders(r *http.Request) map[string]string {
	headers := make(map[string]string)
	for k, v := range r.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}
	return headers
}

// redactHeaders removes sensitive headers before logging.
func redactHeaders(headers map[string]string) map[string]string {
	redacted := make(map[string]string, len(headers))
	for k, v := range headers {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "signature") || strings.Contains(lk, "secret") || strings.Contains(lk, "auth") {
			redacted[k] = "[REDACTED]"
		} else {
			redacted[k] = v
		}
	}
	return redacted
}

// hashPayload computes a SHA-256 hash of the payload for logging (not the raw payload).
func hashPayload(payload []byte) string {
	h := sha256.Sum256(payload)
	return hex.EncodeToString(h[:])
}

// logWebhook logs a webhook invocation (best-effort, does not fail the request).
func logWebhook(ctx context.Context, store WebhookStore, webhookID, provider, eventType, status string, headers map[string]string, payloadHash, errMsg string) {
	if store == nil {
		return
	}
	_, _ = store.LogWebhook(ctx, defaultEnv, config.WebhookLog{
		WebhookID:      webhookID,
		Provider:       provider,
		EventType:      eventType,
		Status:         status,
		RequestHeaders: headers,
		PayloadHash:    payloadHash,
		Error:          errMsg,
	})
}

// WebhookHandlerDeps contains the dependencies for mounting the webhook handler.
type WebhookHandlerDeps struct {
	WebhookStore WebhookStore
	FlowStore    Store
	Interpreter  *flow.Interpreter
	Deps         Deps
	Secrets      connect.SecretProvider
}

// MountWebhookHandler mounts the webhook handler on the given mux.
func MountWebhookHandler(mux *http.ServeMux, d WebhookHandlerDeps) {
	if d.WebhookStore == nil || d.Secrets == nil {
		return // No webhook support configured.
	}
	handler := webhookHandler(d.WebhookStore, d.Interpreter, d.Deps, d.Secrets, d.FlowStore)
	mux.HandleFunc("POST /webhooks/", handler)
}

// parseJSON decodes r's JSON body into v with a 1MB limit.
func parseWebhookJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}
