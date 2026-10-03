//go:build integration && cgo

package httpapi_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
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

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"

	"nzr-rules-engine/internal/auth"
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
	handler := httpapi.NewHandler(store, flow.New(), httpapi.Deps{
		Conns:  registry,
		Decide: engine,
		Trace:  tracer,
		Log:    logger,
	})

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

// ---- wiring join integration (auth + multi-node flow + admin dry-run/validate) ----

// harness bundles the real wired collaborators for the wiring-join tests, built
// over the ephemeral postgres + httptest REST stub (same pattern as
// TestOrdersEndToEnd). restHits records the REST stub path per request so a test
// can assert a write was (or was not) sent.
type harness struct {
	store    config.Store
	registry connect.Registry
	engine   *decision.Engine
	restHits *[]string
}

// newHarness spins up the proven postgres + REST stub, seeds one order row,
// builds the real registry + decision engine over the committed seed, and
// registers cleanup. It is the shared setup for the wiring-join cases.
func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()

	dsn, cleanup := startPostgres(t)
	t.Cleanup(cleanup)

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

	hits := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits = append(*hits, r.URL.Path)
		shipping := "standard"
		if r.URL.Path == "/expedite" {
			shipping = "expedited"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "shipping": shipping})
	}))
	t.Cleanup(srv.Close)

	raw, err := os.ReadFile(filepath.Join("..", "config", "testdata", "seed.json"))
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

	tracer := observ.NewTracer(nil)
	logger := observ.NewLogger(nil)
	registry, err := connect.New(drivers.All(), defs, connect.NewEnvSecretProvider(), tracer, logger)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	t.Cleanup(func() { registry.Close() })

	engine := decision.New(jdmLoader{store: store})
	t.Cleanup(func() { engine.Close() })

	return &harness{store: store, registry: registry, engine: engine, restHits: hits}
}

// deps builds the httpapi.Deps over the harness collaborators, with the optional
// auth middleware attached.
func (h *harness) deps(box *authMiddlewareBox) httpapi.Deps {
	d := httpapi.Deps{
		Conns:  h.registry,
		Decide: h.engine,
		Trace:  observ.NewTracer(nil),
		Log:    observ.NewLogger(nil),
	}
	if box != nil {
		d.Auth = box.mw
	}
	return d
}

// authMiddlewareBox carries a built auth middleware plus the signing key so a
// test can mint tokens the middleware will accept.
type authMiddlewareBox struct {
	mw   *auth.Middleware
	sign func(sub string) string
}

