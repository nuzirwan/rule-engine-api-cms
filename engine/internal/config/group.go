package config

import (
	"regexp"
	"time"
)

// ScalingMode defines how a group scales its workers.
type ScalingMode string

const (
	// ScalingModeStatic keeps a fixed number of replicas running at all times.
	ScalingModeStatic ScalingMode = "static"
	// ScalingModeDynamic scales replicas up/down based on workload (KEDA).
	ScalingModeDynamic ScalingMode = "dynamic"
	// ScalingModeEphemeral spins up workers on demand and terminates them when idle.
	ScalingModeEphemeral ScalingMode = "ephemeral"
)

// validScalingModes is the set of allowed scaling modes for validation.
var validScalingModes = map[ScalingMode]bool{
	ScalingModeStatic:    true,
	ScalingModeDynamic:   true,
	ScalingModeEphemeral: true,
}

// groupIDRegex validates group IDs: lowercase letter followed by lowercase
// letters, digits, or hyphens.
var groupIDRegex = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Resources defines CPU and memory requests/limits for a worker pod.
type Resources struct {
	CPURequest    string `json:"cpuRequest,omitempty"`
	CPULimit      string `json:"cpuLimit,omitempty"`
	MemoryRequest string `json:"memoryRequest,omitempty"`
	MemoryLimit   string `json:"memoryLimit,omitempty"`
}

// ScalingConfig holds the scaling parameters for a group's workers.
type ScalingConfig struct {
	Mode           ScalingMode `json:"mode"`
	MinReplicas    int         `json:"minReplicas"`
	MaxReplicas    int         `json:"maxReplicas"`
	ScaleDownDelay string      `json:"scaleDownDelay,omitempty"` // e.g. "5m"
	StartupTimeout string      `json:"startupTimeout,omitempty"` // e.g. "30s"
	Resources      *Resources  `json:"resources,omitempty"`
}

// Group is the worker group definition stored in Postgres. Connections is a slice
// of connection IDs assigned to this group; a connection belongs to at most one
// group (§3.2 design constraint).
type Group struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Version     int           `json:"version"`
	Connections []string      `json:"connections,omitempty"`
	Scaling     ScalingConfig `json:"scaling"`
	Enabled     bool          `json:"enabled"`
	CreatedAt   time.Time     `json:"createdAt"`
	UpdatedAt   time.Time     `json:"updatedAt"`
}

// GroupAuditData holds the full group state including connections for audit log
// entries. The audit log stores this denormalized snapshot so rollback can
// restore the exact state (design requirement: audit stores full state).
type GroupAuditData struct {
	Group       *Group   `json:"group"`
	Connections []string `json:"connections,omitempty"`
}

// GroupSummary is the list response shape for GET /admin/groups (one row per group).
type GroupSummary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Enabled   bool      `json:"enabled"`
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ValidateGroup validates a Group and returns a Validation-class error if invalid.
// Rules:
//   - ID required, must match ^[a-z][a-z0-9-]*$
//   - Name required
//   - Scaling.Mode must be static, dynamic, or ephemeral
//   - MinReplicas >= 0
//   - MaxReplicas >= MinReplicas
//   - static mode requires MinReplicas > 0 (at least one replica must run)
func ValidateGroup(g *Group) error {
	if g == nil {
		return newErr(Validation, "group is nil")
	}

	// ID required and must match pattern
	if g.ID == "" {
		return newErr(Validation, "group id is required")
	}
	if !groupIDRegex.MatchString(g.ID) {
		return newErr(Validation, "group id must match ^[a-z][a-z0-9-]*$ (lowercase, start with letter)")
	}

	// Name required
	if g.Name == "" {
		return newErr(Validation, "group name is required")
	}

	// ScalingMode must be one of the valid modes
	if !validScalingModes[g.Scaling.Mode] {
		return newErr(Validation, "scaling mode must be one of: static, dynamic, ephemeral")
	}

	// MinReplicas >= 0
	if g.Scaling.MinReplicas < 0 {
		return newErr(Validation, "minReplicas must be >= 0")
	}

	// MaxReplicas >= MinReplicas
	if g.Scaling.MaxReplicas < g.Scaling.MinReplicas {
		return newErr(Validation, "maxReplicas must be >= minReplicas")
	}

	// static mode requires MinReplicas > 0
	if g.Scaling.Mode == ScalingModeStatic && g.Scaling.MinReplicas == 0 {
		return newErr(Validation, "static scaling mode requires minReplicas > 0")
	}

	return nil
}
