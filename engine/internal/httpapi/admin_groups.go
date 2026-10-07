package httpapi

import (
	"context"
	"errors"
	"net/http"

	"nzr-rules-engine/internal/auth"
	"nzr-rules-engine/internal/config"
)

// GroupAdminStore is the narrow method-set for group admin handlers.
type GroupAdminStore interface {
	GetGroup(ctx context.Context, env, groupID string) (config.Group, error)
	GetGroupVersion(ctx context.Context, env, groupID string) (int, error)
	ListGroups(ctx context.Context, env string) ([]config.GroupSummary, error)
	UpsertGroup(ctx context.Context, env string, group *config.Group, changedBy, reason string) error
	DeleteGroup(ctx context.Context, env, groupID, changedBy, reason string) error
	CountFlowsByGroup(ctx context.Context, env, groupID string) (int, error)
}

// compile-time assertion that *config.PgStore satisfies GroupAdminStore.
var _ GroupAdminStore = (*config.PgStore)(nil)

// ---- PUT /admin/groups/{id} -> UpsertGroup ----

// putGroupRequest is the wire shape for creating or updating a group.
type putGroupRequest struct {
	Env         string               `json:"env,omitempty"`
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Connections []string             `json:"connections,omitempty"`
	Scaling     config.ScalingConfig `json:"scaling"`
	Enabled     *bool                `json:"enabled,omitempty"` // Pointer to detect omission (defaults to true)
	Reason      string               `json:"reason,omitempty"`
}

// HandlePutGroup creates or updates a group. It decodes the request, validates
// using config.ValidateGroup (validation parity), calls store.UpsertGroup, and
// returns the group. The group ID comes from the URL path.
func (a *Admin) HandlePutGroup(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	groupID := r.PathValue("id")
	if groupID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing group id")
		return
	}

	var req putGroupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !a.checkEnv(w, req.Env) {
		return
	}

	// Default enabled to true if not specified.
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	// Build the Group struct.
	group := &config.Group{
		ID:          groupID,
		Name:        req.Name,
		Description: req.Description,
		Connections: req.Connections,
		Scaling:     req.Scaling,
		Enabled:     enabled,
	}

	// Validate using the shared ValidateGroup function (validation parity).
	if err := config.ValidateGroup(group); err != nil {
		a.fail(w, r.Context(), "admin.putGroup", err)
		return
	}

	// Get the actor from context for audit.
	ctx := a.auditCtx(r.Context())
	changedBy := "admin"
	if op, ok := auth.OperatorFrom(ctx); ok {
		changedBy = op.Subject
	}

	// Cast store to GroupAdminStore.
	gs, ok := a.store.(GroupAdminStore)
	if !ok {
		writeError(w, http.StatusInternalServerError, "group store not available")
		return
	}

	if err := gs.UpsertGroup(ctx, a.env, group, changedBy, req.Reason); err != nil {
		a.fail(w, ctx, "admin.putGroup", err)
		return
	}

	// Fetch the updated group to return current state.
	updated, err := gs.GetGroup(ctx, a.env, groupID)
	if err != nil {
		a.fail(w, ctx, "admin.putGroup", err)
		return
	}

	writeJSON(w, http.StatusOK, updated)
}

// ---- GET /admin/groups/{id} -> GetGroup ----

// HandleGetGroup retrieves a group by ID.
func (a *Admin) HandleGetGroup(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	groupID := r.PathValue("id")
	if groupID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing group id")
		return
	}

	ctx := r.Context()
	gs, ok := a.store.(GroupAdminStore)
	if !ok {
		writeError(w, http.StatusInternalServerError, "group store not available")
		return
	}

	group, err := gs.GetGroup(ctx, a.env, groupID)
	if err != nil {
		a.fail(w, ctx, "admin.getGroup", err)
		return
	}

	writeJSON(w, http.StatusOK, group)
}

// ---- GET /admin/groups -> ListGroups ----

// HandleListGroups returns all groups as summaries.
func (a *Admin) HandleListGroups(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}

	ctx := r.Context()
	gs, ok := a.store.(GroupAdminStore)
	if !ok {
		writeError(w, http.StatusInternalServerError, "group store not available")
		return
	}

	groups, err := gs.ListGroups(ctx, a.env)
	if err != nil {
		a.fail(w, ctx, "admin.listGroups", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

// ---- DELETE /admin/groups/{id} -> DeleteGroup ----

// deleteGroupRequest is the optional wire shape for delete reason.
type deleteGroupRequest struct {
	Reason string `json:"reason,omitempty"`
}

// HandleDeleteGroup deletes a group. It rejects deletion of the 'default' group
// (403) and rejects when flows are assigned (409).
func (a *Admin) HandleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	groupID := r.PathValue("id")
	if groupID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing group id")
		return
	}

	// Parse optional reason from body.
	var req deleteGroupRequest
	_ = decodeJSON(r, &req) // Ignore error for optional body.

	ctx := a.auditCtx(r.Context())
	gs, ok := a.store.(GroupAdminStore)
	if !ok {
		writeError(w, http.StatusInternalServerError, "group store not available")
		return
	}

	changedBy := "admin"
	if op, ok := auth.OperatorFrom(ctx); ok {
		changedBy = op.Subject
	}

	err := gs.DeleteGroup(ctx, a.env, groupID, changedBy, req.Reason)
	if err != nil {
		// Map specific errors to appropriate HTTP status codes.
		if errors.Is(err, config.ErrCannotDeleteDefault) {
			writeError(w, http.StatusForbidden, "cannot delete default group")
			return
		}
		if errors.Is(err, config.ErrGroupHasFlows) {
			writeError(w, http.StatusConflict, "group has flows assigned")
			return
		}
		a.fail(w, ctx, "admin.deleteGroup", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---- GET /internal/groups/{id}/version -> GetGroupVersion ----

// HandleGetGroupVersion returns only the version number for a group (lightweight
// endpoint for worker hot-reload polling).
func (a *Admin) HandleGetGroupVersion(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	groupID := r.PathValue("id")
	if groupID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing group id")
		return
	}

	ctx := r.Context()
	gs, ok := a.store.(GroupAdminStore)
	if !ok {
		writeError(w, http.StatusInternalServerError, "group store not available")
		return
	}

	version, err := gs.GetGroupVersion(ctx, a.env, groupID)
	if err != nil {
		a.fail(w, ctx, "admin.getGroupVersion", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"version": version})
}
