package config

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/flow"
)

// seedFile is the on-disk seed schema. It is deliberately JSON-friendly (durations
// as milliseconds, JDM bytes as a raw JSON document) and is converted into the
// store's typed shapes by Seed. The thin-slice seed uses the empty "" env.
type seedFile struct {
	Env         string        `json:"env"`
	Flows       []seedFlow    `json:"flows"`
	JDMs        []seedJDM     `json:"jdms"`
	Connections []seedConnDef `json:"connections"`
}

// seedFlow is one flow version in the seed.
type seedFlow struct {
	FlowID   string        `json:"flowId"`
	Version  int           `json:"version"`
	Method   string        `json:"method"`
	Path     string        `json:"path"`
	Active   bool          `json:"active"`
	Tree     flow.Node     `json:"tree"`
	Fixtures []FlowFixture `json:"fixtures,omitempty"`
}

// seedJDM is one JDM in the seed. Doc carries the JDM graph as raw JSON so the
// seed file stays a single readable document; it is stored as the JDM bytes.
type seedJDM struct {
	ID      string          `json:"id"`
	Version int             `json:"version"`
	Doc     json.RawMessage `json:"doc"`
}

// seedConnDef is one connection definition in the seed. Resilience is expressed
// in milliseconds for readability and converted to a connect.ResiliencePolicy.
type seedConnDef struct {
	Key        string          `json:"key"`
	Type       string          `json:"type"`
	Settings   map[string]any  `json:"settings,omitempty"`
	SecretRef  string          `json:"secretRef,omitempty"`
	Resilience *seedResilience `json:"resilience,omitempty"`
}

// seedResilience is the JSON-friendly resilience block (milliseconds).
type seedResilience struct {
	TimeoutMS   int `json:"timeoutMs,omitempty"`
	MaxAttempts int `json:"maxAttempts,omitempty"`
}

// LoadSeed reads a seed JSON file from path and loads it into a new in-memory
// store. A read or decode failure is a Validation error (fail fast at startup).
func LoadSeed(path string) (*memStore, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, wrapErr(Validation, "read seed file", err)
	}
	return SeedFromBytes(raw)
}

// SeedFromBytes loads a seed document from raw bytes into a new store.
func SeedFromBytes(raw []byte) (*memStore, error) {
	var sf seedFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		return nil, wrapErr(Validation, "decode seed", err)
	}
	s := NewMemStore()
	if err := s.Seed(context.Background(), sf); err != nil {
		return nil, err
	}
	return s, nil
}

// Seed loads a decoded seed document into the store: it puts and (where marked)
// activates each flow version, stores each JDM's bytes + version, and records
// the connection defs for the seed's env.
func (s *memStore) Seed(ctx context.Context, sf seedFile) error {
	env := sf.Env

	for _, f := range sf.Flows {
		fv := FlowVersion{
			FlowID:   f.FlowID,
			Version:  f.Version,
			Method:   f.Method,
			Path:     f.Path,
			Tree:     f.Tree,
			Fixtures: f.Fixtures,
		}
		version, err := s.PutFlowVersion(ctx, env, fv)
		if err != nil {
			return err
		}
		if f.Active {
			if err := s.SetActive(ctx, env, f.FlowID, version); err != nil {
				return err
			}
		}
	}

	for _, j := range sf.JDMs {
		if len(j.Doc) == 0 {
			return newErr(Validation, "seed jdm "+j.ID+" has empty doc")
		}
		s.putJDM(env, j.ID, append([]byte(nil), j.Doc...), j.Version)
	}

	defs := make([]connect.ConnectionDef, 0, len(sf.Connections))
	for _, c := range sf.Connections {
		if c.Key == "" || c.Type == "" {
			return newErr(Validation, "seed connection missing key or type")
		}
		defs = append(defs, connect.ConnectionDef{
			Key:        c.Key,
			Type:       c.Type,
			Settings:   c.Settings,
			SecretRef:  c.SecretRef,
			Resilience: toResiliencePolicy(c.Resilience),
		})
	}
	s.setConnections(env, defs)
	return nil
}

// putJDM stores a JDM's bytes + version under (env,id).
func (s *memStore) putJDM(env, id string, bytes []byte, version int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jdms[env] == nil {
		s.jdms[env] = make(map[string]jdmEntry)
	}
	if version == 0 {
		version = 1
	}
	s.jdms[env][id] = jdmEntry{bytes: bytes, version: version}
}

// setConnections records the connection defs for env.
func (s *memStore) setConnections(env string, defs []connect.ConnectionDef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[env] = defs
}

// toResiliencePolicy converts the JSON-friendly resilience block to the typed
// policy; a nil block yields the zero policy (connect applies its defaults).
func toResiliencePolicy(r *seedResilience) connect.ResiliencePolicy {
	var p connect.ResiliencePolicy
	if r == nil {
		return p
	}
	if r.TimeoutMS > 0 {
		p.Timeout = time.Duration(r.TimeoutMS) * time.Millisecond
	}
	if r.MaxAttempts > 0 {
		p.Retry.MaxAttempts = r.MaxAttempts
	}
	return p
}
