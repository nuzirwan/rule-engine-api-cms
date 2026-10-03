package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/observ"
)

// fakeRegistry is a connect.Registry whose HealthCheck returns a canned error.
// Client/Reload are unused by the ops endpoints.
type fakeRegistry struct {
	healthErr error
}

func (f fakeRegistry) Client(ctx context.Context, key string) (connect.Client, error) {
	return nil, errors.New("not used")
}
func (f fakeRegistry) Reload(ctx context.Context, defs []connect.ConnectionDef) error { return nil }
func (f fakeRegistry) HealthCheck(ctx context.Context) error                          { return f.healthErr }

var _ connect.Registry = fakeRegistry{}

// fakePinger stands in for the config store's Ping.
type fakePinger struct {
	err error
}

func (p fakePinger) Ping(ctx context.Context) error { return p.err }

// mountOps builds an ops handler on a fresh mux for a test.
func mountOps(t *testing.T, deps Deps) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	newOps(deps).mount(mux)
	return mux
}

func TestLivez(t *testing.T) {
	h := mountOps(t, Deps{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("livez status = %d want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("livez body = %q want %q", rec.Body.String(), "ok")
	}
}

func TestReadyzHealthy(t *testing.T) {
	h := mountOps(t, Deps{Conns: fakeRegistry{}, Store: fakePinger{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode readyz body: %v", err)
	}
	if body["status"] != "ready" {
		t.Fatalf("readyz body = %v want status:ready", body)
	}
}

func TestReadyzRegistryUnhealthy(t *testing.T) {
	h := mountOps(t, Deps{
		Conns: fakeRegistry{healthErr: errors.New("pool down")},
		Store: fakePinger{},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d want 503", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode readyz body: %v", err)
	}
	if body["notReady"] != "registry" {
		t.Fatalf("readyz body = %v want notReady:registry", body)
	}
}

func TestReadyzStoreUnhealthy(t *testing.T) {
	h := mountOps(t, Deps{
		Conns: fakeRegistry{},
		Store: fakePinger{err: errors.New("pg down")},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d want 503", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode readyz body: %v", err)
	}
	if body["notReady"] != "config_store" {
		t.Fatalf("readyz body = %v want notReady:config_store", body)
	}
}

func TestMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := observ.NewMetrics(reg)
	// Record one flow observation so a known metric name appears in the scrape.
	m.ObserveFlow("f", "", "GET", "ok", 5*time.Millisecond)

	h := mountOps(t, Deps{Metrics: reg})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "nzr_flow_requests_total") {
		t.Fatalf("metrics body missing nzr_flow_requests_total:\n%s", rec.Body.String())
	}
}
