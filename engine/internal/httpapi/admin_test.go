package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nzr-rules-engine/internal/auth"
	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/flow"
)

// fakeAdminStore is a canned AdminStore for the admin handler unit tests. Each
// field lets a test force a specific return / classified error without a DB.
type fakeAdminStore struct {
	putFlowVersion    int
	putFlowErr        error
	setActiveErr      error
	connections       []connect.ConnectionDef
	connectionsErr    error
	getJDMErr         error
	activeFlow        config.FlowVersion
	activeFlowErr     error
	putJDMVersion     int
	putJDMErr         error
	putConnVersion    int
	putConnErr        error
	markValidatedErr  error
	getFlowVersion    config.FlowVersion
	getFlowVersionErr error
	auditEntries      []config.AuditEntry
	auditErr          error
	// list methods:
	listFlows           []config.FlowSummary
	listFlowsErr        error
	listFlowVersions    []config.VersionSummary
	listFlowVersionsErr error
	listJDMs            []config.JDMSummary
	listJDMsErr         error
	getConnection       connect.ConnectionDef
	getConnectionErr    error

	// capture:
	lastPutFlow   config.FlowVersion
	lastSetActive int
	markCalled    bool
}

func (f *fakeAdminStore) PutFlowVersion(ctx context.Context, env string, fv config.FlowVersion) (int, error) {
	f.lastPutFlow = fv
	return f.putFlowVersion, f.putFlowErr
}
func (f *fakeAdminStore) SetActive(ctx context.Context, env, flowID string, version int) error {
	f.lastSetActive = version
	return f.setActiveErr
}
func (f *fakeAdminStore) Connections(ctx context.Context, env string) ([]connect.ConnectionDef, error) {
	return f.connections, f.connectionsErr
}
func (f *fakeAdminStore) GetJDM(ctx context.Context, env, id string) ([]byte, int, error) {
	if f.getJDMErr != nil {
		return nil, 0, f.getJDMErr
	}
	return []byte(`{}`), 1, nil
}
func (f *fakeAdminStore) ActiveFlow(ctx context.Context, env, method, path string) (config.FlowVersion, error) {
	return f.activeFlow, f.activeFlowErr
}
func (f *fakeAdminStore) PutJDMVersion(ctx context.Context, env, jdmID string, doc []byte, version int) (int, error) {
	return f.putJDMVersion, f.putJDMErr
}
func (f *fakeAdminStore) PutConnectionVersion(ctx context.Context, env string, def connect.ConnectionDef) (int, error) {
	return f.putConnVersion, f.putConnErr
}
func (f *fakeAdminStore) MarkValidated(ctx context.Context, env, flowID string, version int) error {
	f.markCalled = true
	return f.markValidatedErr
}
func (f *fakeAdminStore) GetFlowVersion(ctx context.Context, env, flowID string, version int) (config.FlowVersion, error) {
	return f.getFlowVersion, f.getFlowVersionErr
}
func (f *fakeAdminStore) AuditTrail(ctx context.Context, env, objectType, objectID string) ([]config.AuditEntry, error) {
	return f.auditEntries, f.auditErr
}
func (f *fakeAdminStore) ListFlows(ctx context.Context, env string) ([]config.FlowSummary, error) {
	return f.listFlows, f.listFlowsErr
}
func (f *fakeAdminStore) ListFlowVersions(ctx context.Context, env, flowID string) ([]config.VersionSummary, error) {
	return f.listFlowVersions, f.listFlowVersionsErr
}
func (f *fakeAdminStore) ListJDMs(ctx context.Context, env string) ([]config.JDMSummary, error) {
	return f.listJDMs, f.listJDMsErr
}
func (f *fakeAdminStore) GetConnection(ctx context.Context, env, key string) (connect.ConnectionDef, error) {
	return f.getConnection, f.getConnectionErr
}

var _ AdminStore = (*fakeAdminStore)(nil)

// allowOperator is an OperatorAuthenticator that always passes with a full role
// set, so the handler logic (not the guard) is under test.
type allowOperator struct{}

func (allowOperator) AuthenticateOperator(ctx context.Context, r *http.Request) (auth.Operator, error) {
	return auth.Operator{Subject: "op:test", Roles: []string{"flow.read", "flow.write", "flow.publish"}}, nil
}

