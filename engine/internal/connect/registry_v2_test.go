package connect

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegistryV2_NewOpensZeroConnections(t *testing.T) {
	conn := &mockConnector{typ: "test", lifecycle: LifecyclePooled}
	defs := []ConnectionDef{
		{Key: "key1", Type: "test"},
		{Key: "key2", Type: "test"},
		{Key: "key3", Type: "test"},
	}

	r, err := newRegistryV2([]Connector{conn}, defs, nil, nil, nil)
	if err != nil {
		t.Fatalf("newRegistryV2 failed: %v", err)
	}
	defer r.Close()

	// No connections should be opened at construction time (AC-G2)
	if conn.openCount.Load() != 0 {
		t.Errorf("newRegistryV2 should open 0 connections, got %d", conn.openCount.Load())
	}
}

func TestRegistryV2_ClientOpensLazily(t *testing.T) {
	conn := &mockConnector{typ: "test", lifecycle: LifecyclePooled}
	defs := []ConnectionDef{{Key: "test-key", Type: "test"}}

	r, err := newRegistryV2([]Connector{conn}, defs, nil, nil, nil)
	if err != nil {
		t.Fatalf("newRegistryV2 failed: %v", err)
	}
	defer r.Close()

	// Before Client call: no opens
	if conn.openCount.Load() != 0 {
		t.Errorf("expected 0 opens before Client(), got %d", conn.openCount.Load())
	}

	// First Client call opens the connection
	client, err := r.Client(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("Client failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected client, got nil")
	}
	if conn.openCount.Load() != 1 {
		t.Errorf("expected 1 open after first Client(), got %d", conn.openCount.Load())
	}

	// Second Client call reuses (no new open)
	client2, err := r.Client(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("second Client failed: %v", err)
	}
	if client2 != client {
		t.Error("expected same client instance on reuse")
	}
	if conn.openCount.Load() != 1 {
		t.Errorf("expected still 1 open after reuse, got %d", conn.openCount.Load())
	}
}

func TestRegistryV2_ReloadEvictsChangedKeys(t *testing.T) {
	conn := &mockConnector{typ: "test", lifecycle: LifecyclePooled}
	defs := []ConnectionDef{{Key: "test-key", Type: "test", Settings: map[string]any{"host": "old"}}}

	r, err := newRegistryV2([]Connector{conn}, defs, nil, nil, nil)
	if err != nil {
		t.Fatalf("newRegistryV2 failed: %v", err)
	}
	defer r.Close()

	// Open the connection
	_, err = r.Client(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("Client failed: %v", err)
	}
	if conn.openCount.Load() != 1 {
		t.Errorf("expected 1 open, got %d", conn.openCount.Load())
	}

	// Reload with changed def
	newDefs := []ConnectionDef{{Key: "test-key", Type: "test", Settings: map[string]any{"host": "new"}}}
	err = r.Reload(context.Background(), newDefs)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// Give time for async close
	time.Sleep(20 * time.Millisecond)

	// Next Client call should reopen
	_, err = r.Client(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("Client after Reload failed: %v", err)
	}
	if conn.openCount.Load() != 2 {
		t.Errorf("expected 2 opens after Reload, got %d", conn.openCount.Load())
	}
}

func TestRegistryV2_ReloadKeepsUnchangedKeys(t *testing.T) {
	conn := &mockConnector{typ: "test", lifecycle: LifecyclePooled}
	defs := []ConnectionDef{{Key: "test-key", Type: "test", Settings: map[string]any{"host": "same"}}}

	r, err := newRegistryV2([]Connector{conn}, defs, nil, nil, nil)
	if err != nil {
		t.Fatalf("newRegistryV2 failed: %v", err)
	}
	defer r.Close()

	// Open the connection
	_, err = r.Client(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("Client failed: %v", err)
	}
	if conn.openCount.Load() != 1 {
		t.Errorf("expected 1 open, got %d", conn.openCount.Load())
	}

	// Reload with same def (unchanged)
	newDefs := []ConnectionDef{{Key: "test-key", Type: "test", Settings: map[string]any{"host": "same"}}}
	err = r.Reload(context.Background(), newDefs)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// Next Client call should NOT reopen
	_, err = r.Client(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("Client after Reload failed: %v", err)
	}
	if conn.openCount.Load() != 1 {
		t.Errorf("expected still 1 open (unchanged), got %d", conn.openCount.Load())
	}
}

func TestRegistryV2_HealthCheckNoOpenConnections(t *testing.T) {
	conn := &mockConnector{typ: "test", lifecycle: LifecyclePooled}
	defs := []ConnectionDef{{Key: "test-key", Type: "test"}}

	r, err := newRegistryV2([]Connector{conn}, defs, nil, nil, nil)
	if err != nil {
		t.Fatalf("newRegistryV2 failed: %v", err)
	}
	defer r.Close()

	// HealthCheck with no open connections should pass
	err = r.HealthCheck(context.Background())
	if err != nil {
		t.Errorf("HealthCheck with no open connections should pass, got: %v", err)
	}

	// No connections should have been opened
	if conn.openCount.Load() != 0 {
		t.Errorf("HealthCheck should not open connections, got %d opens", conn.openCount.Load())
	}
}

