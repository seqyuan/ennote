package packinstall

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/skillsmgmt"
)

// nonTerminalRunStates are the run_agent_flow states that still hold a Flow. A
// non-terminal Run must finish before its Flow can be removed, because the Run
// froze the Flow definition and may still resume from it.
var nonTerminalRunStates = []string{"pending", "running"}

// Remove uninstalls a pack. It refuses while a non-terminal flow Run references
// an installed flow, and (without force) while an installed object was edited
// locally or a global Role is still referenced by another Graph. A Skill edited
// locally is kept with a warning instead of being deleted.
func (s *Service) Remove(ctx context.Context, packID string, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateState(); err != nil {
		return err
	}
	if s.Sources == nil || strings.TrimSpace(s.SkillsRoot) == "" {
		return fmt.Errorf("pack installer source store or skills root is not configured")
	}
	state, err := s.readState(strings.TrimSpace(packID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newError(CodeNotInstalled, fmt.Sprintf("pack %s is not installed", packID))
		}
		return err
	}
	for _, flow := range state.Flows {
		active, err := s.countActiveRuns(ctx, flow.StoreID)
		if err != nil {
			return err
		}
		if active > 0 {
			return errorf(CodeInstallBlocked,
				"flow %q is referenced by %d non-terminal flow run(s); finish or cancel them before removing %s",
				flow.StoreID, active, state.Pack.ID)
		}
	}
	if !force {
		bound, err := s.enabledBindings(ctx, state)
		if err != nil {
			return err
		}
		if len(bound) > 0 {
			return errorf(CodeInstallBlocked,
				"flow(s) %s are still enabled by a Project binding; disable the binding or remove with force",
				strings.Join(bound, ", "))
		}
		edited, err := s.editedObjects(state)
		if err != nil {
			return err
		}
		if len(edited) > 0 {
			return errorf(CodeInstallBlocked,
				"installed object(s) %s were edited locally; reinstall the pack or remove with force",
				strings.Join(edited, ", "))
		}
		referenced, err := s.rolesReferencedByOtherGraphs(state)
		if err != nil {
			return err
		}
		if len(referenced) > 0 {
			return errorf(CodeInstallBlocked,
				"global Role(s) %s are still referenced by other Graphs; remove with force to delete them anyway",
				strings.Join(referenced, ", "))
		}
	}
	s.remove(state)
	statePath, err := s.statePath(state.Pack.ID)
	if err != nil {
		return err
	}
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove pack state: %w", err)
	}
	if manifestPath, pathErr := s.manifestCopyPath(state.Pack.ID); pathErr == nil {
		if err := os.RemoveAll(filepath.Dir(manifestPath)); err != nil {
			return fmt.Errorf("remove pack manifest copy: %w", err)
		}
	}
	return nil
}

