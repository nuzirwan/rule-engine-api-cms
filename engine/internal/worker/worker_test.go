package worker

import (
	"context"
	"encoding/json"
	"testing"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/flow"
)

// mockWorkerStore implements config.WorkerStore for testing.
type mockWorkerStore struct {
	group       config.Group
	groupErr    error
	version     int
	versionErr  error
	flows       []config.FlowVersion
	flowsErr    error
	connections []connect.ConnectionDef
	connsErr    error
	jdms        map[string][]byte
	jdmsErr     error
}

func (m *mockWorkerStore) GetGroup(ctx context.Context, env, groupID string) (config.Group, error) {
	return m.group, m.groupErr
}

func (m *mockWorkerStore) GetGroupVersion(ctx context.Context, env, groupID string) (int, error) {
	return m.version, m.versionErr
}

func (m *mockWorkerStore) GetActiveFlowsForGroup(ctx context.Context, env, groupID string) ([]config.FlowVersion, error) {
	return m.flows, m.flowsErr
}

func (m *mockWorkerStore) GetConnectionsForGroup(ctx context.Context, env string, keys []string) ([]connect.ConnectionDef, error) {
	return m.connections, m.connsErr
}

func (m *mockWorkerStore) GetJDMsForFlows(ctx context.Context, env string, flows []config.FlowVersion) (map[string][]byte, error) {
	return m.jdms, m.jdmsErr
}

func (m *mockWorkerStore) GetJDM(ctx context.Context, env, id string) ([]byte, int, error) {
	if b, ok := m.jdms[id]; ok {
		return b, 0, nil
	}
	return nil, 0, nil
}

func (m *mockWorkerStore) Ping(ctx context.Context) error {
	return nil
}

func TestWorker_New(t *testing.T) {
	store := &mockWorkerStore{}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   store,
	})

	if w.GroupID() != "test-group" {
		t.Errorf("GroupID = %q, want %q", w.GroupID(), "test-group")
	}
	if w.Ready() {
		t.Error("Ready() = true, want false before LoadGroup")
	}
	if w.LoadedVersion() != 0 {
		t.Errorf("LoadedVersion() = %d, want 0 before LoadGroup", w.LoadedVersion())
	}
}

func TestWorker_DebugConfig_NotLoaded(t *testing.T) {
	store := &mockWorkerStore{}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   store,
	})

	cfg := w.DebugConfig()
	if cfg.GroupID != "test-group" {
		t.Errorf("DebugConfig.GroupID = %q, want %q", cfg.GroupID, "test-group")
	}
	if cfg.Ready {
		t.Error("DebugConfig.Ready = true, want false before LoadGroup")
	}
	if cfg.GroupVersion != 0 {
		t.Errorf("DebugConfig.GroupVersion = %d, want 0", cfg.GroupVersion)
	}
}

func TestWorker_Execute_NotLoaded(t *testing.T) {
	store := &mockWorkerStore{}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   store,
	})

	_, err := w.Execute(context.Background(), "some-flow", "req-1", "trace-1", nil)
	if err == nil {
		t.Error("Execute before LoadGroup should fail")
	}
	if err.Error() != "worker not loaded" {
		t.Errorf("Execute error = %q, want %q", err.Error(), "worker not loaded")
	}
}

func TestWorker_Close_NotLoaded(t *testing.T) {
	store := &mockWorkerStore{}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   store,
	})

	// Close on unloaded worker should not panic.
	if err := w.Close(); err != nil {
		t.Errorf("Close on unloaded worker = %v, want nil", err)
	}
}

// minimalTriggerTree returns a minimal flow tree with just a trigger and response.
func minimalTriggerTree(t *testing.T) flow.Node {
	t.Helper()
	triggerSpec := struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	}{Method: "GET", Path: "/test"}
	triggerRaw, _ := json.Marshal(triggerSpec)

	responseSpec := struct {
		SetPath string `json:"setPath"`
		Value   any    `json:"value"`
	}{SetPath: "message", Value: "ok"}
	responseRaw, _ := json.Marshal(responseSpec)

	return flow.Node{
		ID:   "trigger",
		Type: flow.TypeTrigger,
		Spec: triggerRaw,
		Children: []flow.Node{
			{
				ID:   "response",
				Type: flow.TypeResponse,
				Spec: responseRaw,
			},
		},
	}
}

func TestPreloadedJDMLoader(t *testing.T) {
	jdms := map[string][]byte{
		"test-jdm": []byte(`{"nodes": []}`),
	}
	loader := &preloadedJDMLoader{jdms: jdms}

	// Test found JDM.
	b, version, err := loader.LoadJDM(context.Background(), "", "test-jdm")
	if err != nil {
		t.Errorf("LoadJDM error = %v, want nil", err)
	}
	if string(b) != `{"nodes": []}` {
		t.Errorf("LoadJDM bytes = %q, want %q", string(b), `{"nodes": []}`)
	}
	if version != 0 {
		t.Errorf("LoadJDM version = %d, want 0", version)
	}

	// Test not found JDM.
	_, _, err = loader.LoadJDM(context.Background(), "", "missing-jdm")
	if err == nil {
		t.Error("LoadJDM for missing JDM should fail")
	}
}
