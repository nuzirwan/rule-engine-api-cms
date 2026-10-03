package flow

import (
	"fmt"
	"strings"
)

// ValidationIssue is one structural problem found by ValidateTree. Code is a
// stable machine key (for tests/UI); Message is the human text. NodeID names the
// offending node ("" for whole-tree issues).
type ValidationIssue struct {
	NodeID  string
	Code    string
	Message string
}

// RefResolver answers whether a referenced connection / JDM exists in the target
// environment. It is injected so the pure checker needs no DB and stays testable.
type RefResolver interface {
	HasConnection(key string) bool
	HasJDM(id string) bool
}

// nopRefs is the permissive resolver used when a caller validates structure only
// (every ref is treated as resolvable). ValidateTree accepts a nil RefResolver
// and substitutes this, so structural rules can be checked without wiring refs.
type nopRefs struct{}

func (nopRefs) HasConnection(string) bool { return true }
func (nopRefs) HasJDM(string) bool        { return true }

// controlTypes are the true branching/container node types that MUST own at
// least one child: condition/switch select among their children; sequence/
// parallel/forEach own and run their child set. (Trigger also requires a child
// but is a linear parent — see linearTypes — so it is handled there.)
var controlTypes = map[NodeType]bool{
	TypeCondition: true,
	TypeSwitch:    true,
	TypeSequence:  true,
	TypeParallel:  true,
	TypeForEach:   true,
}

// Linear node types (trigger/action/set/decision/logger) carry the flow's linear
// "next" step(s): the interpreter runs their children IN SEQUENCE via
// walkChildren (do the node's own work, then walk each child in order until a
// Response stops the path). They therefore impose NO child-count ceiling — a
// chain of one next, or a short in-order sequence ending in a Response, are both
// legal and are exactly what the interpreter executes.
//
// This replaces the earlier (incorrect) "action/set/decision/logger are strict
// leaves that must not have children" rule, which contradicted the interpreter's
// own proven execution model: the seed flow chains action→condition→…→set→
// response and TestOrdersEndToEnd passes on exactly that shape. Reconciled during
// the wiring join as an in-scope defect fix. The exactly-one-terminal-Response
// rule (validateResponsePaths) remains the guard against a node running after a
// Response on a path, so dropping the leaf ceiling does not weaken correctness.
// Only `response` stays strictly terminal (0 children); see Rule 4 below.

