package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"nzr-rules-engine/internal/config"
)

// mockVersionStore implements the version-checking interface for Reloader tests.
type mockVersionStore struct {
	mu       sync.Mutex
	versions map[string]int
	err      error
}

func newMockVersionStore() *mockVersionStore {
	return &mockVersionStore{
		versions: make(map[string]int),
	}
}

func (m *mockVersionStore) GetGroupVersion(ctx context.Context, env, groupID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return 0, m.err
	}
	return m.versions[groupID], nil
}

func (m *mockVersionStore) setVersion(groupID string, version int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.versions[groupID] = version
}

func (m *mockVersionStore) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func TestReloader_PollInterval(t *testing.T) {
	cfg := ReloadConfig{}
	store := newMockVersionStore()

	// Create a minimal worker that won't actually load.
	workerStore := &mockWorkerStore{}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   workerStore,
	})

	r := NewReloader(w, store, "", nil, cfg)

	// Default poll interval should be 30s.
	if r.cfg.PollInterval != 30*time.Second {
		t.Errorf("default poll interval = %v, want 30s", r.cfg.PollInterval)
	}

	// Custom poll interval.
	cfg.PollInterval = 10 * time.Second
	r = NewReloader(w, store, "", nil, cfg)
	if r.cfg.PollInterval != 10*time.Second {
		t.Errorf("custom poll interval = %v, want 10s", r.cfg.PollInterval)
	}
}

func TestReloader_StartStop(t *testing.T) {
	store := newMockVersionStore()
	workerStore := &mockWorkerStore{}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   workerStore,
	})

	cfg := ReloadConfig{
		PollInterval: 100 * time.Millisecond,
	}
	r := NewReloader(w, store, "", nil, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.Start(ctx)

	// Give the goroutine time to start.
	time.Sleep(50 * time.Millisecond)

	// Stop should complete without hanging.
	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success.
	case <-time.After(time.Second):
		t.Error("Stop timed out")
	}
}

func TestReloader_CheckAndReload_NoChange(t *testing.T) {
	store := newMockVersionStore()
	store.setVersion("test-group", 1)

	workerStore := &mockWorkerStore{
		group: config.Group{
			ID:      "test-group",
			Name:    "Test Group",
			Version: 1,
		},
		version: 1,
	}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   workerStore,
	})

	// Manually set the loaded version to simulate a loaded worker.
	w.mu.Lock()
	w.state = &loadedState{groupVersion: 1}
	w.mu.Unlock()

	reloadCount := 0
	cfg := ReloadConfig{
		PollInterval: 100 * time.Millisecond,
		OnReload: func(v int) {
			reloadCount++
		},
	}
	r := NewReloader(w, store, "", nil, cfg)

	// Check and reload should not trigger a reload.
	r.checkAndReload(context.Background())

	if reloadCount != 0 {
		t.Errorf("reload count = %d, want 0 (no version change)", reloadCount)
	}
}

func TestReloader_CheckAndReload_VersionError(t *testing.T) {
	store := newMockVersionStore()
	store.setError(errors.New("connection failed"))

	workerStore := &mockWorkerStore{}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   workerStore,
	})

	// Manually set the loaded version to simulate a loaded worker.
	w.mu.Lock()
	w.state = &loadedState{groupVersion: 1}
	w.mu.Unlock()

	reloadCount := 0
	errorCount := 0
	cfg := ReloadConfig{
		PollInterval: 100 * time.Millisecond,
		OnReload: func(v int) {
			reloadCount++
		},
		OnError: func(err error) {
			errorCount++
		},
	}
	r := NewReloader(w, store, "", nil, cfg)

	// Check and reload should not trigger a reload or error callback (version check error is logged but not callback).
	r.checkAndReload(context.Background())

	if reloadCount != 0 {
		t.Errorf("reload count = %d, want 0 (version check error)", reloadCount)
	}
	// OnError is only called for reload errors, not version check errors.
	if errorCount != 0 {
		t.Errorf("error count = %d, want 0", errorCount)
	}
}

func TestReloader_CheckAndReload_NotYetLoaded(t *testing.T) {
	store := newMockVersionStore()
	store.setVersion("test-group", 5)

	workerStore := &mockWorkerStore{}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   workerStore,
	})

	// Worker is not loaded (state is nil, LoadedVersion returns 0).

	reloadCount := 0
	cfg := ReloadConfig{
		PollInterval: 100 * time.Millisecond,
		OnReload: func(v int) {
			reloadCount++
		},
	}
	r := NewReloader(w, store, "", nil, cfg)

	// Check and reload should skip when worker is not yet loaded.
	r.checkAndReload(context.Background())

	if reloadCount != 0 {
		t.Errorf("reload count = %d, want 0 (worker not loaded)", reloadCount)
	}
}

func TestReloader_ContextCancellation(t *testing.T) {
	store := newMockVersionStore()
	workerStore := &mockWorkerStore{}
	w := New(Config{
		GroupID: "test-group",
		Env:     "",
		Store:   workerStore,
	})

	cfg := ReloadConfig{
		PollInterval: 1 * time.Hour, // Long interval, won't trigger during test.
	}
	r := NewReloader(w, store, "", nil, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)

	// Give the goroutine time to start.
	time.Sleep(50 * time.Millisecond)

	// Cancel the context.
	cancel()

	// Wait for the goroutine to exit via doneCh.
	select {
	case <-r.doneCh:
		// Success.
	case <-time.After(time.Second):
		t.Error("goroutine did not exit on context cancellation")
	}
}
