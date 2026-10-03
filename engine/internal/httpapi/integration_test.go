//go:build integration && cgo

package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/connect/drivers"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/httpapi"
	"nzr-rules-engine/internal/observ"
)

// TestOrdersEndToEnd is the real-HTTP equivalent of the spike's AC-S3: one
// nested flow served through the REAL httpapi handler (stdlib ServeMux routing +
// Ctx construction), driving the REAL connect.Registry (ephemeral postgres:16)
// and the REAL decision.Engine (ZEN via the CGO binding) over a REAL in-process
// httptest REST stub. Two order ids take DIFFERENT ZEN branches; we assert the
// stitched JSON response AND that exactly the matching REST endpoint was hit.
//
// Flow (from the committed seed, >=2 nesting depth):
//
//	Trigger -> Action(pg read) -> Condition(ZEN over {amount,status})
//	        -> { expedited: Action(POST /expedite) ; standard: Action(POST /standard) }
//	        -> Set(shipping) -> Response
func TestOrdersEndToEnd(t *testing.T) {
	ctx := context.Background()

	// --- real ephemeral Postgres, seeded with one row per branch ---
	dsn, cleanup := startPostgres(t)
	defer cleanup()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx connect: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `CREATE TABLE orders (id INT PRIMARY KEY, amount INT NOT NULL, status TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO orders (id, amount, status) VALUES (1, 1500, 'paid'), (2, 500, 'paid')`); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	// --- real in-process REST stub: records hits and returns {shipping} so the
	// flow's Set(from "decision.body.shipping") stitches the response. The
	// shipping value is derived from the endpoint the flow chose. ---
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		shipping := "standard"
		if r.URL.Path == "/expedite" {
			shipping = "expedited"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "shipping": shipping})
	}))
	defer srv.Close()

	// --- config store from the committed seed; override the two connection
	// endpoints to point at the ephemeral postgres + the REST stub. ---
	seedPath := filepath.Join("..", "config", "testdata", "seed.json")
	raw, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	store, err := config.SeedFromBytes(raw)
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	defs, err := store.Connections(ctx, "")
	if err != nil {
		t.Fatalf("connections: %v", err)
	}
	defs = overrideEndpoints(t, defs, dsn, srv.URL)

	// --- real registry over the real drivers + real decision engine ---
	tracer := observ.NewTracer(nil)
	logger := observ.NewLogger(nil)
	registry, err := connect.New(drivers.All(), defs, connect.NewEnvSecretProvider(), tracer, logger)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	defer registry.Close()

	engine := decision.New(jdmLoader{store: store})
	defer engine.Close()

	// --- the REAL httpapi handler over the real seams ---
	handler, err := httpapi.NewHandler(store, flow.New(), httpapi.Deps{
		Conns:   registry,
		Decide:  engine,
		Trace:   tracer,
		Log:     logger,
		Store:   store,
		Metrics: prometheus.NewRegistry(),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	cases := []struct {
		name         string
		id           string
		wantShipping string
		wantPath     string
	}{
		{"paid high-value order -> expedited", "1", "expedited", "/expedite"},
		{"paid low-value order -> standard", "2", "standard", "/standard"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits = nil

			req := httptest.NewRequest(http.MethodGet, "/orders/"+tc.id, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d want 200 (body: %s)", rec.Code, rec.Body.String())
			}
			var body map[string]any
			if derr := json.Unmarshal(rec.Body.Bytes(), &body); derr != nil {
				t.Fatalf("decode response: %v (raw: %s)", derr, rec.Body.String())
			}
			if got := body["shipping"]; got != tc.wantShipping {
				t.Errorf("response shipping = %v want %v (full: %v)", got, tc.wantShipping, body)
			}
			if len(hits) != 1 || hits[0] != tc.wantPath {
				t.Errorf("REST hits = %v want exactly [%s]", hits, tc.wantPath)
			}
		})
	}
}

// jdmLoader adapts config.Store to decision.JDMLoader (the same adapter
// cmd/engine wires) so the real engine resolves the seeded order JDM.
type jdmLoader struct {
	store config.Store
}

// LoadJDM implements decision.JDMLoader.
func (l jdmLoader) LoadJDM(ctx context.Context, env, jdmID string) ([]byte, int, error) {
	return l.store.GetJDM(ctx, env, jdmID)
}

// overrideEndpoints points the seeded 'orders-pg' DSN at the ephemeral postgres
// and the 'ship-rest' baseURL at the httptest stub, leaving the rest of each def
// (type, resilience) intact.
func overrideEndpoints(t *testing.T, defs []connect.ConnectionDef, dsn, restURL string) []connect.ConnectionDef {
	t.Helper()
	for i := range defs {
		switch defs[i].Key {
		case "orders-pg":
			defs[i].Settings = cloneSettings(defs[i].Settings)
			defs[i].Settings["dsn"] = dsn
		case "ship-rest":
			defs[i].Settings = cloneSettings(defs[i].Settings)
			defs[i].Settings["baseURL"] = restURL
		}
	}
	return defs
}

// cloneSettings copies a settings map so the override does not mutate the store's
// retained def.
func cloneSettings(s map[string]any) map[string]any {
	out := make(map[string]any, len(s)+1)
	for k, v := range s {
		out[k] = v
	}
	return out
}

// startPostgres starts an ephemeral postgres:16 container on a random host port,
// waits until it accepts connections, and returns a DSN plus a cleanup func. It
// is the PROVEN harness copied from internal/spikeflow/interpreter_integration_test.go.
func startPostgres(t *testing.T) (dsn string, cleanup func()) {
	t.Helper()
	const (
		image = "postgres:16"
		pass  = "spikepw"
		db    = "spikedb"
	)
	// -P publishes the container's 5432 to a random free host port.
	out, err := exec.Command("docker", "run", "-d",
		"-e", "POSTGRES_PASSWORD="+pass,
		"-e", "POSTGRES_DB="+db,
		"-P", image,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	id := strings.TrimSpace(string(out))
	cleanup = func() {
		_ = exec.Command("docker", "rm", "-f", id).Run()
	}

	// Resolve the mapped host port for 5432/tcp via inspect; poll briefly because
	// a -P binding can lag container start.
	hostPort := ""
	for i := 0; i < 40; i++ {
		insp, ierr := exec.Command("docker", "inspect",
			"-f", `{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}`, id,
		).CombinedOutput()
		p := strings.TrimSpace(string(insp))
		if ierr == nil && p != "" && !strings.Contains(p, "no value") {
			hostPort = p
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if hostPort == "" {
		cleanup()
		t.Fatalf("could not resolve mapped host port for 5432/tcp")
	}
	dsn = fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%s/%s?sslmode=disable", pass, hostPort, db)

	// Poll until the server accepts a real connection (up to ~60s).
	pollCtx := context.Background()
	deadline := time.Now().Add(60 * time.Second)
	for {
		c, cerr := pgx.Connect(pollCtx, dsn)
		if cerr == nil {
			if pingErr := c.Ping(pollCtx); pingErr == nil {
				_ = c.Close(pollCtx)
				return dsn, cleanup
			}
			_ = c.Close(pollCtx)
		}
		if time.Now().After(deadline) {
			cleanup()
			t.Fatalf("postgres not ready within deadline: %v", cerr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
