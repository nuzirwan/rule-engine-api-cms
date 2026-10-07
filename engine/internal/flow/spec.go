package flow

import (
	"bytes"
	"encoding/json"

	"nzr-rules-engine/internal/connect"
)

// parseSpec decodes a node's raw Spec into T with unknown-field rejection, so a
// config carrying a field an older engine does not understand fails validation
// cleanly (forward-compat, R11) instead of being silently ignored. A decode
// error is classified ClassValidation.
func parseSpec[T any](raw json.RawMessage) (T, error) {
	var out T
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, wrapErr(ClassValidation, "spec decode", err)
	}
	return out, nil
}

// ConnRef names a config connection by key; the version is resolved by the
// pinned snapshot, not here.
type ConnRef struct {
	Connection string `json:"connection"`
}

// ResilienceOverride is the per-node resilience override surface (AC-7). A nil
// field means "inherit"; the thin slice honors TimeoutMS only.
type ResilienceOverride struct {
	TimeoutMS  *int  `json:"timeoutMs,omitempty"`
	Retries    *int  `json:"retries,omitempty"`
	BreakerOff *bool `json:"breakerOff,omitempty"`
}

// TriggerInput declares which parts of the request are mapped into Ctx.Input.
// The mapping itself is performed by httpapi before Run; this is the
// contract/validation surface.
type TriggerInput struct {
	Params  []string        `json:"params,omitempty"`
	Query   []string        `json:"query,omitempty"`
	Headers []string        `json:"headers,omitempty"`
	Body    bool            `json:"body,omitempty"`
	Schema  json.RawMessage `json:"schema,omitempty"`
}

// TriggerSpec is the flow entrypoint (the root, one per tree).
type TriggerSpec struct {
	Method string       `json:"method"`
	Path   string       `json:"path"`
	Input  TriggerInput `json:"input"`
}

// MessageTriggerSpec is the entrypoint for message-driven flows. When a message
// arrives via ConsumerManager, it drives a flow execution with the message as
// input. Implements at-least-once semantics: ack after the flow's required
// writes commit, seek-back on failure (handled by the consumer implementation).
type MessageTriggerSpec struct {
	// ConnectionKey identifies the consumer connection in the registry.
	ConnectionKey string `json:"connectionKey"`
	// Topic is the topic/queue to consume from.
	Topic string `json:"topic"`
	// FlowID is the flow this trigger belongs to (informational).
	FlowID string `json:"flowId"`
	// DeadLetterTopic is the optional topic for messages that fail after retries.
	DeadLetterTopic string `json:"deadLetterTopic,omitempty"`
}

// OnError selects how an action failure is handled: "fail" (default) aborts the
// walk; "continue" records the failure and keeps walking (R3).
type OnError string

// OnError values.
const (
	OnErrorFail     OnError = "fail"
	OnErrorContinue OnError = "continue"
)

// ActionSpec is a single I/O operation via a connection (leaf).
type ActionSpec struct {
	ConnRef
	Operation  connect.Operation   `json:"operation"`
	SaveAs     string              `json:"saveAs"`
	Resilience *ResilienceOverride `json:"resilience,omitempty"`
	OnError    OnError             `json:"onError,omitempty"`

	// IdempotencyKeyFrom is a template path that resolves to a unique key for
	// deduplication (R4). When non-empty the resolved value is copied into the
	// Operation.IdempotencyKey field; a non-idempotent write (exec/http POST/PUT/
	// DELETE) protected by this key is deduped across the dedup-store TTL window
	// so a replay within the window sees a short-circuit result instead of a
	// duplicate side effect.
	IdempotencyKeyFrom string `json:"idempotencyKeyFrom,omitempty"`

	// UnwrapSingleRow controls how multi-row query results are normalized. When
	// true (the default for backward compatibility), a single-row result is
	// unwrapped to its map representation so "saveAs.field" paths resolve directly.
	// When explicitly false, results are always returned as an array regardless of
	// row count. This removes the demo-tuned heuristic that assumed single-row
	// queries (e.g. orders/{id}).
	UnwrapSingleRow *bool `json:"unwrapSingleRow,omitempty"`
}

// ConditionSpec is a binary branch via a ZEN decision (control node).
type ConditionSpec struct {
	JDMID    string   `json:"jdmId"`
	Input    []string `json:"input"`
	TrueKey  string   `json:"trueKey"`
	FalseKey string   `json:"falseKey"`

	// BranchField names the decision output field to read for branching. When set,
	// the condition reads this specific field from the decision output instead of
	// relying on the generic "branch" or "result" convention. This removes the
	// demo-tuned heuristic that assumed a single-string output (e.g. {"shipping":
	// "expedite"}) could be auto-promoted to branch.
	BranchField string `json:"branchField,omitempty"`
}

// SetSpec mounts a value or a resolved path into Response (leaf). Exactly one of
// Value / From is set.
type SetSpec struct {
	TargetPath string `json:"targetPath"`
	Value      any    `json:"value,omitempty"`
	From       string `json:"from,omitempty"`
	OmitEmpty  bool   `json:"omitEmpty,omitempty"`
}

// ResponseSpec finalizes and stops the walk (leaf). BodyFrom "" returns
// Response as-is; otherwise it names a dotted path.
type ResponseSpec struct {
	Status   int    `json:"status"`
	BodyFrom string `json:"bodyFrom,omitempty"`
}

// --- deferred-type specs: declared so the registry can name them and strict
// decode still rejects unknown fields; the handlers refuse at runtime. ---

// SwitchSpec is the N-way branch spec (deferred in the thin slice).
type SwitchSpec struct {
	JDMID   string            `json:"jdmId"`
	Input   []string          `json:"input"`
	Cases   map[string]string `json:"cases"`
	Default string            `json:"default"`
}

