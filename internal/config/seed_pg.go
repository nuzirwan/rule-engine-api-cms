package config

import (
	"context"
	"encoding/json"
	"errors"

	"nzr-rules-engine/internal/connect"
)

// SeedPgStore loads a seed document (the same on-disk schema LoadSeed parses)
// into a Postgres-backed PgStore, idempotently, through the store's write
// methods — no raw SQL. It mirrors memStore.Seed with ONE behavioral difference
// the PgStore demands: PutFlowVersion lands a version validated=false and
// SetActive refuses an un-validated version, so an active flow is MarkValidated
// between the two (memStore's SetActive has no such gate).
//
// Idempotency: before writing anything, the first flow's active route is
// resolved via ActiveFlow. If it resolves (nil error), the store is already
// seeded and SeedPgStore returns (false, nil) without touching the DB. A
// NotFound means "absent" and seeding proceeds. This top-level skip is the
// primary re-run guard; the write methods are individually safe too (identity
// upserts + version bump under a FOR UPDATE lock).
func SeedPgStore(ctx context.Context, s *PgStore, raw []byte) (seeded bool, err error) {
	var sf seedFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		return false, wrapErr(Validation, "decode seed", err)
	}
	env := sf.Env

	// Idempotency gate: if the first flow's route is already active, skip.
	if len(sf.Flows) > 0 {
		first := sf.Flows[0]
		_, aerr := s.ActiveFlow(ctx, env, first.Method, first.Path)
		if aerr == nil {
			return false, nil // already seeded
		}
		if !errors.Is(aerr, ErrNotFound) {
			return false, aerr // a real failure (e.g. Postgres down) — surface it
		}
	}

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
			return false, err
		}
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
		if _, err := s.PutJDMVersion(ctx, env, j.ID, append([]byte(nil), j.Doc...), j.Version); err != nil {
			return false, err
		}
	}

	for _, c := range sf.Connections {
		if c.Key == "" || c.Type == "" {
			return false, newErr(Validation, "seed connection missing key or type")
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
	}

	return true, nil
}
