package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
)

var (
	ErrFlowDraftConflict = errors.New("flow draft revision conflict")
	ErrFlowValidation    = errors.New("flow validation failed")
)

// --- Publish resolver wiring ---

// FlowPublishOptions wires the publish-time validator against the file-native
// Role/Skill catalog. The legacy SQL profile tables were removed in V2, so
// production always supplies Sources + Models.
type FlowPublishOptions struct {
	// DB and ProjectID are retained for source compatibility with callers
	// that still pass the legacy SQL handles. The file-native resolver never
	// reads them.
	DB        *sql.DB
	ProjectID string
	// FlowID is the owning flow; it enables flow-local Role and Skill
	// resolution (local/<handle>, local/<skill>).
	FlowID string
	// Skills is the known global Skill id set. SkillCatalog, when set, is
	// consulted first so a Skill created after startup is visible.
	Skills         map[string]bool
	SkillCatalog   func() map[string]string
	CheckAllowlist []string
	// CheckAllowlistAllowsAll mirrors the active policy mode
	// (allow_existing_behavior), where every executable is permitted.
	CheckAllowlistAllowsAll bool
	Sources                 *globalsource.Store
	Models                  *ModelRepo
}

// NewFlowValidator builds the publish validator.
func NewFlowValidator(opts FlowPublishOptions) *agentflow.Validator {
	return &agentflow.Validator{
		Resolver: &flowPublishResolver{
			flowID: opts.FlowID, skills: opts.Skills, skillCatalog: opts.SkillCatalog,
			sources: opts.Sources, models: opts.Models,
		},
		CheckAllowlist:          opts.CheckAllowlist,
		CheckAllowlistAllowsAll: opts.CheckAllowlistAllowsAll,
		MaxFanOut:               64,
		MaxRounds:               100,
	}
}

type flowPublishResolver struct {
	flowID       string
	skills       map[string]bool
	skillCatalog func() map[string]string
	sources      *globalsource.Store
	models       *ModelRepo
}

// ResolveRole resolves a task role reference at publish time. The semantics
// intentionally mirror graphrun freeze:
//   - local/<handle>  -> the owning flow's Graph-private Role document;
//   - global/<handle> -> latest (or an exact vNNNNNN / integer) published Role;
//   - bare <handle>   -> flow-local first, then the shared catalog's latest;
//   - handle@N        -> exact shared Role revision version N.
func (f *flowPublishResolver) ResolveRole(ctx context.Context, roleRef string) (*agentflow.RoleInfo, error) {
	ref := strings.TrimSpace(roleRef)
	handle, versionText, hasVersion := strings.Cut(ref, "@")
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return nil, fmt.Errorf("role reference %q has an empty handle", roleRef)
	}
	if f.sources == nil || f.models == nil {
		return nil, fmt.Errorf("file-native Role catalog is unavailable")
	}
	if strings.HasPrefix(handle, "local/") {
		if hasVersion {
			return nil, fmt.Errorf("flow-local role %q cannot be version-pinned", handle)
		}
		id := strings.TrimSpace(strings.TrimPrefix(handle, "local/"))
		if f.flowID == "" {
			return nil, fmt.Errorf("flow-local role %q needs an owning flow", handle)
		}
		document, _, err := f.sources.ReadGraphRole(f.flowID, id)
		if err != nil {
			return nil, fmt.Errorf("flow-local role %q: %w", id, err)
		}
		return f.resolveDocument(ctx, document)
	}
	globalHandle := handle
	if strings.HasPrefix(handle, "global/") {
		globalHandle = strings.TrimSpace(strings.TrimPrefix(handle, "global/"))
		if globalHandle == "" {
			return nil, fmt.Errorf("role reference %q has an empty handle", roleRef)
		}
	}
	if !hasVersion && !strings.HasPrefix(handle, "global/") {
		if f.flowID != "" {
			document, _, err := f.sources.ReadGraphRole(f.flowID, handle)
			switch {
			case err == nil:
				return f.resolveDocument(ctx, document)
			case errors.Is(err, os.ErrNotExist):
				// fall through to the shared catalog
			default:
				return nil, fmt.Errorf("flow-local role %q: %w", handle, err)
			}
		}
	}
	if !hasVersion {
		document, _, err := f.sources.LatestRoleRevision(globalHandle)
		if err != nil {
			return nil, fmt.Errorf("role %q is not published: %w", globalHandle, err)
		}
		return f.resolveDocument(ctx, document)
	}
	versionText = strings.TrimSpace(versionText)
	if versionText == "" {
		return nil, fmt.Errorf("role reference %q must use handle@version", roleRef)
	}
	revisions, err := f.sources.ListRoleRevisions(globalHandle)
	if err != nil {
		return nil, fmt.Errorf("role %q revisions: %w", globalHandle, err)
	}
	for _, revision := range revisions {
		if revision.ID() == versionText || strconv.Itoa(revision.Version) == versionText {
			document, _, err := f.sources.ReadRoleRevision(globalHandle, revision.ID())
			if err != nil {
				return nil, err
			}
			return f.resolveDocument(ctx, document)
		}
	}
	return nil, fmt.Errorf("role %q has no published version %q", globalHandle, versionText)
}

func (f *flowPublishResolver) resolveDocument(ctx context.Context, document *rolesource.Document) (*agentflow.RoleInfo, error) {
	definition, diagnostics := (&RoleDiscovery{Models: f.models}).ResolveDocument(ctx, document)
	if definition == nil {
		if len(diagnostics) > 0 {
			return nil, fmt.Errorf("role %q: %s", document.Handle, diagnostics[0].Message)
		}
		return nil, fmt.Errorf("role %q failed to resolve", document.Handle)
	}
	return &agentflow.RoleInfo{Definition: *definition}, nil
}

func (f *flowPublishResolver) KnownSkill(ctx context.Context, name string) bool {
	ref := strings.TrimSpace(name)
	scope, id, scoped := strings.Cut(ref, "/")
	if scoped {
		switch scope {
		case "local":
			return f.hasLocalSkill(id)
		case "global":
			return f.hasGlobalSkill(id)
		default:
			return false
		}
	}
	return f.hasLocalSkill(ref) || f.hasGlobalSkill(ref)
}

func (f *flowPublishResolver) hasLocalSkill(id string) bool {
	if f.sources == nil || f.flowID == "" || strings.TrimSpace(id) == "" {
		return false
	}
	directory, err := f.sources.GraphResourceDir(f.flowID, "skills", id)
	if err != nil {
		return false
	}
	_, err = skills.Load(directory)
	return err == nil
}

func (f *flowPublishResolver) hasGlobalSkill(id string) bool {
	if id == "" {
		return false
	}
	if f.skillCatalog != nil {
		if _, ok := f.skillCatalog()[id]; ok {
			return true
		}
	}
	return f.skills != nil && f.skills[id]
}

// ToolReadOnly resolves a tool's fan_out safety through the shared conservative
// classifier in domain. MCP remote tools and unknown tools fail closed.
func (f *flowPublishResolver) ToolReadOnly(ctx context.Context, tool string) bool {
	return domain.IsReadOnlySchedulingTool(tool)
}
