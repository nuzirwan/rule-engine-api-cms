package flow

import (
	"errors"
	"reflect"
	"testing"
)

func TestSetPath_SiblingCoexistence(t *testing.T) {
	c := NewCtx("req", "trace", "test", nil)

	for _, step := range []struct {
		path string
		val  any
	}{
		{"order.id", 42},
		{"order.total", 99},
		{"flags.expedited", true},
	} {
		if err := c.SetPath(step.path, step.val); err != nil {
			t.Fatalf("SetPath(%q) error: %v", step.path, err)
		}
	}

	want := map[string]any{
		"order": map[string]any{"id": 42, "total": 99},
		"flags": map[string]any{"expedited": true},
	}
	if !reflect.DeepEqual(c.Response, want) {
		t.Fatalf("Response = %#v, want %#v", c.Response, want)
	}
}

func TestSetPath_NoClobberExistingMap(t *testing.T) {
	c := NewCtx("req", "trace", "test", nil)
	// Pre-seed an existing nested map, then write a sibling leaf under it.
	if err := c.SetPath("a.b", 1); err != nil {
		t.Fatalf("seed SetPath: %v", err)
	}
	if err := c.SetPath("a.c", 2); err != nil {
		t.Fatalf("SetPath a.c: %v", err)
	}
	a, ok := c.Response["a"].(map[string]any)
	if !ok {
		t.Fatalf("a is not a map: %#v", c.Response["a"])
	}
	if a["b"] != 1 || a["c"] != 2 {
		t.Fatalf("sibling clobbered: a = %#v", a)
	}
}

func TestSetPath_NumericIndexPaths(t *testing.T) {
	c := NewCtx("req", "trace", "test", nil)
	if err := c.SetPath("items.0.id", "x"); err != nil {
		t.Fatalf("SetPath items.0.id: %v", err)
	}
	if err := c.SetPath("items.1.id", "y"); err != nil {
		t.Fatalf("SetPath items.1.id: %v", err)
	}
	want := map[string]any{
		"items": []any{
			map[string]any{"id": "x"},
			map[string]any{"id": "y"},
		},
	}
	if !reflect.DeepEqual(c.Response, want) {
		t.Fatalf("Response = %#v, want %#v", c.Response, want)
	}
}

func TestSetPath_TypeConflictIsValidation(t *testing.T) {
	c := NewCtx("req", "trace", "test", nil)
	// Make "a" a scalar, then try to descend through it.
	if err := c.SetPath("a", "scalar"); err != nil {
		t.Fatalf("seed SetPath: %v", err)
	}
	err := c.SetPath("a.b", 1)
	if err == nil {
		t.Fatal("expected a type-conflict error, got nil")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("error is not Validation-class: %v", err)
	}
	// The scalar must be untouched.
	if c.Response["a"] != "scalar" {
		t.Fatalf("scalar clobbered: %#v", c.Response["a"])
	}
}

func TestSetPath_EmptyPathIsValidation(t *testing.T) {
	c := NewCtx("req", "trace", "test", nil)
	if err := c.SetPath("", 1); !errors.Is(err, ErrValidation) {
		t.Fatalf("empty path error = %v, want Validation", err)
	}
}

func TestGetPath_Precedence(t *testing.T) {
	c := NewCtx("req", "trace", "test", map[string]any{
		"shared": "from-input",
		"only":   "input-only",
	})
	c.Data["shared"] = "from-data"
	if err := c.SetPath("shared", "from-response"); err != nil {
		t.Fatalf("SetPath: %v", err)
	}

	// Response wins over Data wins over Input.
	if v, ok := c.GetPath("shared"); !ok || v != "from-response" {
		t.Fatalf("precedence: got (%v,%v), want from-response", v, ok)
	}
	// Falls through to Input when higher roots miss.
	if v, ok := c.GetPath("only"); !ok || v != "input-only" {
		t.Fatalf("fallthrough: got (%v,%v), want input-only", v, ok)
	}
	// A missing path reports not-found.
	if _, ok := c.GetPath("nope"); ok {
		t.Fatal("expected not-found for missing path")
	}
}

func TestGetPath_DottedAndNumericDescent(t *testing.T) {
	c := NewCtx("req", "trace", "test", map[string]any{
		"order": map[string]any{
			"lines": []any{
				map[string]any{"sku": "A"},
				map[string]any{"sku": "B"},
			},
		},
	})
	if v, ok := c.GetPath("order.lines.1.sku"); !ok || v != "B" {
		t.Fatalf("descent: got (%v,%v), want B", v, ok)
	}
	// Descending into a scalar fails cleanly.
	if _, ok := c.GetPath("order.lines.0.sku.deeper"); ok {
		t.Fatal("expected not-found descending into a scalar")
	}
	// Out-of-range index fails cleanly.
	if _, ok := c.GetPath("order.lines.5.sku"); ok {
		t.Fatal("expected not-found for out-of-range index")
	}
}