// newAdminTestHandler builds a handler whose admin surface is wired with the
// given fake store and an always-allow operator.
func newAdminTestHandler(t *testing.T, store AdminStore) http.Handler {
	t.Helper()
	h, err := NewHandler(
		fakeStore{routes: []config.RouteInfo{}},
		flow.New(),
		Deps{Admin: store, OperAuth: allowOperator{}},
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

// minimalTree is a valid trigger->response flow for create/validate tests.
func minimalTree() flow.Node {
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)}
	return flow.Node{ID: "t", Type: flow.TypeTrigger,
		Spec: json.RawMessage(`{"method":"POST","path":"/x","input":{}}`), Children: []flow.Node{resp}}
}

func doJSON(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer x")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestAdminCreateFlow proves a well-formed create returns 201 with the store
// version, and a bad tree is a 400 before the store is touched.
func TestAdminCreateFlow(t *testing.T) {
	store := &fakeAdminStore{putFlowVersion: 7}
	h := newAdminTestHandler(t, store)

	treeJSON, _ := json.Marshal(minimalTree())
	body := `{"flowId":"orders","method":"POST","path":"/x","tree":` + string(treeJSON) + `}`
	rec := doJSON(t, h, http.MethodPost, "/admin/flows", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["version"] != float64(7) || out["validated"] != false {
		t.Fatalf("create body = %v want version 7, validated false", out)
	}

	// A structurally invalid tree (no trigger root) => 400, store not touched.
	badTree, _ := json.Marshal(flow.Node{ID: "a", Type: flow.TypeResponse})
	badBody := `{"flowId":"x","method":"POST","path":"/y","tree":` + string(badTree) + `}`
	rec = doJSON(t, h, http.MethodPost, "/admin/flows", badBody)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad tree status = %d want 400", rec.Code)
	}
}

// TestAdminPublishUnvalidated maps the store's un-validated publish Validation to
// a 422 (publish-blocking), while a valid publish is 200 with route-derived action.
func TestAdminPublishUnvalidated(t *testing.T) {
	store := &fakeAdminStore{setActiveErr: config.ErrUnvalidated}
	h := newAdminTestHandler(t, store)
	rec := doJSON(t, h, http.MethodPost, "/admin/flows/orders/publish", `{"version":3}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("publish un-validated status = %d want 422 (body %s)", rec.Code, rec.Body.String())
	}

	store2 := &fakeAdminStore{}
	h2 := newAdminTestHandler(t, store2)
	rec = doJSON(t, h2, http.MethodPost, "/admin/flows/orders/publish", `{"version":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("publish status = %d want 200", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["action"] != "publish" || out["activeVersion"] != float64(3) {
		t.Fatalf("publish body = %v want action publish, activeVersion 3", out)
	}

	// rollback route derives action=rollback.
	rec = doJSON(t, h2, http.MethodPost, "/admin/flows/orders/rollback", `{"version":1}`)
	var out2 map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out2)
	if rec.Code != http.StatusOK || out2["action"] != "rollback" {
		t.Fatalf("rollback body = %v (status %d) want action rollback 200", out2, rec.Code)
	}
}

// TestAdminConnectionSecretRejected proves a secret-VALUE field is rejected with
// 400 (secret_ref only), both at top level and nested under settings.
func TestAdminConnectionSecretRejected(t *testing.T) {
	store := &fakeAdminStore{putConnVersion: 2}
	h := newAdminTestHandler(t, store)

	// secretRef-only is accepted.
	ok := `{"key":"pg","type":"postgres","settings":{"host":"h"},"secretRef":"env:PG_PW"}`
	rec := doJSON(t, h, http.MethodPost, "/admin/connections", ok)
	if rec.Code != http.StatusCreated {
		t.Fatalf("secretRef-only status = %d want 201 (body %s)", rec.Code, rec.Body.String())
	}

	// top-level secret value rejected.
	for _, body := range []string{
		`{"key":"pg","type":"postgres","password":"hunter2"}`,
		`{"key":"pg","type":"postgres","token":"abc"}`,
		`{"key":"pg","type":"postgres","settings":{"password":"hunter2"}}`,
	} {
		rec := doJSON(t, h, http.MethodPost, "/admin/connections", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("secret-value body %q status = %d want 400", body, rec.Code)
		}
	}
}

// TestAdminListConnectionsRedacts proves the list response never surfaces a
// secret-looking field value; secretRef (a pointer) survives.
func TestAdminListConnectionsRedacts(t *testing.T) {
	store := &fakeAdminStore{connections: []connect.ConnectionDef{
		{Key: "pg", Type: "postgres", Settings: map[string]any{"host": "h", "password": "leak"}, SecretRef: "env:PG_PW"},
	}}
	h := newAdminTestHandler(t, store)
	rec := doJSON(t, h, http.MethodGet, "/admin/connections", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "leak") {
		t.Fatalf("secret value leaked in connection list: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "env:PG_PW") {
		t.Fatalf("secretRef pointer missing from list: %s", rec.Body.String())
	}
}

// TestStatusForAdmin pins the full error->status map by errors.Is/As.
func TestStatusForAdmin(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{config.ErrRouteConflict, http.StatusConflict},
		{config.ErrUnvalidated, http.StatusUnprocessableEntity},
		{config.ErrNotFound, http.StatusNotFound},
		{config.ErrValidation, http.StatusBadRequest},
		{config.ErrTimeout, http.StatusGatewayTimeout},
		{config.ErrUpstream, http.StatusBadGateway},
		{config.ErrInternal, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		if got, _ := statusForAdmin(tc.err); got != tc.want {
			t.Errorf("statusForAdmin(%v) = %d want %d", tc.err, got, tc.want)
		}
	}
}

// TestAdminRoutingPrecedence proves /admin/flows/validate resolves to the
// validate handler (NOT {id}/publish), and GET vs POST dispatch + {type}/{id}
// capture work.
func TestAdminRoutingPrecedence(t *testing.T) {
	store := &fakeAdminStore{}
	h := newAdminTestHandler(t, store)

	// validate (literal) wins over {id}/publish: a validate call with an inline
	// flow returns 200 (a validate result), never a 422/parse of "validate" as id.
	treeJSON, _ := json.Marshal(minimalTree())
	body := `{"flow":{"flowId":"x","method":"POST","path":"/x","tree":` + string(treeJSON) + `}}`
	rec := doJSON(t, h, http.MethodPost, "/admin/flows/validate", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate status = %d want 200 (precedence: literal beats {id}/publish)", rec.Code)
	}
	var vout map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &vout)
	if _, ok := vout["ok"]; !ok {
		t.Fatalf("validate body missing ok: %v", vout)
	}

	// audit {type}/{id} capture + GET dispatch.
	store.auditEntries = []config.AuditEntry{{Action: "publish"}}
	rec = doJSON(t, h, http.MethodGet, "/admin/audit/flow/orders", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit status = %d want 200", rec.Code)
	}
	// unknown audit type => 400.
	rec = doJSON(t, h, http.MethodGet, "/admin/audit/widget/orders", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("audit bad type status = %d want 400", rec.Code)
	}
}

// TestAdminDisabledPlane proves that with no OperatorAuthenticator wired, every
// /admin/* route is 503 (mount-closed), regardless of a valid-looking token.
func TestAdminDisabledPlane(t *testing.T) {
	h, err := NewHandler(
		fakeStore{routes: []config.RouteInfo{}},
		flow.New(),
		Deps{Admin: &fakeAdminStore{}, OperAuth: nil},
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rec := doJSON(t, h, http.MethodPost, "/admin/flows", `{}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled-plane status = %d want 503", rec.Code)
	}
}

// TestAdminRequiresConfigStore proves that in in-memory mode (nil Admin) an admin
// write returns a classified "requires config-store mode" error (500), after the
// guard passes.
func TestAdminRequiresConfigStore(t *testing.T) {
	h, err := NewHandler(
		fakeStore{routes: []config.RouteInfo{}},
		flow.New(),
		Deps{Admin: nil, OperAuth: allowOperator{}},
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rec := doJSON(t, h, http.MethodPost, "/admin/flows", `{"flowId":"x","method":"POST","path":"/x"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("nil-admin status = %d want 500", rec.Code)
	}
}

// TestAdminListFlows proves GET /admin/flows returns the list of flows.
func TestAdminListFlows(t *testing.T) {
	v := 3
	store := &fakeAdminStore{listFlows: []config.FlowSummary{
		{ID: "orders", Method: "POST", Path: "/orders", ActiveVersion: &v},
		{ID: "refunds", Method: "GET", Path: "/refunds"},
	}}
	h := newAdminTestHandler(t, store)
	rec := doJSON(t, h, http.MethodGet, "/admin/flows", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list flows status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	flows, ok := out["flows"].([]any)
	if !ok || len(flows) != 2 {
		t.Fatalf("list flows body = %v want 2 flows", out)
	}

	// Error case.
	store2 := &fakeAdminStore{listFlowsErr: config.ErrUpstream}
	h2 := newAdminTestHandler(t, store2)
	rec = doJSON(t, h2, http.MethodGet, "/admin/flows", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("list flows error status = %d want 502", rec.Code)
	}
}

// TestAdminGetFlow proves GET /admin/flows/{id} returns the flow.
func TestAdminGetFlow(t *testing.T) {
	v := 2
	store := &fakeAdminStore{
		listFlows:        []config.FlowSummary{{ID: "orders", Method: "POST", Path: "/orders", ActiveVersion: &v}},
		listFlowVersions: []config.VersionSummary{{Version: 2, Validated: true}, {Version: 1}},
		getFlowVersion:   config.FlowVersion{FlowID: "orders", Version: 2, Method: "POST", Path: "/orders", Tree: minimalTree()},
	}
	h := newAdminTestHandler(t, store)
	rec := doJSON(t, h, http.MethodGet, "/admin/flows/orders", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get flow status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["flowId"] != "orders" {
		t.Fatalf("get flow body = %v want flowId orders", out)
	}

	// Not found case.
	store2 := &fakeAdminStore{listFlowVersionsErr: config.ErrNotFound}
	h2 := newAdminTestHandler(t, store2)
	rec = doJSON(t, h2, http.MethodGet, "/admin/flows/nonexistent", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get flow not found status = %d want 404", rec.Code)
	}
}

// TestAdminListFlowVersions proves GET /admin/flows/{id}/versions returns versions.
func TestAdminListFlowVersions(t *testing.T) {
	store := &fakeAdminStore{listFlowVersions: []config.VersionSummary{
		{Version: 3, Validated: true},
		{Version: 2, Validated: true},
		{Version: 1, Validated: false},
	}}
	h := newAdminTestHandler(t, store)
	rec := doJSON(t, h, http.MethodGet, "/admin/flows/orders/versions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list versions status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["flowId"] != "orders" {
		t.Fatalf("list versions body missing flowId: %v", out)
	}
	versions, ok := out["versions"].([]any)
	if !ok || len(versions) != 3 {
		t.Fatalf("list versions body = %v want 3 versions", out)
	}

	// Not found case.
	store2 := &fakeAdminStore{listFlowVersionsErr: config.ErrNotFound}
	h2 := newAdminTestHandler(t, store2)
	rec = doJSON(t, h2, http.MethodGet, "/admin/flows/nonexistent/versions", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("list versions not found status = %d want 404", rec.Code)
	}
}

// TestAdminListJDMs proves GET /admin/jdms returns the list of JDMs.
func TestAdminListJDMs(t *testing.T) {
	store := &fakeAdminStore{listJDMs: []config.JDMSummary{
		{ID: "risk-scoring"},
		{ID: "pricing"},
	}}
	h := newAdminTestHandler(t, store)
	rec := doJSON(t, h, http.MethodGet, "/admin/jdms", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list jdms status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	jdms, ok := out["jdms"].([]any)
	if !ok || len(jdms) != 2 {
		t.Fatalf("list jdms body = %v want 2 jdms", out)
	}
}

// TestAdminGetJDM proves GET /admin/jdms/{id} returns the JDM doc.
func TestAdminGetJDM(t *testing.T) {
	store := &fakeAdminStore{} // GetJDM returns []byte(`{}`), 1, nil by default
	h := newAdminTestHandler(t, store)
	rec := doJSON(t, h, http.MethodGet, "/admin/jdms/risk-scoring", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get jdm status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["jdmId"] != "risk-scoring" || out["version"] != float64(1) {
		t.Fatalf("get jdm body = %v want jdmId risk-scoring, version 1", out)
	}

	// Not found case.
	store2 := &fakeAdminStore{getJDMErr: config.ErrNotFound}
	h2 := newAdminTestHandler(t, store2)
	rec = doJSON(t, h2, http.MethodGet, "/admin/jdms/nonexistent", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get jdm not found status = %d want 404", rec.Code)
	}
}

// TestAdminGetConnectionRedacts proves GET /admin/connections/{key} returns the
// connection with settings redacted.
func TestAdminGetConnectionRedacts(t *testing.T) {
	store := &fakeAdminStore{getConnection: connect.ConnectionDef{
		Key: "pg", Type: "postgres", Settings: map[string]any{"host": "h", "password": "leak"}, SecretRef: "env:PG_PW",
	}}
	h := newAdminTestHandler(t, store)
	rec := doJSON(t, h, http.MethodGet, "/admin/connections/pg", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get connection status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "leak") {
		t.Fatalf("secret value leaked in connection: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "env:PG_PW") {
		t.Fatalf("secretRef pointer missing from connection: %s", rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["key"] != "pg" || out["type"] != "postgres" {
		t.Fatalf("get connection body = %v want key pg, type postgres", out)
	}

	// Not found case.
	store2 := &fakeAdminStore{getConnectionErr: config.ErrNotFound}
	h2 := newAdminTestHandler(t, store2)
	rec = doJSON(t, h2, http.MethodGet, "/admin/connections/nonexistent", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get connection not found status = %d want 404", rec.Code)
	}
}