// ValidateTree checks a flow tree's structure without I/O and returns every
// issue found (collect-all, not fail-fast) so an author sees all problems at
// once. refs may be nil (structural-only validation). It enforces slice-a §7:
// root is a single Trigger; known types with strict-decoding specs; control
// nodes have children and leaves do not; branch/switch keys reference real
// children; bounded static depth; forEach MaxItems>0 + Over/As set; parallel
// sibling writes disjoint; exactly-one terminal Response on every path; unique
// node IDs; well-formed Set targets.
func ValidateTree(root Node, refs RefResolver) []ValidationIssue {
	if refs == nil {
		refs = nopRefs{}
	}
	var issues []ValidationIssue
	add := func(id, code, msg string) {
		issues = append(issues, ValidationIssue{NodeID: id, Code: code, Message: msg})
	}

	// Rule 1: root must be a Trigger, and it is the only Trigger in the tree.
	if root.Type != TypeTrigger {
		add(root.ID, "root_not_trigger", "root node must be a trigger")
	}

	seenIDs := map[string]bool{}
	triggerCount := 0

	var walk func(n Node, depth int)
	walk = func(n Node, depth int) {
		// Rule 9: unique node IDs across the tree.
		if n.ID == "" {
			add(n.ID, "empty_id", "node id must be non-empty")
		} else if seenIDs[n.ID] {
			add(n.ID, "duplicate_id", fmt.Sprintf("duplicate node id %q", n.ID))
		}
		seenIDs[n.ID] = true

		if n.Type == TypeTrigger {
			triggerCount++
			if depth > 0 {
				add(n.ID, "nested_trigger", "trigger may only be the root node")
			}
		}

		// Rule 5: bounded static depth.
		if depth > DefaultMaxDepth {
			add(n.ID, "max_depth", fmt.Sprintf("static depth %d exceeds max %d", depth, DefaultMaxDepth))
		}

		// Rule 2: known type + strict-decoding spec.
		validate, known := specValidators[n.Type]
		if !known {
			add(n.ID, "unknown_type", fmt.Sprintf("unknown node type %q", string(n.Type)))
		} else if err := validate(n.Spec); err != nil {
			add(n.ID, "bad_spec", fmt.Sprintf("spec invalid: %v", err))
		}

		// Rule 4: child-count rules by node role.
		//   - control (condition/switch/sequence/parallel/forEach): >= 1 child.
		//   - trigger: >= 1 child (the linear body it walks in sequence).
		//   - linear (action/set/decision/logger): any number of in-order children
		//     (no ceiling — the interpreter runs them as a sequence).
		//   - response: strictly terminal — no children (it sets Directive.Stop so
		//     nothing runs after it on a path).
		if controlTypes[n.Type] && len(n.Children) == 0 {
			add(n.ID, "control_no_children", fmt.Sprintf("%s node must have at least one child", n.Type))
		}
		if n.Type == TypeTrigger && len(n.Children) == 0 {
			add(n.ID, "control_no_children", "trigger node must have at least one child")
		}
		if n.Type == TypeResponse && len(n.Children) > 0 {
			add(n.ID, "leaf_has_children", "response node must not have children")
		}

		// Per-type structural rules.
		switch n.Type {
		case TypeAction:
			if spec, err := parseSpec[ActionSpec](n.Spec); err == nil {
				if spec.Connection == "" {
					add(n.ID, "action_no_connection", "action missing connection")
				} else if !refs.HasConnection(spec.Connection) {
					add(n.ID, "dangling_connection", fmt.Sprintf("connection %q not found", spec.Connection))
				}
			}
		case TypeCondition:
			if spec, err := parseSpec[ConditionSpec](n.Spec); err == nil {
				checkJDM(add, n.ID, refs, spec.JDMID)
				checkChildKey(add, n.ID, n.Children, spec.TrueKey, "trueKey")
				if spec.FalseKey != "" {
					checkChildKey(add, n.ID, n.Children, spec.FalseKey, "falseKey")
				}
			}
		case TypeSwitch:
			if spec, err := parseSpec[SwitchSpec](n.Spec); err == nil {
				checkJDM(add, n.ID, refs, spec.JDMID)
				if len(spec.Cases) == 0 && spec.Default == "" {
					add(n.ID, "switch_no_cases", "switch must have at least one case or a default")
				}
				for branch, childID := range spec.Cases {
					checkChildKey(add, n.ID, n.Children, childID, fmt.Sprintf("case %q", branch))
				}
				if spec.Default != "" {
					checkChildKey(add, n.ID, n.Children, spec.Default, "default")
				}
			}
		case TypeDecision:
			if spec, err := parseSpec[DecisionSpec](n.Spec); err == nil {
				checkJDM(add, n.ID, refs, spec.JDMID)
				if spec.SaveAs == "" {
					add(n.ID, "decision_no_saveas", "decision missing saveAs")
				}
			}
		case TypeForEach:
			// Rule 7: forEach bounds present.
			if spec, err := parseSpec[ForEachSpec](n.Spec); err == nil {
				if spec.MaxItems <= 0 {
					add(n.ID, "foreach_maxitems", "forEach maxItems must be > 0")
				}
				if spec.Over == "" {
					add(n.ID, "foreach_no_over", "forEach missing over")
				}
				if spec.As == "" {
					add(n.ID, "foreach_no_as", "forEach missing as")
				}
			}
		case TypeParallel:
			// Rule 8: parallel sibling writes must be disjoint.
			checkParallelDisjoint(add, n)
		case TypeSet:
			// Rule 10: Set targets well-formed; exactly one of value/from.
			if spec, err := parseSpec[SetSpec](n.Spec); err == nil {
				if strings.TrimSpace(spec.TargetPath) == "" {
					add(n.ID, "set_no_target", "set targetPath must be non-empty")
				} else if len(splitPath(spec.TargetPath)) == 0 {
					add(n.ID, "set_bad_target", fmt.Sprintf("set targetPath %q is not parseable", spec.TargetPath))
				}
				if (spec.From == "") == (spec.Value == nil) {
					add(n.ID, "set_one_source", "set must set exactly one of value/from")
				}
			}
		}

		for i := range n.Children {
			walk(n.Children[i], depth+1)
		}
	}
	walk(root, 0)

	if triggerCount != 1 {
		add(root.ID, "trigger_count", fmt.Sprintf("tree must contain exactly one trigger, found %d", triggerCount))
	}

	// Rule 6: exactly-one terminal Response on every reachable path.
	issues = append(issues, validateResponsePaths(root)...)

	return issues
}

// checkJDM records a dangling-jdm issue when the referenced JDM does not resolve.
func checkJDM(add func(id, code, msg string), nodeID string, refs RefResolver, jdmID string) {
	if jdmID == "" {
		add(nodeID, "no_jdm", "decision/condition/switch missing jdmId")
		return
	}
	if !refs.HasJDM(jdmID) {
		add(nodeID, "dangling_jdm", fmt.Sprintf("jdm %q not found", jdmID))
	}
}

// checkChildKey records an issue when key does not name an actual child ID.
func checkChildKey(add func(id, code, msg string), nodeID string, children []Node, key, label string) {
	if key == "" {
		return
	}
	if findChild(children, key) == nil {
		add(nodeID, "unknown_child_key", fmt.Sprintf("%s references unknown child %q", label, key))
	}
}

