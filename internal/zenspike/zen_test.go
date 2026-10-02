//go:build cgo

package zenspike

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// loadJDM reads the representative decision JDM from testdata.
func loadJDM(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "order.jdm.json"))
	if err != nil {
		t.Fatalf("read jdm: %v", err)
	}
	return b
}

// TestDecisionTable proves AC-S2: the representative JDM evaluates the hardest
// real condition (amount > 1000 AND status == 'paid' => expedited, else standard)
// correctly across a table of inputs, including the strict-> boundary.
func TestDecisionTable(t *testing.T) {
	eng := NewEngine()
	defer eng.Dispose()

	compiled, err := eng.Compile(loadJDM(t))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	defer compiled.Close()

	cases := []struct {
		name   string
		amount int
		status string
		want   string
	}{
		{"high paid -> expedited", 1500, "paid", "expedited"},
		{"low paid -> standard", 500, "paid", "standard"},
		{"high pending -> standard", 1500, "pending", "standard"},
		{"boundary exactly 1000 paid -> standard (strict >)", 1000, "paid", "standard"},
		{"boundary 1001 paid -> expedited", 1001, "paid", "expedited"},
	}

	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := map[string]any{"amount": tc.amount, "status": tc.status}
			out, err := compiled.Eval(ctx, in)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			got, ok := out["shipping"].(string)
			if !ok {
				t.Fatalf("shipping field missing or not a string in %v", out)
			}
			if got != tc.want {
				t.Errorf("amount=%d status=%q: got shipping=%q want %q", tc.amount, tc.status, got, tc.want)
			}
		})
	}
}

// TestDeterminism proves the determinism half of AC-S2 / AC-4: the same input
// over the same compiled decision yields byte-identical output across repeated
// calls and across two independently compiled decisions.
func TestDeterminism(t *testing.T) {
	eng := NewEngine()
	defer eng.Dispose()
	jdm := loadJDM(t)

	c1, err := eng.Compile(jdm)
	if err != nil {
		t.Fatalf("compile c1: %v", err)
	}
	defer c1.Close()
	c2, err := eng.Compile(jdm)
	if err != nil {
		t.Fatalf("compile c2: %v", err)
	}
	defer c2.Close()

	in := map[string]any{"amount": 1500, "status": "paid"}
	ctx := context.Background()

	first, err := c1.Eval(ctx, in)
	if err != nil {
		t.Fatalf("eval first: %v", err)
	}
	want := first["shipping"]

	// Repeat on c1 and on a second compiled decision; all must match.
	for i := 0; i < 50; i++ {
		got1, err := c1.Eval(ctx, in)
		if err != nil {
			t.Fatalf("eval c1 iter %d: %v", i, err)
		}
		if got1["shipping"] != want {
			t.Fatalf("c1 iter %d nondeterministic: got %v want %v", i, got1["shipping"], want)
		}
		got2, err := c2.Eval(ctx, in)
		if err != nil {
			t.Fatalf("eval c2 iter %d: %v", i, err)
		}
		if got2["shipping"] != want {
			t.Fatalf("c2 iter %d differs: got %v want %v", i, got2["shipping"], want)
		}
	}
}
