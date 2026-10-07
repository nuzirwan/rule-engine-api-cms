package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/gateway"
	"nzr-rules-engine/internal/observ"
	"nzr-rules-engine/internal/worker"
)

// fakeDispatcher is a test double for gateway.Dispatcher that records calls and
// returns canned responses.
type fakeDispatcher struct {
	calls     []dispatchCall
	responses []*worker.ExecuteResponse
	errors    []error
	callIndex int
}

type dispatchCall struct {
	FlowID string
	Group  string
	Input  map[string]any
}

func (d *fakeDispatcher) Dispatch(ctx context.Context, flowID string, group string, input map[string]any) (*worker.ExecuteResponse, error) {
	d.calls = append(d.calls, dispatchCall{FlowID: flowID, Group: group, Input: input})
	idx := d.callIndex
	d.callIndex++
	if idx < len(d.responses) {
		return d.responses[idx], nil
	}
	if idx < len(d.errors) {
		return nil, d.errors[idx]
	}
	return &worker.ExecuteResponse{Status: http.StatusOK}, nil
}

// TestGenericFlowHandler_InlineMode verifies that when dispatch mode is inline
// (the default), the interpreter is called directly and the dispatcher is not used.
func TestGenericFlowHandler_InlineMode(t *testing.T) {
	// Create a minimal trigger->response flow
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)}
	trig := flow.Node{
		ID:       "t",
		Type:     flow.TypeTrigger,
		Spec:     json.RawMessage(`{"method":"GET","path":"/orders/{id}","input":{"params":["id"]}}`),
		Children: []flow.Node{resp},
	}
	store := fakeStore{
		fv:     config.FlowVersion{FlowID: "f", Version: 1, Method: "GET", Path: "/orders/{id}", Tree: trig},
		routes: []config.RouteInfo{{FlowID: "f", Method: "GET", Path: "/orders/{id}"}},
	}

	// Inline mode: no dispatcher, or dispatcher nil
	h, err := NewHandler(store, flow.New(), Deps{
		DispatchConfig: &config.DispatchConfig{Mode: config.DispatchInline},
		// Dispatcher is nil - inline mode
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders/42", nil))

	// Should succeed via interpreter (200)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestGenericFlowHandler_GatewayMode verifies that when dispatch mode is gateway,
// the dispatcher is called instead of the interpreter.
func TestGenericFlowHandler_GatewayMode(t *testing.T) {
	// Create a minimal trigger->response flow with a group assignment
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)}
	trig := flow.Node{
		ID:       "t",
		Type:     flow.TypeTrigger,
		Spec:     json.RawMessage(`{"method":"GET","path":"/orders/{id}","input":{"params":["id"]}}`),
		Children: []flow.Node{resp},
	}
	store := fakeStore{
		fv: config.FlowVersion{
			FlowID:  "f",
			Version: 1,
			Method:  "GET",
			Path:    "/orders/{id}",
			Tree:    trig,
			Group:   "order-group",
		},
		routes: []config.RouteInfo{{FlowID: "f", Method: "GET", Path: "/orders/{id}"}},
	}

	// Mock dispatcher
	dispatcher := &mockDispatcher{
		dispatchFn: func(ctx context.Context, flowID string, group string, input map[string]any) (*worker.ExecuteResponse, error) {
			// Verify the dispatcher receives correct params
			if flowID != "f" {
				t.Errorf("dispatch flowID = %s want f", flowID)
			}
			if group != "order-group" {
				t.Errorf("dispatch group = %s want order-group", group)
			}
			if input["id"] != "42" {
				t.Errorf("dispatch input.id = %v want 42", input["id"])
			}
			return &worker.ExecuteResponse{
				Status:   http.StatusOK,
				Response: map[string]any{"result": "from-worker"},
			}, nil
		},
	}

	h, err := NewHandler(store, flow.New(), Deps{
		DispatchConfig: &config.DispatchConfig{
			Mode:         config.DispatchGateway,
			DefaultGroup: "default",
		},
		Dispatcher: dispatcher,
		Log:        observ.NewLogger(nil),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders/42", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if body["result"] != "from-worker" {
		t.Errorf("response.result = %v want from-worker", body["result"])
	}
}

// TestGenericFlowHandler_GatewayMode_DefaultGroup verifies that flows without
// an explicit group use the DefaultGroup from config.
func TestGenericFlowHandler_GatewayMode_DefaultGroup(t *testing.T) {
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)}
	trig := flow.Node{
		ID:       "t",
		Type:     flow.TypeTrigger,
		Spec:     json.RawMessage(`{"method":"GET","path":"/test","input":{}}`),
		Children: []flow.Node{resp},
	}
	store := fakeStore{
		fv: config.FlowVersion{
			FlowID:  "f",
			Version: 1,
			Method:  "GET",
			Path:    "/test",
			Tree:    trig,
			Group:   "", // No group specified
		},
		routes: []config.RouteInfo{{FlowID: "f", Method: "GET", Path: "/test"}},
	}

	var receivedGroup string
	dispatcher := &mockDispatcher{
		dispatchFn: func(ctx context.Context, flowID string, group string, input map[string]any) (*worker.ExecuteResponse, error) {
			receivedGroup = group
			return &worker.ExecuteResponse{Status: http.StatusOK}, nil
		},
	}

	h, err := NewHandler(store, flow.New(), Deps{
		DispatchConfig: &config.DispatchConfig{
			Mode:         config.DispatchGateway,
			DefaultGroup: "fallback-group",
		},
		Dispatcher: dispatcher,
		Log:        observ.NewLogger(nil),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))

	if receivedGroup != "fallback-group" {
		t.Errorf("dispatch group = %s want fallback-group", receivedGroup)
	}
}

