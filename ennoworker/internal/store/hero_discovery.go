package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
)

// HeroDiscovery resolves file-authored Hero documents into executable Hero
// definitions (V2). The legacy global hero SQL repo and project-file
// candidate materialization were removed: file Heroes are published through
// globalsource and frozen into Sessions at Run start.
type HeroDiscovery struct {
	Models *ModelRepo
}

func (d *HeroDiscovery) ResolveDocument(ctx context.Context, document *heroesource.Document) (*domain.HeroDefinition, []domain.HeroValidationDiagnostic) {
	return d.resolveDefinition(ctx, document)
}

func (d *HeroDiscovery) resolveDefinition(ctx context.Context, document *heroesource.Document) (*domain.HeroDefinition, []domain.HeroValidationDiagnostic) {
	diagnostics := make([]domain.HeroValidationDiagnostic, 0)
	if d == nil || d.Models == nil {
		return nil, append(diagnostics, domain.HeroValidationDiagnostic{
			Level: "error", Code: "model_reference_invalid",
			Message: "file-backed model resolver is unavailable", Field: "model.ref",
		})
	}
	model, err := d.Models.ResolvePortableRef(ctx, document.Model.Ref)
	if err != nil {
		diagnostics = append(diagnostics, domain.HeroValidationDiagnostic{
			Level: "error", Code: "model_reference_invalid", Message: err.Error(), Field: "model.ref",
		})
		return nil, diagnostics
	}
	fallbackIDs := make([]string, 0, len(document.Model.Fallbacks))
	for index, ref := range document.Model.Fallbacks {
		fallback, resolveErr := d.Models.ResolvePortableRef(ctx, ref)
		if resolveErr != nil {
			diagnostics = append(diagnostics, domain.HeroValidationDiagnostic{
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
	skills := make([]domain.HeroSkillEntry, len(document.Skills))
	for index, skill := range document.Skills {
		skills[index] = domain.HeroSkillEntry{SkillID: skill.ID, Mode: skill.Mode}
	}
	definition := &domain.HeroDefinition{
		SchemaVersion: document.SchemaVersion, HeroPrompt: document.Prompt,
		ModelBinding: domain.HeroModelBinding{
			Mode: domain.HeroModelFixed, ModelProfileID: model.ID,
			ThinkingEffort: document.Model.ThinkingEffort, FallbackModelProfileIDs: fallbackIDs,
			OverridableFields: []string{},
		},
		Skills: domain.HeroSkills{Entries: skills}, Authority: document.Authority.Domain(),
		PermissionCeiling: document.PermissionCeiling, AllowedTools: append([]string(nil), document.AllowedTools...),
		ContextPolicy: domain.HeroContextPolicy{
			DefaultMode: document.Context.DefaultMode, AllowedModes: append([]domain.HeroContextMode(nil), document.Context.AllowedModes...),
			OwnExecutionContinuity: document.Context.OwnExecutionContinuity,
		},
		DelegationPolicy: domain.HeroDelegationPolicy{
			Admission:                  heroesource.AdmissionDomain(document.Delegation.Admission),
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

// heroTime formats a timestamp for the Session run/flow tables.
func heroTime(value time.Time) string { return value.Format(time.RFC3339Nano) }

// nullableString maps a string pointer to a nullable SQL value.
func nullableString(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return strings.TrimSpace(*value)
}
