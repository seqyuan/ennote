package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

func (s *Server) createRole(w http.ResponseWriter, r *http.Request) {
	if !s.rolesAvailable(w, r) {
		return
	}
	var input struct {
		Handle      string                `json:"handle"`
		Name        string                `json:"name"`
		Description string                `json:"description"`
		Positioning string                `json:"positioning"`
		Icon        string                `json:"icon"`
		Color       string                `json:"color"`
		Scope       domain.RoleScope      `json:"scope"`
		ProjectID   *string               `json:"projectId"`
		FlowID      *string               `json:"flowId"`
		Definition  domain.RoleDefinition `json:"definition"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	role, err := s.Roles.Create(r.Context(), store.CreateRoleInput{
		Handle: input.Handle, Name: input.Name, Description: input.Description,
		Positioning: input.Positioning, Icon: input.Icon, Color: input.Color,
		Scope: input.Scope, ProjectID: input.ProjectID, FlowID: input.FlowID,
		Definition: input.Definition,
	})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_role", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, role)
}

func (s *Server) listRoles(w http.ResponseWriter, r *http.Request) {
	if !s.rolesAvailable(w, r) {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeError(w, r, http.StatusBadRequest, "invalid_role_page", "limit must be between 1 and 100", false)
			return
		}
		limit = value
	}
	var projectID *string
	if value := strings.TrimSpace(r.URL.Query().Get("projectId")); value != "" {
		projectID = &value
	}
	var flowID *string
	if value := strings.TrimSpace(r.URL.Query().Get("flowId")); value != "" {
		flowID = &value
	}
	items, err := s.Roles.List(r.Context(), store.ListRolesInput{
		Query: r.URL.Query().Get("q"), Scope: domain.RoleScope(r.URL.Query().Get("scope")),
		ProjectID: projectID, FlowID: flowID, Status: r.URL.Query().Get("status"),
		Cursor: r.URL.Query().Get("cursor"), Limit: limit,
	})
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	nextCursor := ""
	if len(items) == limit {
		nextCursor = items[len(items)-1].ID
	}
	writeData(w, http.StatusOK, map[string]any{"items": items, "nextCursor": nextCursor})
}

func (s *Server) getRole(w http.ResponseWriter, r *http.Request) {
	if !s.rolesAvailable(w, r) {
		return
	}
	role, err := s.Roles.Get(r.Context(), r.PathValue("roleID"))
	if s.writeRoleError(w, r, err) {
		return
	}
	writeData(w, http.StatusOK, role)
}

func (s *Server) updateRoleDraft(w http.ResponseWriter, r *http.Request) {
	if !s.rolesAvailable(w, r) {
		return
	}
	var input struct {
		ExpectedRevision int                   `json:"expectedRevision"`
		Handle           *string               `json:"handle"`
		Name             *string               `json:"name"`
		Description      *string               `json:"description"`
		Positioning      *string               `json:"positioning"`
		Icon             *string               `json:"icon"`
		Color            *string               `json:"color"`
		Definition       domain.RoleDefinition `json:"definition"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	role, err := s.Roles.UpdateDraft(r.Context(), r.PathValue("roleID"), store.UpdateRoleDraftInput{
		ExpectedRevision: input.ExpectedRevision, Handle: input.Handle, Name: input.Name,
		Description: input.Description, Positioning: input.Positioning, Icon: input.Icon,
		Color: input.Color, Definition: input.Definition,
	})
	if s.writeRoleError(w, r, err) {
		return
	}
	writeData(w, http.StatusOK, role)
}

func (s *Server) validateRole(w http.ResponseWriter, r *http.Request) {
	if !s.rolesAvailable(w, r) {
		return
	}
	result, err := s.Roles.Validate(r.Context(), r.PathValue("roleID"))
	if s.writeRoleError(w, r, err) {
		return
	}
	writeData(w, http.StatusOK, result)
}

func (s *Server) publishRole(w http.ResponseWriter, r *http.Request) {
	if !s.rolesAvailable(w, r) {
		return
	}
	var input struct {
		ExpectedRevision int `json:"expectedRevision"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	version, err := s.Roles.Publish(r.Context(), r.PathValue("roleID"), input.ExpectedRevision)
	if s.writeRoleError(w, r, err) {
		return
	}
	writeData(w, http.StatusCreated, version)
}

func (s *Server) archiveRole(w http.ResponseWriter, r *http.Request) {
	if !s.rolesAvailable(w, r) {
		return
	}
	roleID := r.PathValue("roleID")
	if err := s.Roles.Archive(r.Context(), roleID); s.writeRoleError(w, r, err) {
		return
	}
	role, err := s.Roles.Get(r.Context(), roleID)
	if s.writeRoleError(w, r, err) {
		return
	}
	writeData(w, http.StatusOK, role)
}

func (s *Server) listRoleVersions(w http.ResponseWriter, r *http.Request) {
	if !s.rolesAvailable(w, r) {
		return
	}
	versions, err := s.Roles.ListVersions(r.Context(), r.PathValue("roleID"))
	if s.writeRoleError(w, r, err) {
		return
	}
	writeData(w, http.StatusOK, versions)
}

func (s *Server) getRoleVersion(w http.ResponseWriter, r *http.Request) {
	if !s.rolesAvailable(w, r) {
		return
	}
	version, err := s.Roles.GetVersion(r.Context(), r.PathValue("roleID"), r.PathValue("versionID"))
	if s.writeRoleError(w, r, err) {
		return
	}
	writeData(w, http.StatusOK, version)
}

func (s *Server) rolesAvailable(w http.ResponseWriter, r *http.Request) bool {
	if s.Roles != nil {
		return true
	}
	writeError(w, r, http.StatusServiceUnavailable, "roles_unavailable", "Role management is unavailable", true)
	return false
}

func (s *Server) writeRoleError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, store.ErrRoleNotFound), errors.Is(err, store.ErrRoleVersionNotFound):
		writeError(w, r, http.StatusNotFound, "role_not_found", "Role or Role version not found", false)
	case errors.Is(err, store.ErrRoleDraftConflict):
		writeError(w, r, http.StatusConflict, "role_draft_conflict", "Role draft changed; reload before saving or publishing", false)
	case errors.Is(err, store.ErrRoleValidation):
		writeError(w, r, http.StatusUnprocessableEntity, "role_validation_failed", err.Error(), false)
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_role", err.Error(), false)
	}
	return true
}
