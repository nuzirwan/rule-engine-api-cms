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
	Params  []string `json:"params,omitempty"`
	Query   []string `json:"query,omitempty"`
	Headers []string `json:"headers,omitempty"`
	Body    bool     `json:"body,omitempty"`
}

// TriggerSpec is the flow entrypoint (the root, one per tree).
type TriggerSpec struct {
	Method string       `json:"method"`
	Path   string       `json:"path"`
	Input  TriggerInput `json:"input"`
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
}

// ConditionSpec is a binary branch via a ZEN decision (control node).
type ConditionSpec struct {
	JDMID    string   `json:"jdmId"`
	Input    []string `json:"input"`
	TrueKey  string   `json:"trueKey"`
	FalseKey string   `json:"falseKey"`
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
	TypeTrigger:   func(r json.RawMessage) error { _, e := parseSpec[TriggerSpec](r); return e },
	TypeAction:    func(r json.RawMessage) error { _, e := parseSpec[ActionSpec](r); return e },
	TypeCondition: func(r json.RawMessage) error { _, e := parseSpec[ConditionSpec](r); return e },
	TypeSwitch:    func(r json.RawMessage) error { _, e := parseSpec[SwitchSpec](r); return e },
	TypeSequence:  func(r json.RawMessage) error { _, e := parseSpec[SequenceSpec](r); return e },
	TypeParallel:  func(r json.RawMessage) error { _, e := parseSpec[ParallelSpec](r); return e },
	TypeForEach:   func(r json.RawMessage) error { _, e := parseSpec[ForEachSpec](r); return e },
	TypeDecision:  func(r json.RawMessage) error { _, e := parseSpec[DecisionSpec](r); return e },
	TypeSet:       func(r json.RawMessage) error { _, e := parseSpec[SetSpec](r); return e },
	TypeLogger:    func(r json.RawMessage) error { _, e := parseSpec[LoggerSpec](r); return e },
	TypeResponse:  func(r json.RawMessage) error { _, e := parseSpec[ResponseSpec](r); return e },
}
