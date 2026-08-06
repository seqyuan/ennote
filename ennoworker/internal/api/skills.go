package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/skillsmgmt"
)

// ——— skills management ———

func (s *Server) skillsWorkspaceDir(w http.ResponseWriter, r *http.Request, projectID string) string {
	if projectID == "" {
		return ""
	}
	record, err := s.Projects.FindWorkspaceByProjectID(r.Context(), projectID)
	if err != nil {
		writeInternal(w, r, err)
		return ""
	}
	if record == nil {
		writeError(w, r, http.StatusNotFound, "workspace_not_found", "project workspace not found", false)
		return ""
	}
	return record.HostPath
}

func (s *Server) skillsTrusted(w http.ResponseWriter, r *http.Request, projectID string) bool {
	record, err := s.Projects.FindWorkspaceByProjectID(r.Context(), projectID)
	if err != nil {
		writeInternal(w, r, err)
		return false
	}
	if record == nil {
		writeError(w, r, http.StatusNotFound, "workspace_not_found", "project workspace not found", false)
		return false
	}
	canonical, err := filepath.Abs(record.HostPath)
	if err != nil {
		canonical = record.HostPath
	}
	trusted, err := s.Trust.IsTrusted(record.ID, canonical)
	if err != nil {
		writeInternal(w, r, err)
		return false
	}
	return trusted
}

// listSkills returns the merged catalog with install annotations and, for a
// selected project, the project-scope skills plus its trust status.
func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	workspaceDir := s.skillsWorkspaceDir(w, r, projectID)
	if workspaceDir == "" && projectID != "" {
		return // error already written
	}
	result, err := s.Skills.List(workspaceDir)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if projectID != "" && s.Trust != nil {
		result.ProjectResourcesLoaded = s.skillsTrusted(w, r, projectID)
	}
	writeData(w, http.StatusOK, result)
}

// toggleSkillDisabled sets or clears disable-model-invocation on a user-root
// skill's SKILL.md frontmatter.
func (s *Server) toggleSkillDisabled(w http.ResponseWriter, r *http.Request) {
	dir, ok := s.resolveUserSkillDir(r.PathValue("relPath"))
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_skill_path", "skill path must stay inside the user skills root", false)
		return
	}
	var body struct {
		Disabled bool `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "body must be JSON with a disabled boolean", false)
		return
	}
	if _, err := s.Skills.ToggleDisabled(dir, body.Disabled); err != nil {
		writeError(w, r, http.StatusBadRequest, "skill_toggle_failed", err.Error(), false)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// searchSkills queries the skills.sh registry (with npx find fallback).
func (s *Server) searchSkills(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, _ = strconv.Atoi(raw)
	}
	results, err := skillsmgmt.Search(r.Context(), query, limit, "")
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "skill_search_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"results": results})
}

// installSkill runs `npx skills add` (global, or project-scoped when the
// project workspace is trusted).
func (s *Server) installSkill(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Package   string `json:"package"`
		Scope     string `json:"scope"`
		ProjectID string `json:"projectId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "body must be JSON", false)
		return
	}
	pkg := strings.TrimSpace(body.Package)
	if pkg == "" {
		writeError(w, r, http.StatusBadRequest, "package_required", "package is required", false)
		return
	}
	if body.Scope != "global" && body.Scope != "project" {
		writeError(w, r, http.StatusBadRequest, "invalid_scope", "scope must be global or project", false)
		return
	}
	cwd := ""
	if body.Scope == "project" {
		if body.ProjectID == "" {
			writeError(w, r, http.StatusBadRequest, "project_required", "projectId is required for project installs", false)
			return
		}
		if !s.skillsTrusted(w, r, body.ProjectID) {
			writeError(w, r, http.StatusForbidden, "project_not_trusted", "project resources must be trusted before installing project skills", false)
			return
		}
		workspaceDir := s.skillsWorkspaceDir(w, r, body.ProjectID)
		if workspaceDir == "" {
			return // error already written
		}
		cwd = workspaceDir
	}
	output, err := skillsmgmt.Install(r.Context(), pkg, body.Scope, cwd)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "skill_install_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"success": true, "output": output})
}

