package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/skillsmgmt"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
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

// toggleSkillDisabled sets or clears disable-model-invocation on a managed
// root skill's SKILL.md frontmatter.
func (s *Server) toggleSkillDisabled(w http.ResponseWriter, r *http.Request) {
	dir, ok := s.Skills.ResolveDir(r.PathValue("relPath"))
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_skill_path", "skill path is not in a managed skills root", false)
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

// removeSkill removes a globally installed skill (pi ecosystem only).
func (s *Server) removeSkill(w http.ResponseWriter, r *http.Request) {
	dir, ok := s.Skills.ResolveDir(r.PathValue("relPath"))
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_skill_path", "skill path is not in a managed skills root", false)
		return
	}
	if !isWithinPath(dir, s.Skills.PiGlobalRoot()) {
		writeError(w, r, http.StatusBadRequest, "skill_not_removable", "only marketplace-installed (pi) skills can be removed here", false)
		return
	}
	name := filepath.Base(dir)
	if _, err := skillsmgmt.Remove(r.Context(), name, true); err != nil {
		writeError(w, r, http.StatusBadGateway, "skill_remove_failed", err.Error(), false)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ——— skill roots management ———

// refreshSkillRoots reloads the enabled additional roots into the skills
// service so Settings changes take effect without a Worker restart.
func (s *Server) refreshSkillRoots(ctx context.Context) error {
	roots, err := s.SkillRoots.EnabledPaths(ctx)
	if err != nil {
		return err
	}
	out := make([]skillsmgmt.Root, 0, len(roots))
	for _, root := range roots {
		out = append(out, skillsmgmt.Root{Name: root.Name, Path: root.Path, Priority: root.Priority})
	}
	s.Skills.AdditionalRoots = out
	return nil
}

// listSkillRoots returns the configured additional skill roots.
func (s *Server) listSkillRoots(w http.ResponseWriter, r *http.Request) {
	roots, err := s.SkillRoots.List(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"items": roots})
}

// createSkillRoot adds an additional skills directory (or preset by agent kind).
func (s *Server) createSkillRoot(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		Path      string `json:"path"`
		AgentKind string `json:"agentKind"`
		Priority  int    `json:"priority"`
		Enabled   *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "body must be JSON", false)
		return
	}
	path, kind, err := resolveSkillRootPath(body.Path, body.AgentKind, s.Skills.HomeDir)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_skill_root", err.Error(), false)
		return
	}
	if body.Name == "" {
		body.Name = kind
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	root, err := s.SkillRoots.Create(r.Context(), store.CreateSkillRootInput{
		Name: body.Name, Path: path, AgentKind: kind, Priority: body.Priority, Enabled: enabled,
	})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "skill_root_create_failed", err.Error(), false)
		return
	}
	if err := s.refreshSkillRoots(r.Context()); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, root)
}

// updateSkillRoot patches an additional root.
func (s *Server) updateSkillRoot(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      *string `json:"name"`
		Path      *string `json:"path"`
		AgentKind *string `json:"agentKind"`
		Priority  *int    `json:"priority"`
		Enabled   *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "body must be JSON", false)
		return
	}
	patch := struct {
		Name      *string
		Path      *string
		AgentKind *string
		Priority  *int
		Enabled   *bool
	}{Name: body.Name, Path: body.Path, AgentKind: body.AgentKind, Priority: body.Priority, Enabled: body.Enabled}
	if patch.Path != nil {
		kind := ""
		if patch.AgentKind != nil {
			kind = *patch.AgentKind
		}
		path, resolvedKind, err := resolveSkillRootPath(*patch.Path, kind, s.Skills.HomeDir)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_skill_root", err.Error(), false)
			return
		}
		patch.Path = &path
		patch.AgentKind = &resolvedKind
	}
	root, err := s.SkillRoots.Update(r.Context(), r.PathValue("rootID"), patch)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if err := s.refreshSkillRoots(r.Context()); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, root)
}

// deleteSkillRoot removes an additional root.
func (s *Server) deleteSkillRoot(w http.ResponseWriter, r *http.Request) {
	if err := s.SkillRoots.Delete(r.Context(), r.PathValue("rootID")); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if err := s.refreshSkillRoots(r.Context()); err != nil {
		writeInternal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveSkillRootPath resolves a root path from an explicit path or a preset
// agent kind. Presets map to the skills.sh CLI ecosystem directories under the
// worker host home (the browser never computes host paths).
func resolveSkillRootPath(explicitPath, agentKind, home string) (string, string, error) {
	kind := strings.ToLower(strings.TrimSpace(agentKind))
	if explicitPath = strings.TrimSpace(explicitPath); explicitPath != "" {
		if kind == "" {
			kind = "generic"
		}
		return filepath.Clean(explicitPath), kind, nil
	}
	subdirs := map[string]string{
		"pi":      filepath.Join(".pi", "agent", "skills"),
		"claude":  filepath.Join(".claude", "skills"),
		"codex":   filepath.Join(".codex", "skills"),
		"cursor":  filepath.Join(".cursor", "skills"),
		"generic": filepath.Join(".agents", "skills"),
	}
	subdir, ok := subdirs[kind]
	if !ok {
		return "", "", fmt.Errorf("agentKind must be pi, claude, codex, cursor, or generic; or provide an explicit path")
	}
	return filepath.Join(home, subdir), kind, nil

}

func isWithinPath(candidate, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}