// enabledBindings lists installed flow store ids that a Project still has
// enabled. Force removal is allowed to leave the binding stale so a user can
// still recover their files; the default path refuses to silently break a run.
func (s *Service) enabledBindings(ctx context.Context, state *State) ([]string, error) {
	if s.Projects == nil {
		return nil, nil
	}
	owned := map[string]bool{}
	for _, flow := range state.Flows {
		if flow.Format == "flow" {
			owned[flow.StoreID] = true
		}
	}
	if len(owned) == 0 {
		return nil, nil
	}
	projects, err := s.Projects.List(ctx)
	if err != nil {
		return nil, err
	}
	enabled := map[string]bool{}
	for _, project := range projects {
		manifest, err := s.Projects.ReadManifest(project.ID)
		if err != nil {
			return nil, err
		}
		for _, binding := range manifest.GraphBindings {
			kind := binding.SourceKind
			if kind == "" {
				kind = projectstore.SourceKindGraph
			}
			if kind == projectstore.SourceKindFlow && binding.Enabled && owned[binding.GraphID] {
				enabled[binding.GraphID] = true
			}
		}
	}
	ids := make([]string, 0, len(enabled))
	for id := range enabled {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// countActiveRuns counts the non-terminal run_agent_flow rows whose
// flow_version_id is "<storeID>@<revision>". A flow can only have Runs from a
// published revision, so a pack-installed draft is normally invisible here; the
// check still matters once a user publishes and starts a Run.
func (s *Service) countActiveRuns(ctx context.Context, storeID string) (int, error) {
	if s.Sessions == nil {
		return 0, nil
	}
	sessionIDs, err := s.Sessions.AllSessionIDs()
	if err != nil {
		return 0, fmt.Errorf("list sessions: %w", err)
	}
	prefix := storeID + "@"
	total := 0
	for _, sessionID := range sessionIDs {
		db, err := s.Sessions.OpenSession(ctx, sessionID)
		if err != nil {
			return 0, err
		}
		var count int
		err = db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM run_agent_flow
			 WHERE state IN (?, ?) AND substr(flow_version_id, 1, ?) = ?`,
			nonTerminalRunStates[0], nonTerminalRunStates[1], len(prefix), prefix).Scan(&count)
		if err != nil {
			return 0, fmt.Errorf("count active flow runs in session %s: %w", sessionID, err)
		}
		total += count
	}
	return total, nil
}

// editedObjects lists installed objects whose current digest no longer matches
// the digest recorded at install time. Skills are excluded: they are kept with a
// warning rather than blocking removal.
func (s *Service) editedObjects(state *State) ([]string, error) {
	edited := make([]string, 0)
	for _, flow := range state.Flows {
		var digest string
		var err error
		if flow.Format == "flow" {
			_, digest, err = s.Sources.ReadFlow(flow.StoreID)
		} else {
			_, digest, err = s.Sources.ReadGraph(flow.StoreID)
		}
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			edited = append(edited, flow.Format+":"+flow.StoreID)
			continue
		}
		if digest != flow.InstalledGraphDigest {
			edited = append(edited, flow.Format+":"+flow.StoreID)
		}
	}
	for _, role := range state.Roles {
		if role.Scope == roleScopeGlobal {
			_, digest, err := s.Sources.ReadRole(role.StoreID)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				edited = append(edited, "role:"+role.StoreID)
				continue
			}
			if digest != role.Digest {
				edited = append(edited, "role:"+role.StoreID)
			}
			continue
		}
		graphID, handle, found := strings.Cut(role.StoreID, "/")
		if !found {
			continue
		}
		_, digest, err := s.Sources.ReadGraphRole(graphID, handle)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			edited = append(edited, "graph_role:"+role.StoreID)
			continue
		}
		if digest != role.Digest {
			edited = append(edited, "graph_role:"+role.StoreID)
		}
	}
	return edited, nil
}

// rolesReferencedByOtherGraphs finds global Roles this pack installed that other
// Graphs still point at ("global/<handle>"). Deleting them would break those
// Graphs, so an unforced remove refuses.
func (s *Service) rolesReferencedByOtherGraphs(state *State) ([]string, error) {
	owned := map[string]bool{}
	for _, flow := range state.Flows {
		owned[flow.StoreID] = true
	}
	handles := map[string]bool{}
	for _, role := range state.Roles {
		if role.Scope == roleScopeGlobal {
			handles[role.StoreID] = true
		}
	}
	if len(handles) == 0 {
		return nil, nil
	}
	graphs, err := s.Sources.ListGraphs()
	if err != nil {
		return nil, err
	}
	referenced := map[string]bool{}
	for _, graph := range graphs {
		if graph.Document == nil || owned[graph.ID] {
			continue
		}
		for _, task := range graph.Document.Tasks {
			handle, found := strings.CutPrefix(task.Role, "global/")
			if found && handles[handle] {
				referenced[handle] = true
			}
		}
	}
	flows, err := s.Sources.ListFlows()
	if err != nil {
		return nil, err
	}
	for _, flow := range flows {
		if flow.Flow == nil || owned[flow.ID] {
			continue
		}
		for _, task := range flow.Flow.Tasks {
			ref := strings.TrimSpace(task.Role)
			handle := ""
			if value, found := strings.CutPrefix(ref, "global/"); found {
				handle = value
			} else if ref != "" && !strings.Contains(ref, "/") && !strings.Contains(ref, "@") {
				if _, _, roleErr := s.Sources.ReadGraphRole(flow.ID, ref); errors.Is(roleErr, os.ErrNotExist) {
					handle = ref
				}
			}
			handle, _, _ = strings.Cut(handle, "@")
			if handle != "" && handles[handle] {
				referenced[handle] = true
			}
		}
	}
	names := make([]string, 0, len(referenced))
	for handle := range referenced {
		names = append(names, handle)
	}
	sort.Strings(names)
	return names, nil
}

// remove deletes the installed objects. Whole directories are removed because a
// Role/Graph revision sequence must stay contiguous, and a Skill keeps a local
// edit instead of being destroyed.
func (s *Service) remove(state *State) {
	for _, flow := range state.Flows {
		if err := s.Sources.DeleteGraph(flow.StoreID); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("pack remove: delete graph failed", "pack", state.Pack.ID, "graph", flow.StoreID, "error", err)
		}
	}
	for _, role := range state.Roles {
		if role.Scope != roleScopeGlobal {
			continue // removed with its Graph directory
		}
		if err := s.Sources.DeleteRole(role.StoreID); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("pack remove: delete role failed", "pack", state.Pack.ID, "role", role.StoreID, "error", err)
		}
	}
	for _, skill := range state.Skills {
		if skill.Scope != skillScopeGlobal {
			continue // removed with its Graph directory
		}
		root := s.SkillsRoot
		digest, err := skillsmgmt.ContentDigest(root, skill.StoreID)
		if err != nil || digest != skill.ContentDigest {
			slog.Warn("pack remove: skill was edited locally; keeping it",
				"pack", state.Pack.ID, "skill", skill.StoreID, "warning", WarningSkillKept)
			continue
		}
		removeSkillDir(root, skill.StoreID)
	}
}