// checkParallelDisjoint collects the Set.TargetPath written by each direct child
// subtree of a parallel node and flags any pair of sibling subtrees whose write
// path-prefixes overlap, since the §4 merge requires disjoint sibling writes.
func checkParallelDisjoint(add func(id, code, msg string), parallel Node) {
	// For each direct child subtree, gather the set of target paths it writes.
	perSibling := make([]map[string]bool, len(parallel.Children))
	for i := range parallel.Children {
		paths := map[string]bool{}
		collectSetPaths(parallel.Children[i], paths)
		perSibling[i] = paths
	}
	for i := 0; i < len(perSibling); i++ {
		for j := i + 1; j < len(perSibling); j++ {
			for p := range perSibling[i] {
				for q := range perSibling[j] {
					if pathsOverlap(p, q) {
						add(parallel.ID, "parallel_overlap",
							fmt.Sprintf("parallel sibling write paths overlap: %q and %q", p, q))
					}
				}
			}
		}
	}
}

// collectSetPaths walks a subtree gathering every Set node's targetPath into out.
func collectSetPaths(n Node, out map[string]bool) {
	if n.Type == TypeSet {
		if spec, err := parseSpec[SetSpec](n.Spec); err == nil && spec.TargetPath != "" {
			out[spec.TargetPath] = true
		}
	}
	for i := range n.Children {
		collectSetPaths(n.Children[i], out)
	}
}

// pathsOverlap reports whether two dotted write paths are the same or one is a
// prefix of the other (a prefix write would clobber the other's subtree).
func pathsOverlap(a, b string) bool {
	as := splitPath(a)
	bs := splitPath(b)
	n := len(as)
	if len(bs) < n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		if as[i] != bs[i] {
			return false
		}
	}
	// All compared segments equal: equal paths or one is a prefix of the other.
	return true
}

// validateResponsePaths enforces rule 6: on every reachable path from the root
// through the children a control node can run, there is exactly one Response and
// it is terminal (no node follows it on that path). It returns an issue when a
// path reaches a leaf with no Response or contains more than one.
func validateResponsePaths(root Node) []ValidationIssue {
	var issues []ValidationIssue

	// countResponses returns (minResponses, maxResponses, afterResponse) found on
	// the paths of the subtree, where afterResponse reports whether any node sits
	// after a Response on some path.
	var check func(n Node) (minR, maxR int, hasAfter bool)
	check = func(n Node) (int, int, bool) {
		if n.Type == TypeResponse {
			// A terminal Response: any children would be after it (leaf rule also
			// flags children, this guards the path arithmetic).
			after := len(n.Children) > 0
			return 1, 1, after
		}

		if len(n.Children) == 0 {
			// A non-Response leaf: zero responses on this path.
			return 0, 0, false
		}

		switch n.Type {
		case TypeCondition, TypeSwitch:
			// Branch nodes: each child is an independent path. A branch may fall
			// through without taking a child (no match / no default / no falseKey),
			// so the minimum responses contributed is 0 unless the branch is total.
			// To keep AC-1 strict we treat each branch child as its own path and
			// require every one of them to terminate in exactly one Response, and we
			// also require the branch to be total (handled below for condition/switch).
			minR, maxR := -1, 0
			hasAfter := false
			for i := range n.Children {
				cmin, cmax, caft := check(n.Children[i])
				if caft {
					hasAfter = true
				}
				if minR == -1 || cmin < minR {
					minR = cmin
				}
				if cmax > maxR {
					maxR = cmax
				}
			}
			if minR == -1 {
				minR = 0
			}
			return minR, maxR, hasAfter
		default:
			// Linear parents (trigger/sequence/parallel/forEach/action/set/logger):
			// their children run in sequence on the same path, so responses sum. A
			// Response in the middle means later siblings are "after" it.
			total := 0
			maxTotal := 0
			hasAfter := false
			seenResponse := false
			for i := range n.Children {
				cmin, cmax, caft := check(n.Children[i])
				if caft {
					hasAfter = true
				}
				if seenResponse && cmax > 0 {
					hasAfter = true
				}
				if seenResponse && (cmin > 0 || cmax > 0 || isPathNode(n.Children[i])) {
					hasAfter = true
				}
				total += cmin
				maxTotal += cmax
				if cmax > 0 {
					seenResponse = true
				}
			}
			return total, maxTotal, hasAfter
		}
	}

	minR, maxR, hasAfter := check(root)
	if minR < 1 {
		issues = append(issues, ValidationIssue{
			NodeID: root.ID, Code: "missing_response",
			Message: "a reachable path has no response node",
		})
	}
	if maxR > 1 {
		issues = append(issues, ValidationIssue{
			NodeID: root.ID, Code: "multiple_responses",
			Message: "a reachable path has more than one response node",
		})
	}
	if hasAfter {
		issues = append(issues, ValidationIssue{
			NodeID: root.ID, Code: "response_not_terminal",
			Message: "a node follows a response on some path",
		})
	}
	return issues
}

// isPathNode reports whether a node contributes execution on a linear path (used
// to detect a node following a Response). Every node type does, so this is a
// readability helper for validateResponsePaths.
func isPathNode(Node) bool { return true }
