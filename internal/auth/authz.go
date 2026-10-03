package auth

import (
	"context"
	"errors"

	"nzr-rules-engine/internal/decision"
)

// zenAuthorizer expresses authorization as a ZEN decision over the shared
// decision.Evaluator seam — the same authoring model as condition nodes, not
// hand-rolled role checks (AC-19). It is deny-by-default: a request proceeds only
// on an explicit allow==true; a missing, false or non-bool allow denies, and an
// Evaluate error is never treated as allow (fail closed, security-and-authz).
type zenAuthorizer struct {
	decide     decision.Evaluator
	authzJDMID string
}

// NewAuthorizer builds an Authorizer that evaluates authzJDMID through decide.
// A nil evaluator or empty JDM id is a programmer error surfaced at construction.
func NewAuthorizer(decide decision.Evaluator, authzJDMID string) (Authorizer, error) {
	if decide == nil {
		return nil, errors.New("auth: nil decision evaluator")
	}
	if authzJDMID == "" {
		return nil, errors.New("auth: authz jdm id required")
	}
	return &zenAuthorizer{decide: decide, authzJDMID: authzJDMID}, nil
}

// Authorize implements Authorizer. It projects the input into a plain map, calls
// the AuthZ JDM, and maps the result deny-by-default. An evaluator error
// propagates (classified by the decision slice) and is never an allow.
func (a *zenAuthorizer) Authorize(ctx context.Context, in AuthzInput) (Decision, error) {
	input := map[string]any{
		"user":     in.User,
		"roles":    in.Roles,
		"resource": in.Resource,
		"action":   in.Action,
	}
	for k, v := range in.Attrs {
		// Attrs never override the core projection keys.
		if _, clash := input[k]; !clash {
			input[k] = v
		}
	}

	out, err := a.decide.Evaluate(ctx, a.authzJDMID, input)
	if err != nil {
		// Fail closed: surface the error (classified upstream); the middleware
		// maps it to a non-200 and never to allow.
		return Decision{Allow: false}, err
	}

	allow, _ := out["allow"].(bool) // absent / non-bool => false => deny-by-default
	reason, _ := out["reason"].(string)
	return Decision{Allow: allow, Reason: reason}, nil
}