func TestRegistryV2_HealthCheckProbesOpenConnections(t *testing.T) {
	conn := &mockConnector{typ: "test", lifecycle: LifecyclePooled}
	defs := []ConnectionDef{{Key: "test-key", Type: "test"}}

	r, err := newRegistryV2([]Connector{conn}, defs, nil, nil, nil)
	if err != nil {
		t.Fatalf("newRegistryV2 failed: %v", err)
	}
	defer r.Close()

	// Open a connection first
	_, err = r.Client(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("Client failed: %v", err)
	}

	// HealthCheck should probe the open connection
	err = r.HealthCheck(context.Background())
	if err != nil {
		t.Errorf("HealthCheck should pass, got: %v", err)
	}
}

func TestRegistryV2_UnknownKeyReturnsError(t *testing.T) {
	conn := &mockConnector{typ: "test", lifecycle: LifecyclePooled}
	defs := []ConnectionDef{{Key: "test-key", Type: "test"}}

	r, err := newRegistryV2([]Connector{conn}, defs, nil, nil, nil)
	if err != nil {
		t.Fatalf("newRegistryV2 failed: %v", err)
	}
	defer r.Close()

	_, err = r.Client(context.Background(), "unknown-key")
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
	var connErr *ConnError
	if !errors.As(err, &connErr) {
		t.Fatalf("expected ConnError, got %T", err)
	}
	if connErr.Class != Validation {
		t.Errorf("expected Validation class, got %v", connErr.Class)
	}
}

func TestRegistryV2_GracefulStartup_DeadBackendDoesNotFailBoot(t *testing.T) {
	// AC-G5: a dead backend def does NOT fail boot
	goodConn := &mockConnector{typ: "good", lifecycle: LifecyclePooled}
	badConn := &mockConnector{typ: "bad", lifecycle: LifecyclePooled, openErr: errors.New("connection failed")}
	defs := []ConnectionDef{
		{Key: "good-key", Type: "good"},
		{Key: "bad-key", Type: "bad"},
	}

	// newRegistryV2 should succeed even with a bad connector defined
	r, err := newRegistryV2([]Connector{goodConn, badConn}, defs, nil, nil, nil)
	if err != nil {
		t.Fatalf("newRegistryV2 should not fail with dead backend def: %v", err)
	}
	defer r.Close()

	// Good key should work
	_, err = r.Client(context.Background(), "good-key")
	if err != nil {
		t.Fatalf("good-key should work: %v", err)
	}

	// Bad key should fail on first use (not at boot)
	_, err = r.Client(context.Background(), "bad-key")
	if err == nil {
		t.Fatal("bad-key should fail on use")
	}

	// Good key should still work after bad key failure
	_, err = r.Client(context.Background(), "good-key")
	if err != nil {
		t.Fatalf("good-key should still work: %v", err)
	}
}

func TestRegistryV2_MultipleDefsMultipleTypes(t *testing.T) {
	pgConn := &mockConnector{typ: "postgres", lifecycle: LifecyclePooled, capabilities: CapQueryExec}
	restConn := &mockConnector{typ: "rest", lifecycle: LifecycleEphemeral, capabilities: CapQueryExec}
	vkConn := &mockConnector{typ: "valkey", lifecycle: LifecyclePooled, capabilities: CapKeyValue | CapDedupStore}

	defs := []ConnectionDef{
		{Key: "orders-db", Type: "postgres"},
		{Key: "users-api", Type: "rest"},
		{Key: "cache", Type: "valkey"},
	}

	r, err := newRegistryV2([]Connector{pgConn, restConn, vkConn}, defs, nil, nil, nil)
	if err != nil {
		t.Fatalf("newRegistryV2 failed: %v", err)
	}
	defer r.Close()

	// No opens at construction
	if pgConn.openCount.Load() != 0 || restConn.openCount.Load() != 0 || vkConn.openCount.Load() != 0 {
		t.Error("expected 0 opens at construction")
	}

	// Open each
	_, err = r.Client(context.Background(), "orders-db")
	if err != nil {
		t.Fatalf("orders-db failed: %v", err)
	}
	_, err = r.Client(context.Background(), "users-api")
	if err != nil {
		t.Fatalf("users-api failed: %v", err)
	}
	_, err = r.Client(context.Background(), "cache")
	if err != nil {
		t.Fatalf("cache failed: %v", err)
	}

	// Each should have opened exactly once
	if pgConn.openCount.Load() != 1 {
		t.Errorf("expected 1 postgres open, got %d", pgConn.openCount.Load())
	}
	if restConn.openCount.Load() != 1 {
		t.Errorf("expected 1 rest open, got %d", restConn.openCount.Load())
	}
	if vkConn.openCount.Load() != 1 {
		t.Errorf("expected 1 valkey open, got %d", vkConn.openCount.Load())
	}
}
