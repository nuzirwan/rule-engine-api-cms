//go:build integration && cgo

package spikeflow

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

	"nzr-rules-engine/internal/zenspike"
)

// TestNestedFlowEndToEnd proves AC-S3: one nested flow run SYNCHRONOUSLY end to
// end against a REAL ephemeral Postgres (Docker) and a REAL in-process REST stub,
// with the ZEN decision driven by the REAL CGO binding. Two inputs take DIFFERENT
// branches; we assert the stitched JSON response AND that the correct REST
// endpoint was hit for each.
//
// Flow shape (>=2 nesting depth):
//
//	Trigger -> Action(pg read) -> Condition(ZEN eval over {amount,status})
//	        -> { expedited: Action(POST /expedite) ; standard: Action(POST /standard) }
//	        -> Set(shipping) -> Response
func TestNestedFlowEndToEnd(t *testing.T) {
	ctx := context.Background()

	// --- real ephemeral Postgres, seeded with two rows (one per branch) ---
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
		t.Fatalf("seed: %v", err)
	}

	// --- real in-process REST stub recording which endpoint ran ---
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path": r.URL.Path})
	}))
	defer srv.Close()

	// --- real ZEN decision via the CGO binding ---
	eng := zenspike.NewEngine()
	defer eng.Dispose()
	jdm, err := os.ReadFile(filepath.Join("..", "zenspike", "testdata", "order.jdm.json"))
	if err != nil {
		t.Fatalf("read jdm: %v", err)
	}
	compiled, err := eng.Compile(jdm)
	if err != nil {
		t.Fatalf("compile jdm: %v", err)
	}
	defer compiled.Close()

	// Action: Postgres read for a given order id.
	pgRead := func(orderID int) ActionFunc {
		return func(ctx context.Context, c *Ctx) (map[string]any, error) {
			var amount int
			var status string
			row := conn.QueryRow(ctx, `SELECT amount, status FROM orders WHERE id=$1`, orderID)
			if err := row.Scan(&amount, &status); err != nil {
				return nil, fmt.Errorf("pg read order %d: %w", orderID, err)
			}
			return map[string]any{"amount": amount, "status": status}, nil
		}
	}
	// Action: REST POST to the stub at the given path.
	restCall := func(path string) ActionFunc {
		return func(ctx context.Context, c *Ctx) (map[string]any, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+path, strings.NewReader(`{}`))
			if err != nil {
				return nil, err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			var out map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return nil, err
			}
			return out, nil
		}
	}
	// Decision: project {amount,status} and evaluate the real JDM.
	decide := func(ctx context.Context, in map[string]any) (map[string]any, error) {
		return compiled.Eval(ctx, in)
	}

	build := func(orderID int) *Node {
		expedite := &Node{ID: "expedited", Type: TypeAction, Action: restCall("/expedite"), SaveAs: "rest",
			Children: []*Node{{ID: "set-e", Type: TypeSet, TargetPath: "shipping", FromPath: "decision.shipping",
				Children: []*Node{{ID: "resp-e", Type: TypeResponse}}}}}
		standard := &Node{ID: "standard", Type: TypeAction, Action: restCall("/standard"), SaveAs: "rest",
			Children: []*Node{{ID: "set-s", Type: TypeSet, TargetPath: "shipping", FromPath: "decision.shipping",
				Children: []*Node{{ID: "resp-s", Type: TypeResponse}}}}}
		cond := &Node{ID: "cond", Type: TypeCondition, Decide: decide,
			Inputs:    []string{"order.amount", "order.status"},
			BranchKey: "shipping", TrueKey: "expedited", FalseKey: "standard",
			Children: []*Node{expedite, standard}}
		pg := &Node{ID: "pg", Type: TypeAction, Action: pgRead(orderID), SaveAs: "order",
			Children: []*Node{cond}}
		return &Node{ID: "trigger", Type: TypeTrigger, Children: []*Node{pg}}
	}

	cases := []struct {
		name         string
		orderID      int
		wantShipping string
		wantPath     string
	}{
		{"paid high-value order -> expedited", 1, "expedited", "/expedite"},
		{"paid low-value order -> standard", 2, "standard", "/standard"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits = nil
			c := NewCtx(map[string]any{})
			if err := New().Run(ctx, build(tc.orderID), c); err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := c.Response["shipping"]; got != tc.wantShipping {
				t.Errorf("stitched response shipping = %v want %v (full: %v)", got, tc.wantShipping, c.Response)
			}
			if len(hits) != 1 || hits[0] != tc.wantPath {
				t.Errorf("REST hits = %v want exactly [%s]", hits, tc.wantPath)
			}
		})
	}
}

// startPostgres starts an ephemeral postgres:16 container on a random host port,
// waits until it accepts connections, and returns a DSN plus a cleanup func.
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

	// Resolve the mapped host port for 5432/tcp via inspect (reliable once the
	// container's port bindings are populated; poll briefly as -P binding can lag).
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
	ctx := context.Background()
	deadline := time.Now().Add(60 * time.Second)
	for {
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			if pingErr := conn.Ping(ctx); pingErr == nil {
				_ = conn.Close(ctx)
				return dsn, cleanup
			}
			_ = conn.Close(ctx)
		}
		if time.Now().After(deadline) {
			cleanup()
			t.Fatalf("postgres not ready within deadline: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