// checkSkillUpdates compares recorded version hashes against the remote.
func (s *Server) checkSkillUpdates(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID string `json:"projectId"`
		Package   string `json:"package"`
		Scope     string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "body must be JSON", false)
		return
	}
	if (body.Package == "") != (body.Scope == "") {
		writeError(w, r, http.StatusBadRequest, "invalid_filter", "package and scope must be provided together", false)
		return
	}
	workspaceDir := s.skillsWorkspaceDir(w, r, body.ProjectID)
	if workspaceDir == "" && body.ProjectID != "" {
		return // error already written
	}
	result, err := s.Skills.List(workspaceDir)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	var installs []skillsmgmt.InstallInfo
	for i := range result.Skills {
		install := result.Skills[i].Install
		if install == nil {
			continue
		}
		if body.Package != "" && (install.Package != body.Package || install.Scope != body.Scope) {
			continue
		}
		installs = append(installs, *install)
	}
	if body.Package != "" && len(installs) == 0 {
		writeError(w, r, http.StatusNotFound, "installed_skill_not_found", "installed skill not found", false)
		return
	}
	updates := skillsmgmt.CheckUpdates(r.Context(), installs, os.Getenv("GITHUB_TOKEN"), "")
	writeData(w, http.StatusOK, map[string]any{"updates": updates})
}

// updateSkill re-runs the install command for an existing install.
func (s *Server) updateSkill(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID string `json:"projectId"`
		Package   string `json:"package"`
		Scope     string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "body must be JSON", false)
		return
	}
	if body.Package == "" || (body.Scope != "global" && body.Scope != "project") {
		writeError(w, r, http.StatusBadRequest, "invalid_update", "package and scope are required", false)
		return
	}
	workspaceDir := s.skillsWorkspaceDir(w, r, body.ProjectID)
	if workspaceDir == "" && body.ProjectID != "" {
		return
	}
	result, err := s.Skills.List(workspaceDir)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	var target *skillsmgmt.InstallInfo
	for i := range result.Skills {
		install := result.Skills[i].Install
		if install != nil && install.Package == body.Package && install.Scope == body.Scope {
			target = install
			break
		}
	}
	if target == nil {
		writeError(w, r, http.StatusNotFound, "installed_skill_not_found", "installed skill not found", false)
		return
	}
	if !target.CanCheckForUpdates {
		writeError(w, r, http.StatusBadRequest, "skill_not_updatable", "this skill cannot be updated automatically", false)
		return
	}
	output, err := skillsmgmt.Update(r.Context(), *target, workspaceDir)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "skill_update_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"success": true, "output": output})
}

// removeSkill removes a user-root skill (global scope only).
func (s *Server) removeSkill(w http.ResponseWriter, r *http.Request) {
	dir, ok := s.resolveUserSkillDir(r.PathValue("relPath"))
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_skill_path", "skill path must stay inside the user skills root", false)
		return
	}
	name := filepath.Base(dir)
	if _, err := skillsmgmt.Remove(r.Context(), name, true); err != nil {
		writeError(w, r, http.StatusBadGateway, "skill_remove_failed", err.Error(), false)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveUserSkillDir safely maps a catalog relPath onto the user skills root,
// rejecting traversal and absolute paths.
func (s *Server) resolveUserSkillDir(relPath string) (string, bool) {
	raw := strings.TrimSpace(relPath)
	if raw == "" || strings.HasPrefix(raw, "/") || strings.Contains(raw, "..") {
		return "", false
	}
	dir := filepath.Join(s.Skills.UserRoot, filepath.FromSlash(raw))
	if !isWithinPath(dir, s.Skills.UserRoot) {
		return "", false
	}
	return dir, true
}

func isWithinPath(candidate, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}
