package worker

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockReadyWorker wraps a Worker to simulate ready/not-ready states for testing.
type mockReadyWorker struct {
	*Worker
	mockReady bool
}

func TestHandler_Healthz(t *testing.T) {
	store := &mockWorkerStore{}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   store,
	})
	h := NewHandler(w)

	mux := http.NewServeMux()
	h.Mount(mux)

	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("healthz status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("healthz body = %q, want %q", rec.Body.String(), "ok")
	}
}

func TestHandler_Readyz_NotReady(t *testing.T) {
	store := &mockWorkerStore{}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   store,
	})
	h := NewHandler(w)

	mux := http.NewServeMux()
	h.Mount(mux)

	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if rec.Body.String() != "not ready" {
		t.Errorf("readyz body = %q, want %q", rec.Body.String(), "not ready")
	}
}

func TestHandler_DebugConfig(t *testing.T) {
	store := &mockWorkerStore{}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   store,
	})
	h := NewHandler(w)

	mux := http.NewServeMux()
	h.Mount(mux)

	req := httptest.NewRequest("GET", "/debug/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("debug/config status = %d, want %d", rec.Code, http.StatusOK)
	}

	var cfg DebugConfig
	if err := json.NewDecoder(rec.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode debug config: %v", err)
	}
	if cfg.GroupID != "test-group" {
		t.Errorf("debug config groupId = %q, want %q", cfg.GroupID, "test-group")
	}
	if cfg.Ready {
		t.Error("debug config ready = true, want false")
	}
}

func TestHandler_Execute_NotReady(t *testing.T) {
	store := &mockWorkerStore{}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   store,
	})
	h := NewHandler(w)

	mux := http.NewServeMux()
	h.Mount(mux)

	body := `{"flowId": "test-flow", "input": {}}`
	req := httptest.NewRequest("POST", "/execute", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("execute status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var resp ExecuteResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error in response")
	}
	if resp.Error.Code != ErrCodeNotReady {
		t.Errorf("error code = %q, want %q", resp.Error.Code, ErrCodeNotReady)
	}
}

func TestHandler_Execute_InvalidRequest(t *testing.T) {
	store := &mockWorkerStore{}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   store,
	})
	// Manually set ready to test request validation.
	w.ready.Store(1)

	h := NewHandler(w)

	mux := http.NewServeMux()
	h.Mount(mux)

	// Test invalid JSON.
	req := httptest.NewRequest("POST", "/execute", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("execute invalid json status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var resp ExecuteResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != ErrCodeInvalidRequest {
		t.Errorf("error code = %v, want %q", resp.Error, ErrCodeInvalidRequest)
	}
}

func TestHandler_Execute_MissingFlowID(t *testing.T) {
	store := &mockWorkerStore{}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   store,
	})
	// Manually set ready to test request validation.
	w.ready.Store(1)

	h := NewHandler(w)

	mux := http.NewServeMux()
	h.Mount(mux)

	// Test missing flowId.
	body := `{"input": {}}`
	req := httptest.NewRequest("POST", "/execute", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("execute missing flowId status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var resp ExecuteResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != ErrCodeInvalidRequest {
		t.Errorf("error code = %v, want %q", resp.Error, ErrCodeInvalidRequest)
	}
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		name       string
		errMsg     string
		wantCode   string
		wantStatus int
	}{
		{"flow not found", "flow not found: test", ErrCodeFlowNotFound, http.StatusNotFound},
		{"worker not loaded", "worker not loaded", ErrCodeNotReady, http.StatusServiceUnavailable},
		{"timeout", "context deadline exceeded", ErrCodeTimeout, http.StatusGatewayTimeout},
		{"validation", "validation error: bad input", ErrCodeValidation, http.StatusBadRequest},
		{"upstream", "upstream error: connection failed", ErrCodeUpstream, http.StatusBadGateway},
		{"internal", "some internal error", ErrCodeInternal, http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &testError{msg: tc.errMsg}
			code, status := classifyError(err)
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
		})
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string { return e.msg }
