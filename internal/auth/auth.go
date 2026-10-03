// Package auth is Slice C's authentication/authorization surface
// (lld-contracts.md): AuthN validates a bearer JWT against a rotating JWKS and
// yields a Principal; AuthZ expresses allow/deny as a ZEN decision over the
// decision.Evaluator seam. Both fail closed — deny by default — per the
// security-and-authz standard. The net/http middleware chain injects the
// Principal into the request context and maps failures to 401 (AuthN) / 403
// (AuthZ) without leaking detail to the client.
//
// This package is pure Go: AuthZ uses ZEN only through the decision.Evaluator
// interface, so auth carries no cgo import even though a decision runs behind it.
package auth

import "context"

// Authenticator validates a bearer token and returns the caller principal
// (frozen seam, lld-contracts.md).
type Authenticator interface {
	Authenticate(ctx context.Context, bearer string) (Principal, error)
}

// Authorizer decides allow/deny for a principal performing an action on a
// resource, expressed as a ZEN decision (frozen seam, lld-contracts.md).
type Authorizer interface {
	Authorize(ctx context.Context, in AuthzInput) (Decision, error)
}

// Principal is the authenticated caller. Claims carries the remaining JWT claims
// for AuthZ input; Subject is the "sub"; Roles come from a configured claim.
type Principal struct {
	Subject string
	Roles   []string
	Claims  map[string]any
}

// AuthzInput is the projection handed to the AuthZ decision: who (User/Roles),
// what (Resource/Action), plus extra attributes for ABAC rules.
type AuthzInput struct {
	User     string
	Roles    []string
	Resource string
	Action   string
	Attrs    map[string]any
}

// Decision is the AuthZ result. Reason is for audit logs, never for the client.
type Decision struct {
	Allow  bool
	Reason string
}