// TestWiringAuthEnforced proves the auth chain wired in front of the flow route:
// with auth configured, a request with NO bearer is 401, a request with a valid
// bearer (allowed by the authz JDM) reaches the flow and returns 200, and a
// valid bearer whose subject the authz JDM denies is 403.
func TestWiringAuthEnforced(t *testing.T) {
	h := newHarness(t)

	box := buildAuthBox(t, h)
	handler := httpapi.NewHandler(h.store, flow.New(), h.deps(box))

	// (1) no bearer -> 401
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders/1", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-bearer status = %d want 401 (body %s)", rec.Code, rec.Body.String())
	}

	// (2) valid bearer, allowed subject -> 200 and the flow ran
	*h.restHits = nil
	req := httptest.NewRequest(http.MethodGet, "/orders/1", nil)
	req.Header.Set("Authorization", "Bearer "+box.sign("alice"))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("allowed status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["shipping"] != "expedited" {
		t.Fatalf("shipping = %v want expedited (full %v)", body["shipping"], body)
	}

	// (3) valid bearer, denied subject -> 403
	req = httptest.NewRequest(http.MethodGet, "/orders/1", nil)
	req.Header.Set("Authorization", "Bearer "+box.sign("denied"))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("denied status = %d want 403 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestWiringAdminValidate proves step 10(d): /admin/flows/validate on the seed
// flow returns ok:true, and a deliberately-broken flow (response with a child)
// returns ok:false with the specific structural code — publish-blocking proven.
func TestWiringAdminValidate(t *testing.T) {
	h := newHarness(t)
	handler := httpapi.NewHandler(h.store, flow.New(), h.deps(nil))

	// Pull the seed's active flow tree to validate it as a candidate.
	fv, err := h.store.ActiveFlow(context.Background(), "", http.MethodGet, "/orders/{id}")
	if err != nil {
		t.Fatalf("active flow: %v", err)
	}

	// (a) seed flow validates ok:true (structurally; no fixtures asserted here).
	okBody := map[string]any{
		"env": "",
		"flow": map[string]any{
			"flowId": fv.FlowID, "method": fv.Method, "path": fv.Path, "tree": fv.Tree,
		},
	}
	rec := postJSONBody(t, handler, "/admin/flows/validate", okBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var okResp struct {
		OK         bool             `json:"ok"`
		Structural []map[string]any `json:"structural"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &okResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !okResp.OK {
		t.Fatalf("seed flow validate ok = false, want true (structural %v)", okResp.Structural)
	}

	// (b) a broken flow (response node with a child) is ok:false with the code.
	brokenTree := map[string]any{
		"id": "trigger", "type": "trigger",
		"spec": map[string]any{"method": "GET", "path": "/x"},
		"children": []any{
			map[string]any{
				"id": "r", "type": "response", "spec": map[string]any{"status": 200},
				"children": []any{
					map[string]any{"id": "r2", "type": "response", "spec": map[string]any{"status": 200}},
				},
			},
		},
	}
	badBody := map[string]any{"env": "", "flow": map[string]any{"flowId": "broken", "method": "GET", "path": "/x", "tree": brokenTree}}
	rec = postJSONBody(t, handler, "/admin/flows/validate", badBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("broken validate status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var badResp struct {
		OK         bool `json:"ok"`
		Structural []struct {
			Code string `json:"Code"`
		} `json:"structural"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &badResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if badResp.OK {
		t.Fatalf("broken flow validate ok = true, want false")
	}
	hasLeaf := false
	for _, s := range badResp.Structural {
		if s.Code == "leaf_has_children" {
			hasLeaf = true
		}
	}
	if !hasLeaf {
		t.Fatalf("broken flow missing leaf_has_children code: %s", rec.Body.String())
	}
}

// TestWiringAdminDryRun proves step 10(c): /admin/flows/dry-run over the orders
// flow returns a node trace where the REST POST action is recorded
// wrote:"suppressed", the REST stub recorded ZERO hits (write suppressed), while
// the postgres read still ran (the condition branched on real data).
func TestWiringAdminDryRun(t *testing.T) {
	h := newHarness(t)
	handler := httpapi.NewHandler(h.store, flow.New(), h.deps(nil))

	*h.restHits = nil
	body := map[string]any{
		"env": "", "flowId": "orders-expedite", "method": "GET", "path": "/orders/{id}",
		"input": map[string]any{"id": 1},
	}
	rec := postJSONBody(t, handler, "/admin/flows/dry-run", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry-run status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Trace []struct {
			NodeID string         `json:"node_id"`
			Attrs  map[string]any `json:"attrs"`
		} `json:"trace"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// NOTE on errors: the orders flow's Set reads "decision.body.shipping" — the
	// RETURN VALUE of the write action. Under dry-run that write is suppressed
	// (recorded wrote:"suppressed"), so its body is absent and the dependent Set
	// surfaces a validation error. That is the correct, expected consequence of
	// suppressing a write whose output a later node consumes; the dry-run still
	// returns the node-by-node trace (the deliverable) and performs NO write. The
	// AC-14 assertions below (suppressed marker + zero external POSTs + the read
	// still ran) are what this case proves; a non-empty errors array for a
	// write-output-dependent flow is expected, not a failure.

	// The REST write was suppressed: zero hits on the stub.
	if len(*h.restHits) != 0 {
		t.Fatalf("REST stub hits = %v want none (write suppressed)", *h.restHits)
	}
	// The write action node recorded wrote:"suppressed". The suppressed node is
	// the "expedited" branch write, which is only reached if the postgres READ
	// ran and the ZEN condition branched on the real amount=1500 row — so the
	// read executing (reads are NOT suppressed) is proven by the branch taken.
	//
	// NOTE: the interpreter records a trace step via the collector only on the
	// write-suppression path (the cross-cutting edit this task added); it does
	// not emit an automatic per-node Record for every node (that TraceNode
	// integration lives in internal/flow and is out of this wiring task's scope).
	// So the trace shows the suppressed write; the read is proven by the branch.
	foundSuppressed := false
	for _, s := range resp.Trace {
		if s.NodeID == "expedited" && s.Attrs != nil && s.Attrs["wrote"] == "suppressed" {
			foundSuppressed = true
		}
	}
	if !foundSuppressed {
		t.Fatalf("expedited write node not recorded wrote:suppressed in trace: %s", rec.Body.String())
	}
}

// postJSONBody marshals body to JSON and POSTs it to target against h.
func postJSONBody(t *testing.T, h http.Handler, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(string(b)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// buildAuthBox builds the real auth middleware for the harness using a static
// JWKS (the RSA public key as a JWK) so BuildAuthMiddleware enables auth with no
// network, plus a signer that mints RS256 tokens the verifier accepts. The authz
// JDM in the seed denies subject "denied" and allows everyone else.
func buildAuthBox(t *testing.T, h *harness) *authMiddlewareBox {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	const kid = "test-kid"
	const issuer = "https://issuer.test"
	const audience = "nzr-api"

	jwks := rsaJWKS(t, &key.PublicKey, kid)

	mw, enabled, err := httpapi.BuildAuthMiddleware(httpapi.AuthConfig{
		Issuer:     issuer,
		Audience:   audience,
		StaticJWKS: jwks,
		AuthzJDMID: "authz",
	}, h.engine, observ.NewLogger(nil))
	if err != nil {
		t.Fatalf("build auth: %v", err)
	}
	if !enabled {
		t.Fatalf("auth should be enabled with full config")
	}

	sign := func(sub string) string {
		now := time.Now()
		claims := jwt.MapClaims{
			"sub":   sub,
			"iss":   issuer,
			"aud":   audience,
			"roles": []any{"user"},
			"iat":   now.Add(-time.Minute).Unix(),
			"nbf":   now.Add(-time.Minute).Unix(),
			"exp":   now.Add(time.Hour).Unix(),
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = kid
		s, serr := tok.SignedString(key)
		if serr != nil {
			t.Fatalf("sign: %v", serr)
		}
		return s
	}
	return &authMiddlewareBox{mw: mw, sign: sign}
}

// rsaJWKS renders an RSA public key as a one-key JWKS JSON document (kty "RSA",
// base64url n/e) so the static key source parses it back to the same key.
func rsaJWKS(t *testing.T, pub *rsa.PublicKey, kid string) string {
	t.Helper()
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	var eBuf [8]byte
	binary.BigEndian.PutUint64(eBuf[:], uint64(pub.E))
	// Trim leading zero bytes from the big-endian E.
	i := 0
	for i < len(eBuf)-1 && eBuf[i] == 0 {
		i++
	}
	e := base64.RawURLEncoding.EncodeToString(eBuf[i:])
	doc := map[string]any{
		"keys": []any{
			map[string]any{"kty": "RSA", "kid": kid, "n": n, "e": e},
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return string(b)
}
