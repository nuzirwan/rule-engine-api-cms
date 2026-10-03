package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/flow"
)

// AdminFixture is the richer fixture the admin plane accepts on the wire for
// validate/dry-run (slice-f-admin-api.md §4.2). It is NOT the persisted
// config.FlowFixture (which stays {Name,Input,Want}); Mocks + the structured
// Expect are validation-time-only. On persist of a candidate flow, fixtures are
// down-mapped to config.FlowFixture (name+input preserved, Expect.Output -> Want;
// Mocks are not persisted).
type AdminFixture struct {
	Name   string                    `json:"name"`
	Input  map[string]any            `json:"input"`
	Mocks  map[string]map[string]any `json:"mocks,omitempty"` // "conn:{key}" -> "op:{name}" -> result
	Expect struct {
		Output     map[string]any `json:"output,omitempty"`
		BranchPath []string       `json:"branchPath,omitempty"`
		Errors     []string       `json:"errors,omitempty"`
	} `json:"expect"`
}

// downMapFixtures converts the admin-local AdminFixtures to the persisted
// config.FlowFixture set: name + input are preserved and Expect.Output maps to
// Want. Mocks and the other Expect sub-fields are validation-time-only and not
// persisted.
func downMapFixtures(in []AdminFixture) []config.FlowFixture {
	if len(in) == 0 {
		return nil
	}
	out := make([]config.FlowFixture, 0, len(in))
	for _, f := range in {
		out = append(out, config.FlowFixture{
			Name:  f.Name,
			Input: f.Input,
			Want:  f.Expect.Output,
		})
	}
	return out
}

// toIssueBodies renders flow validation issues into the stable wire shape the
// Strapi UI consumes (nodeId + machine code + message).
func toIssueBodies(issues []flow.ValidationIssue) []map[string]any {
	out := make([]map[string]any, 0, len(issues))
	for _, iss := range issues {
		out = append(out, map[string]any{
			"nodeId":  iss.NodeID,
			"code":    iss.Code,
			"message": iss.Message,
		})
	}
	return out
}

// storeRefs adapts the admin store to flow.RefResolver so ValidateTree can check
// dangling connection/JDM references against the active config (§4.1).
type storeRefs struct {
	ctx   context.Context
	store AdminStore
	env   string
}

// HasConnection reports whether key is an active connection in env. A store error
// is treated as "present" so a transient store blip does not block an author on a
// false dangling-ref (§4.1); the real resolve re-checks at publish/runtime.
func (s storeRefs) HasConnection(key string) bool {
	defs, err := s.store.Connections(s.ctx, s.env)
	if err != nil {
		return true
	}
	for _, d := range defs {
		if d.Key == key {
			return true
		}
	}
	return false
}

// HasJDM reports whether id resolves to an active JDM. A NotFound is "absent";
// any other error is treated as "present" (don't block the author on a blip).
func (s storeRefs) HasJDM(id string) bool {
	_, _, err := s.store.GetJDM(s.ctx, s.env, id)
	if err == nil {
		return true
	}
	return !isNotFound(err)
}

// isNotFound reports whether err is a config NotFound.
func isNotFound(err error) bool {
	return err != nil && errors.Is(err, config.ErrNotFound)
}

// rejectIfPresent is a JSON field type that records whether the key was present
// in the request body at all (even if null), so the connection handler can reject
// any secret-value field without allowing it to be silently decoded (AC-20).
type rejectIfPresent struct {
	present bool
}

// UnmarshalJSON marks the field present regardless of its value.
func (p *rejectIfPresent) UnmarshalJSON(b []byte) error {
	p.present = true
	return nil
}

// secretValueKeys are the connection-settings keys that look like a secret VALUE
// (not a secret_ref pointer). Their presence under settings is rejected.
var secretValueKeys = map[string]bool{
	"password": true, "secret": true, "token": true, "apikey": true, "api_key": true,
}

// hasSecretValueInSettings reports whether settings carries a secret-value field
// (e.g. settings.password), which the admin edge rejects — secret_ref ONLY.
func hasSecretValueInSettings(settings map[string]any) bool {
	for k := range settings {
		if secretValueKeys[strings.ToLower(k)] {
			return true
		}
	}
	return false
}

// jsonRawEmpty reports whether a json.RawMessage is empty or JSON null.
func jsonRawEmpty(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null"
}
