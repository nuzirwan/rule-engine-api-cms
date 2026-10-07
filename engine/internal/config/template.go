package config

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"nzr-rules-engine/internal/flow"
)

//go:embed templates/*.json
var builtinTemplateFS embed.FS

// TemplateVariable describes one variable placeholder in a flow template.
type TemplateVariable struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // "string" | "int" | "boolean"
	Description string `json:"description"`
	Default     string `json:"default,omitempty"`
	Optional    bool   `json:"optional,omitempty"`
}

// TemplateFlowDef is one flow entry inside a template. Tree is kept as
// json.RawMessage because it may contain {{VAR}} placeholders that make it
// invalid JSON until after substitution.
type TemplateFlowDef struct {
	FlowID string          `json:"flowId"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Tree   json.RawMessage `json:"tree"`
}

// Template is the parsed schema for one built-in flow template.
type Template struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Category    string             `json:"category"`
	Variables   []TemplateVariable `json:"variables"`
	Flows       []TemplateFlowDef  `json:"flows"`
}

// varUnsafe rejects variable values that would break a JSON string.
var varUnsafe = regexp.MustCompile(`["\\\x00-\x1f]`)

// SubstituteTemplate resolves variable placeholders in t and returns one
// FlowVersion per flow definition in the template. Caller-supplied vars
// override template defaults. Missing required variables, JSON-unsafe values,
// or structurally invalid trees (checked via flow.ValidateTree) return a
// Validation error.
//
// Returns FlowVersion with Version=0 and Fixtures=nil. Version is assigned
// by PutFlowVersion when stored. Callers must not store the returned values
// directly without going through PutFlowVersion.
func SubstituteTemplate(t Template, vars map[string]string) ([]FlowVersion, error) {
	// 1. Resolve variables (supplied → default → error).
	resolved := make(map[string]string, len(t.Variables))
	for _, v := range t.Variables {
		val, ok := vars[v.Name]
		if !ok || val == "" {
			val = v.Default
		}
		if val == "" && !v.Optional {
			return nil, newErr(Validation, fmt.Sprintf("required template variable %q not set", v.Name))
		}
		if varUnsafe.MatchString(val) {
			return nil, newErr(Validation, fmt.Sprintf("template variable %q value is unsafe for JSON strings", v.Name))
		}
		resolved[v.Name] = val
	}

	// 2. Substitute each flow.
	out := make([]FlowVersion, 0, len(t.Flows))
	for _, fd := range t.Flows {
		raw := string(fd.Tree)
		flowID := fd.FlowID
		method := fd.Method
		path := fd.Path
		for k, v := range resolved {
			placeholder := "{{" + k + "}}"
			raw = strings.ReplaceAll(raw, placeholder, v)
			flowID = strings.ReplaceAll(flowID, placeholder, v)
			method = strings.ReplaceAll(method, placeholder, v)
			path = strings.ReplaceAll(path, placeholder, v)
		}

		// 3. Confirm the substituted value is syntactically valid JSON.
		var tree flow.Node
		if err := json.Unmarshal([]byte(raw), &tree); err != nil {
			return nil, wrapErr(Validation, fmt.Sprintf(
				"template %q flow %q tree invalid after substitution", t.ID, fd.FlowID), err)
		}

		// 4. Validate structural correctness (trigger, specs, no-duplicate IDs,
		//    one terminal response per path, etc.). Pass nil RefResolver for
		//    structural-only validation — connection/JDM refs are not checked
		//    at instantiation time (the store is not available here).
		if issues := flow.ValidateTree(tree, nil); len(issues) != 0 {
			msgs := make([]string, len(issues))
			for i, iss := range issues {
				msgs[i] = fmt.Sprintf("[%s] %s", iss.Code, iss.Message)
			}
			return nil, newErr(Validation, fmt.Sprintf(
				"template %q flow %q tree invalid after substitution: %s",
				t.ID, fd.FlowID, strings.Join(msgs, "; ")))
		}

		out = append(out, FlowVersion{
			FlowID: flowID,
			Method: method,
			Path:   path,
			Tree:   tree,
		})
	}
	return out, nil
}

// LoadBuiltinTemplates reads and parses every *.json file from the embedded
// templates FS. Returns an error if any file is malformed.
func LoadBuiltinTemplates() ([]Template, error) {
	entries, err := fs.ReadDir(builtinTemplateFS, "templates")
	if err != nil {
		return nil, wrapErr(Validation, "read embedded templates dir", err)
	}
	out := make([]Template, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := builtinTemplateFS.ReadFile("templates/" + e.Name())
		if err != nil {
			return nil, wrapErr(Validation, "read template file "+e.Name(), err)
		}
		var t Template
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, wrapErr(Validation, "decode template "+e.Name(), err)
		}
		out = append(out, t)
	}
	return out, nil
}
