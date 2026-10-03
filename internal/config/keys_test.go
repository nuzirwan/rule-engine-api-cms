package config

import "testing"

// TestKeySchemeNamespacedAndVersioned proves cache keys are namespaced and carry
// the v1 schema segment so a shape change cannot serve stale entries
// ([[caching-strategy]] versioned keys).
func TestKeySchemeNamespacedAndVersioned(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"flow", keyFlow("prod", "GET", "/orders/{id}"), "cfg:v1:flow:prod:GET:/orders/{id}"},
		{"jdm", keyJDM("prod", "order"), "cfg:v1:jdm:prod:order"},
		{"conns", keyConns("prod"), "cfg:v1:conns:prod"},
		{"flow-default-env", keyFlow("", "GET", "/x"), "cfg:v1:flow::GET:/x"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s key = %q; want %q", c.name, c.got, c.want)
		}
	}
}

// TestInvalidationChannelVersioned proves the pub/sub channel carries the schema
// version too.
func TestInvalidationChannelVersioned(t *testing.T) {
	if invalidateChannel != "cfg:v1:invalidate" {
		t.Fatalf("invalidateChannel = %q; want cfg:v1:invalidate", invalidateChannel)
	}
}

// TestFlowDeletePatternIsGlob proves the flow invalidation pattern is a glob over
// the env's flow namespace (a pointer move can change any route the flow owns).
func TestFlowDeletePatternIsGlob(t *testing.T) {
	p := flowDeletePattern("prod")
	if p != "cfg:v1:flow:prod:*" {
		t.Fatalf("flowDeletePattern = %q; want cfg:v1:flow:prod:*", p)
	}
	if !hasGlob(p) {
		t.Fatal("flowDeletePattern should be detected as a glob")
	}
	if hasGlob(keyConns("prod")) {
		t.Fatal("a plain conns key should not be a glob")
	}
}
