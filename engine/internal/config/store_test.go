package config

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"nzr-rules-engine/internal/flow"
)

// TestSeedResolvesFlow loads the committed seed and asserts the thin-slice flow
// resolves via ActiveFlow, the JDM loads with bytes+version, both connections
// load, and Ping succeeds.
func TestSeedResolvesFlow(t *testing.T) {
	ctx := context.Background()
	store, err := LoadSeed("testdata/seed.json")
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}

	// ActiveFlow resolves the seeded tree for the thin-slice route.
	fv, err := store.ActiveFlow(ctx, "", "GET", "/orders/{id}")
	if err != nil {
		t.Fatalf("ActiveFlow: %v", err)
	}
	if fv.FlowID != "orders-expedite" || fv.Version != 1 {
		t.Fatalf("ActiveFlow id/version = %s/%d; want orders-expedite/1", fv.FlowID, fv.Version)
	}
	if fv.Tree.Type != flow.TypeTrigger {
		t.Fatalf("root node type = %q; want trigger", fv.Tree.Type)
	}
	// Walk the tree shape: trigger -> action -> condition -> {expedited, standard}.
	action := childByID(t, fv.Tree.Children, "read-order")
	cond := childByID(t, action.Children, "classify")
	if cond.Type != flow.TypeCondition {
		t.Fatalf("classify node type = %q; want condition", cond.Type)
	}
	childByID(t, cond.Children, "expedited")
	childByID(t, cond.Children, "standard")

	// GetJDM returns bytes + version for the order JDM, and the bytes are valid JSON.
	jdm, version, err := store.GetJDM(ctx, "", "order")
	if err != nil {
		t.Fatalf("GetJDM: %v", err)
	}
	if version != 1 {
		t.Fatalf("JDM version = %d; want 1", version)
	}
	if len(jdm) == 0 {
		t.Fatal("JDM bytes empty")
	}
	var doc map[string]any
	if err := json.Unmarshal(jdm, &doc); err != nil {
		t.Fatalf("JDM bytes are not valid JSON: %v", err)
	}
	if _, ok := doc["nodes"]; !ok {
		t.Fatal("JDM doc missing nodes")
	}

	// Connections returns both seeded defs.
	conns, err := store.Connections(ctx, "")
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if len(conns) != 3 {
		t.Fatalf("connections = %d; want 3 (orders-pg, ship-rest, fmc-pg)", len(conns))
	}
	keys := map[string]string{}
	for _, c := range conns {
		keys[c.Key] = c.Type
	}
	if keys["orders-pg"] != "postgres" {
		t.Fatalf("orders-pg type = %q; want postgres", keys["orders-pg"])
	}
	if keys["ship-rest"] != "rest" {
		t.Fatalf("ship-rest type = %q; want rest", keys["ship-rest"])
	}
	if keys["fmc-pg"] != "postgres" {
		t.Fatalf("fmc-pg type = %q; want postgres", keys["fmc-pg"])
	}

	// Ping is reachable.
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// TestActiveRoutes proves ActiveRoutes enumerates the seeded active route(s)
// with the right {FlowID,Method,Path}, and that a put-but-not-activated version
// is NOT returned (only active pointers are enumerated).
func TestActiveRoutes(t *testing.T) {
	ctx := context.Background()
	store, err := LoadSeed("testdata/seed.json")
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}

	routes, err := store.ActiveRoutes(ctx, "")
	if err != nil {
		t.Fatalf("ActiveRoutes: %v", err)
	}
	// The seed activates three flows: orders-expedite plus the two FMC demo flows.
	if len(routes) != 3 {
		t.Fatalf("active routes = %d; want 3 (%+v)", len(routes), routes)
	}
	byPath := map[string]RouteInfo{}
	for _, r := range routes {
		byPath[r.Path] = r
	}
	if got, ok := byPath["/orders/{id}"]; !ok || got.FlowID != "orders-expedite" || got.Method != "GET" {
		t.Fatalf("route /orders/{id} = %+v (ok=%v); want {orders-expedite GET}", got, ok)
	}
	if got, ok := byPath["/order/{order_id}"]; !ok || got.FlowID != "fmc-order-by-id" || got.Method != "GET" {
		t.Fatalf("route /order/{order_id} = %+v (ok=%v); want {fmc-order-by-id GET}", got, ok)
	}
	if got, ok := byPath["/order/msisdn/{msisdn}"]; !ok || got.FlowID != "fmc-order-by-msisdn" || got.Method != "GET" {
		t.Fatalf("route /order/msisdn/{msisdn} = %+v (ok=%v); want {fmc-order-by-msisdn GET}", got, ok)
	}

	// A new flow version that is PUT but never SetActive must not appear.
	inactive := FlowVersion{
		FlowID: "widgets-list",
		Method: "GET",
		Path:   "/widgets",
		Tree:   flow.Node{ID: "t", Type: flow.TypeTrigger},
	}
	if _, err := store.PutFlowVersion(ctx, "", inactive); err != nil {
		t.Fatalf("PutFlowVersion: %v", err)
	}
	routes, err = store.ActiveRoutes(ctx, "")
	if err != nil {
		t.Fatalf("ActiveRoutes after put: %v", err)
	}
	for _, r := range routes {
		if r.FlowID == "widgets-list" {
			t.Fatalf("inactive flow version returned by ActiveRoutes: %+v", r)
		}
	}
	if len(routes) != 3 {
		t.Fatalf("active routes after inactive put = %d; want 3", len(routes))
	}

	// An env with no active routes returns a non-nil empty slice.
	empty, err := store.ActiveRoutes(ctx, "no-such-env")
	if err != nil {
		t.Fatalf("ActiveRoutes empty env: %v", err)
	}
	if empty == nil {
		t.Fatal("ActiveRoutes returned a nil slice for an empty env; want non-nil empty")
	}
	if len(empty) != 0 {
		t.Fatalf("empty env routes = %d; want 0", len(empty))
	}
}

// TestActiveFlowUnknownRoute proves an unknown route is a NotFound error.
func TestActiveFlowUnknownRoute(t *testing.T) {
	store, err := LoadSeed("testdata/seed.json")
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	_, err = store.ActiveFlow(context.Background(), "", "GET", "/nope")
	if err == nil {
		t.Fatal("expected NotFound for an unknown route")
	}
	var ce *ConfigError
	if !errors.As(err, &ce) || ce.Class != NotFound {
		t.Fatalf("unknown route error = %v; want NotFound", err)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown route error does not match ErrNotFound: %v", err)
	}
}

// childByID returns the child with the given id, failing the test if absent.
func childByID(t *testing.T, children []flow.Node, id string) flow.Node {
	t.Helper()
	for _, c := range children {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("child %q not found", id)
	return flow.Node{}
}
