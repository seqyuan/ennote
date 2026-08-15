package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
)

// RoleDiscovery resolves file-authored Role documents into executable Role
// definitions (V2). The legacy global role SQL repo and project-file
// candidate materialization were removed: file Roles are published through
// globalsource and frozen into Sessions at Run start.
type RoleDiscovery struct {
	Models *ModelRepo
}

func (d *RoleDiscovery) ResolveDocument(ctx context.Context, document *rolesource.Document) (*domain.RoleDefinition, []domain.RoleValidationDiagnostic) {
	return d.resolveDefinition(ctx, document)
}

func (d *RoleDiscovery) resolveDefinition(ctx context.Context, document *rolesource.Document) (*domain.RoleDefinition, []domain.RoleValidationDiagnostic) {
	diagnostics := make([]domain.RoleValidationDiagnostic, 0)
	if d == nil || d.Models == nil {
		return nil, append(diagnostics, domain.RoleValidationDiagnostic{
			Level: "error", Code: "model_reference_invalid",
			Message: "file-backed model resolver is unavailable", Field: "model.ref",
		})
	}
	model, err := d.Models.ResolvePortableRef(ctx, document.Model.Ref)
	if err != nil {
		diagnostics = append(diagnostics, domain.RoleValidationDiagnostic{
			Level: "error", Code: "model_reference_invalid", Message: err.Error(), Field: "model.ref",
		})
		return nil, diagnostics
	}
	fallbackIDs := make([]string, 0, len(document.Model.Fallbacks))
	for index, ref := range document.Model.Fallbacks {
		fallback, resolveErr := d.Models.ResolvePortableRef(ctx, ref)
		if resolveErr != nil {
			diagnostics = append(diagnostics, domain.RoleValidationDiagnostic{
				Level: "error", Code: "model_reference_invalid", Message: resolveErr.Error(),
				Field: fmt.Sprintf("model.fallbacks[%d]", index),
			})
			continue
		}
		fallbackIDs = append(fallbackIDs, fallback.ID)
	}
	if len(diagnostics) > 0 {
		return nil, diagnostics
	}
	skills := make([]domain.RoleSkillEntry, len(document.Skills))
	for index, skill := range document.Skills {
		skills[index] = domain.RoleSkillEntry{SkillID: skill.ID, Mode: skill.Mode}
	}
	definition := &domain.RoleDefinition{
		SchemaVersion: document.SchemaVersion, RolePrompt: document.Prompt,
		ModelBinding: domain.RoleModelBinding{
			Mode: domain.RoleModelFixed, ModelProfileID: model.ID,
			ThinkingEffort: document.Model.ThinkingEffort, FallbackModelProfileIDs: fallbackIDs,
			OverridableFields: []string{},
		},
		Skills: domain.RoleSkills{Entries: skills}, Authority: document.Authority,
		PermissionCeiling: document.PermissionCeiling, AllowedTools: append([]string(nil), document.AllowedTools...),
		ContextPolicy: domain.RoleContextPolicy{
			DefaultMode: document.Context.DefaultMode, AllowedModes: append([]domain.RoleContextMode(nil), document.Context.AllowedModes...),
			OwnExecutionContinuity: document.Context.OwnExecutionContinuity,
		},
		DelegationPolicy: domain.RoleDelegationPolicy{
			Admission:                  document.Delegation.Admission,
			AllowedCallerKinds:         append([]string(nil), document.Delegation.AllowedCallerKinds...),
			AllowedStrategies:          append([]string(nil), document.Delegation.AllowedStrategies...),
			MaxInvocationsPerParentRun: document.Delegation.MaxInvocationsPerParentRun,
			MaxConcurrentInstances:     document.Delegation.MaxConcurrentInstances,
			BudgetCeiling: domain.DelegationBudgetCeiling{
				MaxModelCalls:    document.Delegation.BudgetCeiling.MaxModelCalls,
				MaxToolCalls:     document.Delegation.BudgetCeiling.MaxToolCalls,
				MaxTotalTokens:   document.Delegation.BudgetCeiling.MaxTotalTokens,
				MaxOutputTokens:  document.Delegation.BudgetCeiling.MaxOutputTokens,
				MaxCostUSDMicros: document.Delegation.BudgetCeiling.MaxCostUSDMicros,
				MaxWallTimeMS:    document.Delegation.BudgetCeiling.MaxWallTimeMS,
			},
		},
		OutputContract: document.OutputContract, MaxLoopIterations: document.MaxLoopIterations,
	}
	return definition, diagnostics
}

// roleToolRequiresMutation reports whether a tool can mutate the workspace.
func roleToolRequiresMutation(name string) bool {
	switch name {
	case "write", "edit", "exec", "bash", "publish_artifact":
		return true
	default:
		return false
	}
}

// roleTime formats a timestamp for the Session run/flow tables.
func roleTime(value time.Time) string { return value.Format(time.RFC3339Nano) }

// nullableString maps a string pointer to a nullable SQL value.
func nullableString(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return strings.TrimSpace(*value)
}