// SequenceSpec runs children in order (deferred in the thin slice).
type SequenceSpec struct {
	StopOnError *bool `json:"stopOnError,omitempty"`
}

// ParallelSpec runs children concurrently (deferred in the thin slice).
type ParallelSpec struct {
	MaxConcurrency int  `json:"maxConcurrency,omitempty"`
	FailFast       bool `json:"failFast,omitempty"`
}

// ForEachSpec iterates a collection (deferred in the thin slice).
type ForEachSpec struct {
	Over     string `json:"over"`
	As       string `json:"as"`
	IndexAs  string `json:"indexAs,omitempty"`
	MaxItems int    `json:"maxItems"`
	Parallel bool   `json:"parallel,omitempty"`
}

// DecisionSpec computes values via ZEN without branching (deferred in the thin slice).
type DecisionSpec struct {
	JDMID  string   `json:"jdmId"`
	Input  []string `json:"input"`
	SaveAs string   `json:"saveAs"`
}

// FilterSpec filters an array, keeping items where the ZEN predicate returns a
// truthy "match" field. The filtered array is stored under SaveAs in Ctx.Data.
type FilterSpec struct {
	Over     string   `json:"over"`     // path to source array
	JDMID    string   `json:"jdmId"`    // ZEN decision for predicate
	Input    []string `json:"input"`    // fields from each item to pass to predicate
	SaveAs   string   `json:"saveAs"`   // where to store filtered array
	MaxItems int      `json:"maxItems"` // budget guard (AC-17)
}

// FindSpec finds the first item in an array where the ZEN predicate returns a
// truthy "match" field. The found item (or null) is stored under SaveAs in Ctx.Data.
type FindSpec struct {
	Over     string   `json:"over"`     // path to source array
	JDMID    string   `json:"jdmId"`    // ZEN decision for predicate
	Input    []string `json:"input"`    // fields from each item to pass to predicate
	SaveAs   string   `json:"saveAs"`   // where to store found item
	MaxItems int      `json:"maxItems"` // budget guard (AC-17)
}

// MapSpec transforms each item in an array via a ZEN decision. For each item,
// the ZEN receives the item (or projected fields) and returns the transformed
// value. The output array has the same length as the input. It is a leaf node.
type MapSpec struct {
	Over     string   `json:"over"`     // path to source array (required)
	JDMID    string   `json:"jdmId"`    // ZEN decision for transformation (required)
	Input    []string `json:"input"`    // fields from each item to pass to ZEN (optional)
	SaveAs   string   `json:"saveAs"`   // where to store transformed array (required)
	MaxItems int      `json:"maxItems"` // budget guard, required > 0 (AC-17)
}

// ReduceSpec aggregates an array to a single value via a ZEN decision. For each
// item, the ZEN receives {"accumulator": <acc>, "current": <item>, "index": <n>}
// and returns the new accumulator. The final accumulator is stored at SaveAs.
// It is a leaf node.
type ReduceSpec struct {
	Over         string   `json:"over"`                   // path to source array (required)
	JDMID        string   `json:"jdmId"`                  // ZEN decision for reducer (required)
	Input        []string `json:"input"`                  // fields from current item to project (optional)
	SaveAs       string   `json:"saveAs"`                 // where to store final accumulator (required)
	MaxItems     int      `json:"maxItems"`               // budget guard, required > 0 (AC-17)
	InitialValue any      `json:"initialValue,omitempty"` // starting accumulator, defaults to nil
}

// LoggerSpec is an optional debug point (deferred in the thin slice).
type LoggerSpec struct {
	Label      string   `json:"label"`
	Level      string   `json:"level"`
	Capture    []string `json:"capture"`
	SampleRate *float64 `json:"sampleRate,omitempty"`
}

// specValidators drives DisallowUnknownFields validation per node type. Every
// node type has an entry so an unknown field in any spec is rejected cleanly.
var specValidators = map[NodeType]func(json.RawMessage) error{
	TypeTrigger:        func(r json.RawMessage) error { _, e := parseSpec[TriggerSpec](r); return e },
	TypeMessageTrigger: func(r json.RawMessage) error { _, e := parseSpec[MessageTriggerSpec](r); return e },
	TypeAction:         func(r json.RawMessage) error { _, e := parseSpec[ActionSpec](r); return e },
	TypeCondition:      func(r json.RawMessage) error { _, e := parseSpec[ConditionSpec](r); return e },
	TypeSwitch:         func(r json.RawMessage) error { _, e := parseSpec[SwitchSpec](r); return e },
	TypeSequence:       func(r json.RawMessage) error { _, e := parseSpec[SequenceSpec](r); return e },
	TypeParallel:       func(r json.RawMessage) error { _, e := parseSpec[ParallelSpec](r); return e },
	TypeForEach:        func(r json.RawMessage) error { _, e := parseSpec[ForEachSpec](r); return e },
	TypeDecision:       func(r json.RawMessage) error { _, e := parseSpec[DecisionSpec](r); return e },
	TypeFilter:         func(r json.RawMessage) error { _, e := parseSpec[FilterSpec](r); return e },
	TypeFind:           func(r json.RawMessage) error { _, e := parseSpec[FindSpec](r); return e },
	TypeMap:            func(r json.RawMessage) error { _, e := parseSpec[MapSpec](r); return e },
	TypeReduce:         func(r json.RawMessage) error { _, e := parseSpec[ReduceSpec](r); return e },
	TypeSet:            func(r json.RawMessage) error { _, e := parseSpec[SetSpec](r); return e },
	TypeLogger:         func(r json.RawMessage) error { _, e := parseSpec[LoggerSpec](r); return e },
	TypeResponse:       func(r json.RawMessage) error { _, e := parseSpec[ResponseSpec](r); return e },
}
