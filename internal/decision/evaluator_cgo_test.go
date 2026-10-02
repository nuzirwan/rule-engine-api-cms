//go:build cgo

package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// loadOrderJDM reads the committed order JDM bytes used across the slice tests
// (same decision table as internal/zenspike/testdata/order.jdm.json). The config
// seed carries the same table; this test reads the zenspike copy directly.
func loadOrderJDM(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "zenspike", "testdata", "order.jdm.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read order jdm: %v", err)
	}
	return b
}

// countingLoader serves fixed bytes at a chosen version and counts LoadJDM calls.
type countingLoader struct {
	jdm     []byte
	version int
	loads   int64
}

func (l *countingLoader) LoadJDM(ctx context.Context, env, jdmID string) ([]byte, int, error) {
	atomic.AddInt64(&l.loads, 1)
	return l.jdm, l.version, nil
}

// TestEvaluateDeterministic proves AC-4: the same (id,version,input) yields
// byte-identical output across repeats and across two Engine instances.
func TestEvaluateDeterministic(t *testing.T) {
	jdm := loadOrderJDM(t)
	ctx := context.Background()
	input := map[string]any{"amount": 1500, "status": "paid"}

	eng1 := New(&countingLoader{jdm: jdm, version: 1})
	defer eng1.Close()
	eng2 := New(&countingLoader{jdm: jdm, version: 1})
	defer eng2.Close()

	var want []byte
	for i := 0; i < 5; i++ {
		out, err := eng1.Evaluate(ctx, "order", input)
		if err != nil {
			t.Fatalf("eng1 evaluate #%d: %v", i, err)
		}
		if out["shipping"] != "expedited" {
			t.Fatalf("eng1 shipping = %v; want expedited", out["shipping"])
		}
		b, _ := json.Marshal(out)
		if want == nil {
			want = b
			continue
		}
		if !bytes.Equal(b, want) {
			t.Fatalf("eng1 output not deterministic: %s vs %s", b, want)
		}
	}

	out2, err := eng2.Evaluate(ctx, "order", input)
	if err != nil {
		t.Fatalf("eng2 evaluate: %v", err)
	}
	b2, _ := json.Marshal(out2)
	if !bytes.Equal(b2, want) {
		t.Fatalf("output differs across instances: %s vs %s", b2, want)
	}
}

// TestEvaluateBranches proves the real JDM takes different outputs for different
// inputs (high-value paid => expedited, low-value => standard).
func TestEvaluateBranches(t *testing.T) {
	jdm := loadOrderJDM(t)
	ctx := context.Background()
	eng := New(&countingLoader{jdm: jdm, version: 1})
	defer eng.Close()

	hi, err := eng.Evaluate(ctx, "order", map[string]any{"amount": 1500, "status": "paid"})
	if err != nil {
		t.Fatalf("evaluate hi: %v", err)
	}
	if hi["shipping"] != "expedited" {
		t.Fatalf("hi shipping = %v; want expedited", hi["shipping"])
	}
	lo, err := eng.Evaluate(ctx, "order", map[string]any{"amount": 500, "status": "paid"})
	if err != nil {
		t.Fatalf("evaluate lo: %v", err)
	}
	if lo["shipping"] != "standard" {
		t.Fatalf("lo shipping = %v; want standard", lo["shipping"])
	}
}

// TestCompileOncePerVersion proves the real engine compiles once per
// (id,version) and recompiles for a new version. It wraps the cache's compile
// func with a counter (white-box, same package).
func TestCompileOncePerVersion(t *testing.T) {
	jdm := loadOrderJDM(t)
	ctx := context.Background()
	loader := &countingLoader{jdm: jdm, version: 1}
	eng := New(loader)
	defer eng.Close()

	var compiles int64
	inner := eng.cache.compile
	eng.cache.compile = func(b []byte) (Compiled, error) {
		atomic.AddInt64(&compiles, 1)
		return inner(b)
	}

	input := map[string]any{"amount": 1500, "status": "paid"}
	for i := 0; i < 3; i++ {
		if _, err := eng.Evaluate(ctx, "order", input); err != nil {
			t.Fatalf("evaluate v1 #%d: %v", i, err)
		}
	}
	if got := atomic.LoadInt64(&compiles); got != 1 {
		t.Fatalf("compiles for repeated v1 = %d; want 1", got)
	}

	// Simulate a publish: the loader now returns a new version for the same id.
	loader.version = 2
	if _, err := eng.Evaluate(ctx, "order", input); err != nil {
		t.Fatalf("evaluate v2: %v", err)
	}
	if got := atomic.LoadInt64(&compiles); got != 2 {
		t.Fatalf("compiles after version bump = %d; want 2", got)
	}
}
