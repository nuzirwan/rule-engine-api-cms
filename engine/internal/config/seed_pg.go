package config

import (
	"context"
	"encoding/json"

	"nzr-rules-engine/internal/connect"
)

// SeedPgStore loads a seed document (the same on-disk schema LoadSeed parses)
// into a Postgres-backed PgStore, idempotently, through the store's write
// methods — no raw SQL. It mirrors memStore.Seed with ONE behavioral difference
// the PgStore demands: PutFlowVersion lands a version validated=false and
// SetActive refuses an un-validated version, so an active flow is MarkValidated
// between the two (memStore's SetActive has no such gate).
//
// Idempotency is PER-OBJECT, not all-or-nothing (slice-f-admin-api.md §6): each
// flow / JDM / connection is written ONLY if its identity does not already exist
// (FlowExists / JDMExists / ConnectionExists). An existing object is skipped and
// never version-churned — the seed is a bootstrap, not a migration tool
// (decision D3): editing an existing object is an admin-API/Strapi operation, not
// a re-seed. This fixes the live bug where adding ONE new flow to seed.json was
// silently skipped on restart because the first flow was already active. Return
// seeded = (anything was written).
func SeedPgStore(ctx context.Context, s *PgStore, raw []byte) (seeded bool, err error) {
	var sf seedFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		return false, wrapErr(Validation, "decode seed", err)
	}
	env := sf.Env

	wrote := false

	for _, f := range sf.Flows {
		exists, err := s.FlowExists(ctx, env, f.FlowID)
		if err != nil {
			return false, err // a real failure (e.g. Postgres down) — surface it
		}
		if exists {
			continue // bootstrap-only: an existing flow is never re-created
		}
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
			return false, err
		}
		wrote = true
		if f.Active {
			// PgStore blocks publishing an un-validated version; validate first.
			if err := s.MarkValidated(ctx, env, f.FlowID, version); err != nil {
				return false, err
			}
			if err := s.SetActive(ctx, env, f.FlowID, version); err != nil {
				return false, err
			}
		}
	}

	for _, j := range sf.JDMs {
		if len(j.Doc) == 0 {
			return false, newErr(Validation, "seed jdm "+j.ID+" has empty doc")
		}
		exists, err := s.JDMExists(ctx, env, j.ID)
		if err != nil {
			return false, err
		}
		if exists {
			continue
		}
		if _, err := s.PutJDMVersion(ctx, env, j.ID, append([]byte(nil), j.Doc...), j.Version); err != nil {
			return false, err
		}
		wrote = true
	}

	for _, c := range sf.Connections {
		if c.Key == "" || c.Type == "" {
			return false, newErr(Validation, "seed connection missing key or type")
		}
		exists, err := s.ConnectionExists(ctx, env, c.Key)
		if err != nil {
			return false, err
		}
		if exists {
			continue
		}
		def := connect.ConnectionDef{
			Key:        c.Key,
			Type:       c.Type,
			Settings:   c.Settings,
			SecretRef:  c.SecretRef,
			Resilience: toResiliencePolicy(c.Resilience),
		}
		if _, err := s.PutConnectionVersion(ctx, env, def); err != nil {
			return false, err
		}
		wrote = true
	}

	return wrote, nil
}
