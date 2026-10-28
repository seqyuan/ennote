package packinstall

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/agentpack"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

// validateFlow runs the real publish validator against the staged documents
// before anything is written. The resolver reads the pack's in-memory Role and
// Skill files first, then falls back to already-installed global sources.
func (p *planner) validateFlow(packFlowID string, definition *domain.FlowDefinition) error {
	resolver := &stagedResolver{planner: p}
	validator := &agentflow.Validator{
		Resolver: resolver, CheckAllowlist: p.service.CheckAllowlist,
		CheckAllowlistAllowsAll: p.service.CheckAllowlistAllowsAll,
		MaxFanOut:               64, MaxRounds: 100,
	}
	result := validator.Validate(p.ctx, definition)
	if result.Valid {
		return nil
	}
	messages := make([]string, 0, len(result.Diagnostics))
	for _, diagnostic := range result.Diagnostics {
		messages = append(messages, diagnostic.Code+": "+diagnostic.Message)
	}
	return fmt.Errorf("flow %q failed staged validation: %s", packFlowID, strings.Join(messages, "; "))
}

type stagedResolver struct {
	planner *planner
}

func (r *stagedResolver) ResolveRole(ctx context.Context, roleRef string) (*agentflow.RoleInfo, error) {
	ref := strings.TrimSpace(roleRef)
	handle, versionText, hasVersion := strings.Cut(ref, "@")
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return nil, fmt.Errorf("role reference %q has an empty handle", roleRef)
	}
	scope, id, scoped := strings.Cut(handle, "/")
	if !scoped {
		id = handle
	}
	if scoped && scope != "local" && scope != "global" {
		return nil, fmt.Errorf("role reference %q must use local/ or global/", roleRef)
	}
	if !hasVersion {
		if document, declared := r.planner.roleDocs[id]; declared {
			entry, _ := r.planner.roleEntry(id)
			if scoped && scope == "local" && entry.Scope != agentpack.RoleScopeFlow {
				return nil, fmt.Errorf("local/%s is declared with scope: %s", id, entry.Scope)
			}
			if scoped && scope == "global" && entry.Scope != agentpack.RoleScopeGlobal {
				return nil, fmt.Errorf("global/%s is declared with scope: %s", id, entry.Scope)
			}
			definition, diagnostics := (&store.RoleDiscovery{Models: r.planner.service.Models}).ResolveDocument(ctx, document)
			if definition == nil {
				if len(diagnostics) > 0 {
					return nil, fmt.Errorf("role %q: %s", id, diagnostics[0].Message)
				}
				return nil, fmt.Errorf("role %q failed to resolve", id)
			}
			return &agentflow.RoleInfo{Definition: *definition}, nil
		}
	}
	if strings.HasPrefix(handle, "local/") {
		return nil, fmt.Errorf("flow-local role %q is not declared by the pack", handle)
	}
	globalHandle := id
	if hasVersion {
		revisions, err := r.planner.service.Sources.ListRoleRevisions(globalHandle)
		if err != nil {
			return nil, err
		}
		for _, revision := range revisions {
			if revision.ID() == strings.TrimSpace(versionText) || strconv.Itoa(revision.Version) == strings.TrimSpace(versionText) {
				document, _, err := r.planner.service.Sources.ReadRoleRevision(globalHandle, revision.ID())
				if err != nil {
					return nil, err
				}
				definition, diagnostics := (&store.RoleDiscovery{Models: r.planner.service.Models}).ResolveDocument(ctx, document)
				if definition == nil {
					if len(diagnostics) > 0 {
						return nil, fmt.Errorf("role %q: %s", globalHandle, diagnostics[0].Message)
					}
					return nil, fmt.Errorf("role %q failed to resolve", globalHandle)
				}
				return &agentflow.RoleInfo{Definition: *definition}, nil
			}
		}
		return nil, fmt.Errorf("role %q has no published version %q", globalHandle, versionText)
	}
	document, _, err := r.planner.service.Sources.LatestRoleRevision(globalHandle)
	if err != nil {
		return nil, fmt.Errorf("role %q is not installed: %w", globalHandle, err)
	}
	definition, diagnostics := (&store.RoleDiscovery{Models: r.planner.service.Models}).ResolveDocument(ctx, document)
	if definition == nil {
		if len(diagnostics) > 0 {
			return nil, fmt.Errorf("role %q: %s", globalHandle, diagnostics[0].Message)
		}
		return nil, fmt.Errorf("role %q failed to resolve", globalHandle)
	}
	return &agentflow.RoleInfo{Definition: *definition}, nil
}

func (r *stagedResolver) KnownSkill(_ context.Context, name string) bool {
	ref := strings.TrimSpace(name)
	scope, id, scoped := strings.Cut(ref, "/")
	if scoped {
		switch scope {
		case "local":
			entry, declared := r.planner.skillEntries[id]
			return declared && entry.Scope == agentpack.SkillScopeFlow
		case "global":
			return r.hasVendoredGlobalSkill(id) || r.hasInstalledSkill(id)
		default:
			return false
		}
	}
	if _, declared := r.planner.skillEntries[ref]; declared {
		return true
	}
	return r.hasInstalledSkill(ref)
}

func (r *stagedResolver) hasVendoredGlobalSkill(id string) bool {
	entry, declared := r.planner.skillEntries[id]
	return declared && entry.Scope == agentpack.SkillScopeGlobal
}

func (r *stagedResolver) hasInstalledSkill(id string) bool {
	if id == "" {
		return false
	}
	_, err := skills.Load(filepath.Join(r.planner.service.SkillsRoot, id))
	return err == nil
}

func (r *stagedResolver) ToolReadOnly(_ context.Context, tool string) bool {
	return domain.IsReadOnlySchedulingTool(tool)
}
