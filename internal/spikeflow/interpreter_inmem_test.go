package spikeflow

import (
	"context"
	"testing"
)

// TestWalkInMemory exercises the interpreter with stub actions and a stub
// decision — no CGO, no Docker. It proves the tree-walk, branch selection,
// SetPath stitching, and short-circuit on Response.
func TestWalkInMemory(t *testing.T) {
	calledEndpoint := ""

	// stub pg read action: seeds the order into ctx.Data["order"].
	readOrder := func(amount int, status string) ActionFunc {
		return func(ctx context.Context, c *Ctx) (map[string]any, error) {
			return map[string]any{"amount": amount, "status": status}, nil
		}
	}
	// stub REST actions record which endpoint ran.
	restAction := func(name string) ActionFunc {
		return func(ctx context.Context, c *Ctx) (map[string]any, error) {
			calledEndpoint = name
			return map[string]any{"endpoint": name}, nil
		}
	}
	// stub decision: amount>1000 AND status==paid => expedited else standard.
	decide := func(ctx context.Context, in map[string]any) (map[string]any, error) {
		amt, _ := in["amount"].(int)
		st, _ := in["status"].(string)
		shipping := "standard"
		if amt > 1000 && st == "paid" {
			shipping = "expedited"
		}
		return map[string]any{"shipping": shipping}, nil
	}

	build := func(amount int, status string) *Node {
		expedite := &Node{ID: "expedited", Type: TypeAction, Action: restAction("expedite"), SaveAs: "rest",
			Children: []*Node{{ID: "set-e", Type: TypeSet, TargetPath: "shipping", FromPath: "decision.shipping",
				Children: []*Node{{ID: "resp-e", Type: TypeResponse}}}}}
		standard := &Node{ID: "standard", Type: TypeAction, Action: restAction("standard"), SaveAs: "rest",
			Children: []*Node{{ID: "set-s", Type: TypeSet, TargetPath: "shipping", FromPath: "decision.shipping",
				Children: []*Node{{ID: "resp-s", Type: TypeResponse}}}}}
		cond := &Node{ID: "cond", Type: TypeCondition, Decide: decide,
			Inputs:    []string{"order.amount", "order.status"},
			BranchKey: "shipping", TrueKey: "expedited", FalseKey: "standard",
			Children: []*Node{expedite, standard}}
		pg := &Node{ID: "pg", Type: TypeAction, Action: readOrder(amount, status), SaveAs: "order",
			Children: []*Node{cond}}
		return &Node{ID: "trigger", Type: TypeTrigger, Children: []*Node{pg}}
	}

	cases := []struct {
		name         string
		amount       int
		status       string
		wantShipping string
		wantEndpoint string
	}{
		{"expedited", 1500, "paid", "expedited", "expedite"},
		{"standard", 500, "paid", "standard", "standard"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calledEndpoint = ""
			c := NewCtx(map[string]any{})
			if err := New().Run(context.Background(), build(tc.amount, tc.status), c); err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := c.Response["shipping"]; got != tc.wantShipping {
				t.Errorf("response shipping = %v want %v", got, tc.wantShipping)
			}
			if calledEndpoint != tc.wantEndpoint {
				t.Errorf("endpoint = %q want %q", calledEndpoint, tc.wantEndpoint)
			}
		})
	}
}

// TestSetPathNoClobber proves the SetPath sibling-coexistence rule the response
// stitching depends on.
func TestSetPathNoClobber(t *testing.T) {
	c := NewCtx(nil)
	must := func(err error) {
		if err != nil {
			t.Fatalf("setpath: %v", err)
		}
	}
	must(c.SetPath("order.id", 42))
	must(c.SetPath("order.total", 99))
	must(c.SetPath("flags.expedited", true))

	order, ok := c.Response["order"].(map[string]any)
	if !ok || order["id"] != 42 || order["total"] != 99 {
		t.Fatalf("sibling clobber: %v", c.Response)
	}
	flags, ok := c.Response["flags"].(map[string]any)
	if !ok || flags["expedited"] != true {
		t.Fatalf("new subtree missing: %v", c.Response)
	}

	// A scalar in an intermediate position is a conflict.
	if err := c.SetPath("order.id.deeper", 1); err == nil {
		t.Fatalf("expected path conflict error")
	}
}
