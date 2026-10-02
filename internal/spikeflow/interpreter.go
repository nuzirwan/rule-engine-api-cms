package spikeflow

import (
	"context"
	"fmt"
	"strings"
)

// Ctx is the per-request context accumulator (trimmed Slice A §4): three maps,
// Response written only via SetPath, reads over a merged Response->Data->Input view.
type Ctx struct {
	Input    map[string]any // seeded by Trigger, read-only after
	Data     map[string]any // per-node outputs via SaveAs
	Response map[string]any // the stitched output, written only via SetPath
}

// NewCtx builds a Ctx seeded with the trigger input.
func NewCtx(input map[string]any) *Ctx {
	if input == nil {
		input = map[string]any{}
	}
	return &Ctx{Input: input, Data: map[string]any{}, Response: map[string]any{}}
}

// GetPath reads a dotted path from the merged view (Response -> Data -> Input).
func (c *Ctx) GetPath(path string) (any, bool) {
	segs := strings.Split(path, ".")
	for _, root := range []map[string]any{c.Response, c.Data, c.Input} {
		if v, ok := dig(root, segs); ok {
			return v, true
		}
	}
	return nil, false
}

func dig(root map[string]any, segs []string) (any, bool) {
	var cur any = root
	for _, seg := range segs {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// SetPath writes v at a dotted path into Response. Intermediate maps are created;
// only the leaf is assigned (no sibling clobber). A scalar in an intermediate
// position is a conflict error (trimmed Slice A §4 rules 1-4; no numeric indices).
func (c *Ctx) SetPath(path string, v any) error {
	segs := strings.Split(path, ".")
	if len(segs) == 0 || segs[0] == "" {
		return fmt.Errorf("spikeflow: empty targetPath")
	}
	cur := c.Response
	for _, seg := range segs[:len(segs)-1] {
		child, ok := cur[seg]
		if !ok {
			nm := map[string]any{}
			cur[seg] = nm
			cur = nm
			continue
		}
		nm, ok := child.(map[string]any)
		if !ok {
			return fmt.Errorf("spikeflow: path conflict at %q", seg)
		}
		cur = nm
	}
	cur[segs[len(segs)-1]] = v
	return nil
}

// Interpreter walks a spike flow tree. Stateless; per-request state is in Ctx.
type Interpreter struct{}

// New returns a spike interpreter.
func New() *Interpreter { return &Interpreter{} }

// Run walks the tree from the root (a Trigger) to a Response, synchronously.
func (ip *Interpreter) Run(ctx context.Context, root *Node, c *Ctx) error {
	stopped, err := ip.walk(ctx, root, c)
	if err != nil {
		return err
	}
	if !stopped {
		return fmt.Errorf("spikeflow: flow completed without a Response node")
	}
	return nil
}

// walk returns stopped=true once a Response node has run (short-circuit upward).
func (ip *Interpreter) walk(ctx context.Context, n *Node, c *Ctx) (stopped bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	switch n.Type {
	case TypeTrigger:
		// Trigger: a linear parent; walk its children in order until one stops.
		return ip.walkChildren(ctx, n.Children, c)

	case TypeAction:
		res, err := n.Action(ctx, c)
		if err != nil {
			return false, fmt.Errorf("action %q: %w", n.ID, err)
		}
		if n.SaveAs != "" {
			c.Data[n.SaveAs] = res
		}
		// An Action is linear: after its side effect, walk its children in order.
		return ip.walkChildren(ctx, n.Children, c)

	case TypeCondition:
		input := map[string]any{}
		for _, p := range n.Inputs {
			if v, ok := c.GetPath(p); ok {
				input[lastSeg(p)] = v
			}
		}
		out, err := n.Decide(ctx, input)
		if err != nil {
			return false, fmt.Errorf("condition %q decide: %w", n.ID, err)
		}
		branch, _ := out[n.BranchKey].(string)
		// Stash the decision output so a downstream Set can reference it.
		c.Data["decision"] = out

		childID := n.FalseKey
		if branch == n.TrueKey {
			childID = n.TrueKey
		}
		child := findChild(n.Children, childID)
		if child == nil {
			return false, fmt.Errorf("condition %q: no child for branch %q", n.ID, childID)
		}
		return ip.walk(ctx, child, c)

	case TypeSet:
		val := n.Value
		if n.FromPath != "" {
			v, ok := c.GetPath(n.FromPath)
			if !ok {
				return false, fmt.Errorf("set %q: path %q not found", n.ID, n.FromPath)
			}
			val = v
		}
		if err := c.SetPath(n.TargetPath, val); err != nil {
			return false, fmt.Errorf("set %q: %w", n.ID, err)
		}
		return ip.walkChildren(ctx, n.Children, c)

	case TypeResponse:
		return true, nil

	default:
		return false, fmt.Errorf("spikeflow: unknown node type %q", n.Type)
	}
}

func (ip *Interpreter) walkChildren(ctx context.Context, children []*Node, c *Ctx) (bool, error) {
	for _, ch := range children {
		stopped, err := ip.walk(ctx, ch, c)
		if err != nil {
			return false, err
		}
		if stopped {
			return true, nil
		}
	}
	return false, nil
}

func findChild(children []*Node, id string) *Node {
	for _, ch := range children {
		if ch.ID == id {
			return ch
		}
	}
	return nil
}

func lastSeg(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}