// TestGenericFlowHandler_GatewayMode_WorkerNotFound verifies error handling when
// the dispatcher returns WorkerNotFound.
func TestGenericFlowHandler_GatewayMode_WorkerNotFound(t *testing.T) {
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)}
	trig := flow.Node{
		ID:       "t",
		Type:     flow.TypeTrigger,
		Spec:     json.RawMessage(`{"method":"GET","path":"/test","input":{}}`),
		Children: []flow.Node{resp},
	}
	store := fakeStore{
		fv: config.FlowVersion{
			FlowID:  "f",
			Version: 1,
			Method:  "GET",
			Path:    "/test",
			Tree:    trig,
			Group:   "nonexistent",
		},
		routes: []config.RouteInfo{{FlowID: "f", Method: "GET", Path: "/test"}},
	}

	dispatcher := &mockDispatcher{
		dispatchFn: func(ctx context.Context, flowID string, group string, input map[string]any) (*worker.ExecuteResponse, error) {
			return nil, &gateway.DispatchError{
				Code:    gateway.ErrCodeWorkerNotFound,
				Message: "no worker found for group nonexistent",
				Group:   "nonexistent",
			}
		},
	}

	h, err := NewHandler(store, flow.New(), Deps{
		DispatchConfig: &config.DispatchConfig{Mode: config.DispatchGateway},
		Dispatcher:     dispatcher,
		Log:            observ.NewLogger(nil),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))

	// Worker not found should return 503 Service Unavailable
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d want 503 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestInternalExecute_Success verifies the internal execute endpoint routes
// cross-group calls correctly.
func TestInternalExecute_Success(t *testing.T) {
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)}
	trig := flow.Node{
		ID:       "t",
		Type:     flow.TypeTrigger,
		Spec:     json.RawMessage(`{"method":"GET","path":"/cross-group","input":{}}`),
		Children: []flow.Node{resp},
	}
	store := fakeStore{
		fv: config.FlowVersion{
			FlowID:  "target-flow",
			Version: 1,
			Method:  "GET",
			Path:    "/cross-group",
			Tree:    trig,
			Group:   "target-group",
		},
		routes: []config.RouteInfo{{FlowID: "target-flow", Method: "GET", Path: "/cross-group"}},
	}

	dispatcher := &mockDispatcher{
		dispatchFn: func(ctx context.Context, flowID string, group string, input map[string]any) (*worker.ExecuteResponse, error) {
			if flowID != "target-flow" {
				t.Errorf("dispatch flowID = %s want target-flow", flowID)
			}
			if group != "target-group" {
				t.Errorf("dispatch group = %s want target-group", group)
			}
			return &worker.ExecuteResponse{
				Status:   http.StatusOK,
				Response: map[string]any{"cross": "group-result"},
			}, nil
		},
	}

	h, err := NewHandler(store, flow.New(), Deps{
		DispatchConfig: &config.DispatchConfig{
			Mode:         config.DispatchGateway,
			DefaultGroup: "default",
		},
		Dispatcher: dispatcher,
		Log:        observ.NewLogger(nil),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	// POST /internal/execute with ExecuteRequest body
	execReq := worker.ExecuteRequest{
		FlowID:    "target-flow",
		RequestID: "req-123",
		TraceID:   "trace-456",
		Input:     map[string]any{"key": "value"},
	}
	body, _ := json.Marshal(execReq)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/execute", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var respBody worker.ExecuteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if respBody.Response["cross"] != "group-result" {
		t.Errorf("response.cross = %v want group-result", respBody.Response["cross"])
	}
}

