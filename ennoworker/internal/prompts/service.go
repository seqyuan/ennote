package prompts

import (
	"context"
	"errors"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

// Service is the top-level prompts subsystem. It wires together builtins,
// config, registry resolution, global CRUD, and invocation expansion.
type Service struct {
	HomeDir     string
	Projects    *store.ProjectRepo
	TrustStore  *workspace.TrustStore
	Builtins    []Template
	GlobalStore *GlobalStore
}

// ProjectList resolves the effective template catalog for a project.
// Returns ErrPromptConfigInvalid / ErrWorkspaceTrustUnavailable /
// ErrPromptResourceLimit / ErrPromptStorageUnavailable for infrastructure
// failures — never a partial registry.
func (s *Service) ProjectList(ctx context.Context, projectID string) (*Registry, error) {
	wSpace, err := s.Projects.FindWorkspaceByProjectID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("find workspace: %w", err)
	}
	if wSpace == nil {
		return nil, fmt.Errorf("%w: project %q", ErrProjectNotFound, projectID)
	}

	canonicalRoot, err := workspace.CanonicalWorkspaceRoot(wSpace.HostPath)
	if err != nil {
		return nil, fmt.Errorf("canonical workspace root: %w", err)
	}

	trusted, trustErr := s.TrustStore.IsTrusted(wSpace.ID, canonicalRoot)
	if trustErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrWorkspaceTrustUnavailable, trustErr)
	}

	settingsPaths, configErr := LoadConfigPaths(s.HomeDir)
	if configErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrPromptConfigInvalid, configErr)
	}

	ctx2 := ResolveContext{
		HomeDir:       s.HomeDir,
		WorkspaceID:   wSpace.ID,
		CanonicalRoot: canonicalRoot,
		Trusted:       trusted,
		SettingsPaths: settingsPaths,
	}

	return Resolve(ctx2, s.Builtins, s.GlobalStore, NewResolveBudget())
}

// ProjectExpand parses an invocation and expands it against the project's
// template catalog. Invalid invocations return an invalid_invocation result
// immediately without reading infrastructure. Unknown names return
// not_found. Invocations over 16 KiB return ErrInvocationTooLarge.
func (s *Service) ProjectExpand(ctx context.Context, projectID, invocation string) (ExpandResult, error) {
	// Parser fast path: check invocation size and syntax first.
	// No project/workspace/config/trust/global reads before this.
	if len(invocation) > 16*1024 {
		return ExpandResult{}, ErrInvocationTooLarge
	}

	parsed, ok := ParseInvocation(invocation)
	if !ok {
		return NewInvalidInvocationExpand(), nil
	}

	// Now resolve the registry for the project.
	reg, err := s.ProjectList(ctx, projectID)
	if err != nil {
		return ExpandResult{}, err
	}

	return reg.ExpandParsedInvocation(parsed.Name, parsed.RawArguments)
}

// ManagementList returns the global management catalog
// (builtin + settings + global). The settings tier degrades (warning
// diagnostic) when config is invalid or the request budget is exhausted;
// the global tier enters recovery mode when over the entry limit. Never
// returns an error for recoverable conditions.
func (s *Service) ManagementList() (*Registry, []GlobalPromptTemplateEntry, bool, []Diagnostic, error) {
	settingsPaths, configErr := LoadConfigPaths(s.HomeDir)

	budget := NewResolveBudget()
	reg := NewRegistry()
	for _, t := range s.Builtins {
		reg.Add(t)
	}

	var diags []Diagnostic

	// Settings tier: degrade with warning on config failure or budget
	// exhaustion.
	if configErr != nil {
		diags = append(diags, Diagnostic{
			Level: "warning", Code: "prompt_config_invalid",
			Source: "settings", Message: "config.json is invalid; settings tier disabled",
		})
	} else if len(settingsPaths) > 0 {
		if err := loadSettingsTier(reg, settingsPaths, budget); err != nil {
			diags = append(diags, Diagnostic{
				Level: "warning", Code: "prompt_resource_limit",
				Source: "settings", Message: "request budget exhausted; settings tier disabled",
			})
		}
	}

	// Global tier.
	var globalEntries []GlobalPromptTemplateEntry
	var recoveryMode bool
	if s.GlobalStore != nil {
		result := s.GlobalStore.List()
		globalEntries = result.GlobalEntries
		recoveryMode = result.RecoveryMode
		diags = append(diags, result.Diagnostics...)

		if !recoveryMode {
			for _, t := range result.Templates {
				full, err := s.GlobalStore.Get(t.Name)
				if err != nil {
					continue
				}
				reg.Add(full)
			}
		}
	}

	diags = append(diags, reg.Diagnostics()...)
	return reg, globalEntries, recoveryMode, sanitizeDiagnostics(diags), nil
}

// IsInfrastructureError reports whether err is one of the typed
// request-level failures that map to a 500 ErrorEnvelope (never surfaced as
// a not_found or a partial result).
func IsInfrastructureError(err error) bool {
	return errors.Is(err, ErrPromptConfigInvalid) ||
		errors.Is(err, ErrWorkspaceTrustUnavailable) ||
		errors.Is(err, ErrPromptResourceLimit) ||
		errors.Is(err, ErrPromptStorageUnavailable)
}
