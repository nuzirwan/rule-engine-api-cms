package decision

import (
	"context"
	"testing"
)

// countingCompiled is a fake Compiled returning a fixed output; it records that
// it was closed.
type countingCompiled struct {
	out    map[string]any
	closed bool
}

func (c *countingCompiled) Eval(ctx context.Context, input map[string]any) (map[string]any, error) {
	return c.out, nil
}
func (c *countingCompiled) Close() { c.closed = true }

// TestGetOrCompileOncePerKey proves getOrCompile compiles exactly once per
// (id,version) across repeats and recompiles for a NEW version (version-keyed
// invalidation).
func TestGetOrCompileOncePerKey(t *testing.T) {
	var compiles int
	cache := newCompiledCache(func(jdm []byte) (Compiled, error) {
		compiles++
		return &countingCompiled{out: map[string]any{"v": compiles}}, nil
	})

	jdm := []byte(`{"doc":1}`)

	// Repeated (order,1) compiles once.
	for i := 0; i < 4; i++ {
		if _, err := cache.getOrCompile("order", 1, jdm); err != nil {
			t.Fatalf("getOrCompile v1 #%d: %v", i, err)
		}
	}
	if compiles != 1 {
		t.Fatalf("compiles after repeated v1 = %d; want 1", compiles)
	}

	// A new version recompiles.
	if _, err := cache.getOrCompile("order", 2, jdm); err != nil {
		t.Fatalf("getOrCompile v2: %v", err)
	}
	if compiles != 2 {
		t.Fatalf("compiles after v2 = %d; want 2 (version-keyed recompile)", compiles)
	}

	// Back to v1 is still cached (no recompile).
	if _, err := cache.getOrCompile("order", 1, jdm); err != nil {
		t.Fatalf("getOrCompile v1 again: %v", err)
	}
	if compiles != 2 {
		t.Fatalf("compiles after revisiting v1 = %d; want 2", compiles)
	}
}

// TestCacheCloseFreesGraphs proves Close frees every cached graph.
func TestCacheCloseFreesGraphs(t *testing.T) {
	var made []*countingCompiled
	cache := newCompiledCache(func(jdm []byte) (Compiled, error) {
		c := &countingCompiled{out: map[string]any{}}
		made = append(made, c)
		return c, nil
	})

	_, _ = cache.getOrCompile("a", 1, nil)
	_, _ = cache.getOrCompile("b", 1, nil)
	cache.Close()

	for i, c := range made {
		if !c.closed {
			t.Fatalf("graph %d not closed on cache Close", i)
		}
	}
}

// fakeLoader serves fixed JDM bytes + version for any id.
type fakeLoader struct {
	jdm     []byte
	version int
}

func (f fakeLoader) LoadJDM(ctx context.Context, env, jdmID string) ([]byte, int, error) {
	return f.jdm, f.version, nil
}