// TestInternalExecute_FlowNotFound verifies that a request for a non-existent flow
// returns an appropriate error.
func TestInternalExecute_FlowNotFound(t *testing.T) {
	store := fakeStore{
		routes: []config.RouteInfo{}, // No routes
	}

	dispatcher := &mockDispatcher{
		dispatchFn: func(ctx context.Context, flowID string, group string, input map[string]any) (*worker.ExecuteResponse, error) {
			t.Fatal("dispatcher should not be called for non-existent flow")
			return nil, nil
		},
	}

	h, err := NewHandler(store, flow.New(), Deps{
		DispatchConfig: &config.DispatchConfig{Mode: config.DispatchGateway},
		Dispatcher:     dispatcher,
		Log:            observ.NewLogger(nil),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	execReq := worker.ExecuteRequest{
		FlowID:    "nonexistent",
		RequestID: "req-123",
	}
	body, _ := json.Marshal(execReq)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/execute", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d want 404 (body: %s)", rec.Code, rec.Body.String())
	}

	var respBody worker.ExecuteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if respBody.Error == nil {
		t.Fatal("expected error in response")
	}
	if respBody.Error.Code != worker.ErrCodeFlowNotFound {
		t.Errorf("error code = %s want %s", respBody.Error.Code, worker.ErrCodeFlowNotFound)
	}
}

// TestInternalExecute_InvalidBody verifies that invalid request body returns 400.
func TestInternalExecute_InvalidBody(t *testing.T) {
	store := fakeStore{routes: []config.RouteInfo{}}

	h, err := NewHandler(store, flow.New(), Deps{
		DispatchConfig: &config.DispatchConfig{Mode: config.DispatchGateway},
		Dispatcher:     &mockDispatcher{},
		Log:            observ.NewLogger(nil),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/execute", strings.NewReader("invalid json"))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestStatusForDispatchError verifies dispatch error classification to HTTP status.
func TestStatusForDispatchError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "worker not found",
			err:        &gateway.DispatchError{Code: gateway.ErrCodeWorkerNotFound},
			wantStatus: http.StatusServiceUnavailable,
			wantMsg:    "worker not found",
		},
		{
			name:       "worker not ready",
			err:        &gateway.DispatchError{Code: gateway.ErrCodeWorkerNotReady},
			wantStatus: http.StatusServiceUnavailable,
			wantMsg:    "worker not ready",
		},
		{
			name:       "breaker open",
			err:        &gateway.DispatchError{Code: gateway.ErrCodeBreakerOpen},
			wantStatus: http.StatusServiceUnavailable,
			wantMsg:    "circuit breaker open",
		},
		{
			name:       "timeout",
			err:        &gateway.DispatchError{Code: gateway.ErrCodeTimeout},
			wantStatus: http.StatusGatewayTimeout,
			wantMsg:    "worker timeout",
		},
		{
			name:       "upstream error",
			err:        &gateway.DispatchError{Code: gateway.ErrCodeUpstream},
			wantStatus: http.StatusBadGateway,
			wantMsg:    "worker error",
		},
		{
			name:       "validation error",
			err:        &gateway.DispatchError{Code: gateway.ErrCodeValidation},
			wantStatus: http.StatusBadRequest,
			wantMsg:    "validation error",
		},
		{
			name:       "not found",
			err:        &gateway.DispatchError{Code: gateway.ErrCodeNotFound},
			wantStatus: http.StatusNotFound,
			wantMsg:    "not found",
		},
		{
			name:       "generic error",
			err:        errors.New("something went wrong"),
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "internal error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, msg := statusForDispatchError(tc.err)
			if status != tc.wantStatus {
				t.Errorf("status = %d want %d", status, tc.wantStatus)
			}
			if msg != tc.wantMsg {
				t.Errorf("msg = %s want %s", msg, tc.wantMsg)
			}
		})
	}
}

// mockDispatcher implements the Dispatch interface for testing.
type mockDispatcher struct {
	dispatchFn func(ctx context.Context, flowID string, group string, input map[string]any) (*worker.ExecuteResponse, error)
}

func (m *mockDispatcher) Dispatch(ctx context.Context, flowID string, group string, input map[string]any) (*worker.ExecuteResponse, error) {
	if m.dispatchFn != nil {
		return m.dispatchFn(ctx, flowID, group, input)
	}
	return &worker.ExecuteResponse{Status: http.StatusOK}, nil
}

// Compile-time check that mockDispatcher satisfies the expected interface.
var _ interface {
	Dispatch(context.Context, string, string, map[string]any) (*worker.ExecuteResponse, error)
} = (*mockDispatcher)(nil)
