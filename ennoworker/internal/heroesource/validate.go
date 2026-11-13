package heroesource

import (
	"fmt"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// ValidateForWrite enforces the PortableHeroDocument write-time constraints
// declared by contracts/openapi.yaml. It is deliberately separate from
// normalizeAndValidate: normalizeAndValidate runs on the READ path (Parse,
// SourceDigest, Encode, and every revision reader depend on it), so it must
// stay lenient enough to keep loading pre-existing Hero files and already
// published revisions. Rejecting these values there would make a legacy file
// unopenable and break the "published revisions stay runnable" guarantee.
//
// ValidateForWrite calls normalizeAndValidate first so a document that passes
// here encodes exactly as it would have before this check existed; it then
// enforces only the constraints a reader is allowed to skip. Every message
// names the field path so the UI can point at the offending field.
func ValidateForWrite(document *Document) error {
	if document == nil {
		return fmt.Errorf("Hero source document is required")
	}
	if err := normalizeAndValidate(document); err != nil {
		return err
	}

	switch document.PermissionCeiling {
	case domain.PermissionDiscuss, domain.PermissionAsk, domain.PermissionAuto:
	default:
		return fmt.Errorf("permissionCeiling must be one of discuss, ask, auto")
	}

	switch document.Delegation.Admission {
	case domain.DelegationAutoWithinBudget, domain.DelegationApprovalRequired, AdmissionDeny:
	default:
		return fmt.Errorf("delegation.admission must be one of auto_within_budget, approval_required, deny")
	}
	if document.Delegation.MaxInvocationsPerParentRun < 1 {
		return fmt.Errorf("delegation.maxInvocationsPerParentRun must be >= 1")
	}
	if document.Delegation.MaxConcurrentInstances < 1 {
		return fmt.Errorf("delegation.maxConcurrentInstances must be >= 1")
	}

	ceiling := document.Delegation.BudgetCeiling
	if ceiling.MaxModelCalls < 1 {
		return fmt.Errorf("delegation.budgetCeiling.maxModelCalls must be >= 1")
	}
	if ceiling.MaxToolCalls < 0 {
		return fmt.Errorf("delegation.budgetCeiling.maxToolCalls must be >= 0")
	}
	if ceiling.MaxTotalTokens < 1 {
		return fmt.Errorf("delegation.budgetCeiling.maxTotalTokens must be >= 1")
	}
	if ceiling.MaxOutputTokens < 1 {
		return fmt.Errorf("delegation.budgetCeiling.maxOutputTokens must be >= 1")
	}
	if ceiling.MaxCostUSDMicros < 0 {
		return fmt.Errorf("delegation.budgetCeiling.maxCostUsdMicros must be >= 0")
	}
	if ceiling.MaxWallTimeMS < 1 {
		return fmt.Errorf("delegation.budgetCeiling.maxWallTimeMs must be >= 1")
	}

	if document.MaxLoopIterations < 1 {
		return fmt.Errorf("maxLoopIterations must be >= 1")
	}

	if len(document.Context.AllowedModes) == 0 {
		return fmt.Errorf("context.allowedModes must not be empty")
	}
	if document.Context.DefaultMode == "" {
		return fmt.Errorf("context.defaultMode must not be empty")
	}
	if !containsContextMode(document.Context.AllowedModes, document.Context.DefaultMode) {
		return fmt.Errorf("context.defaultMode must be a member of context.allowedModes")
	}

	switch document.Model.ThinkingEffort {
	case domain.ThinkingDefault, domain.ThinkingLow, domain.ThinkingMedium, domain.ThinkingHigh:
	default:
		return fmt.Errorf("model.thinkingEffort must be one of default, low, medium, high")
	}

	if document.Context.OwnExecutionContinuity != domain.HeroContinuityNone {
		return fmt.Errorf("context.ownExecutionContinuity must be \"none\"")
	}

	if strings.TrimSpace(document.Prompt) == "" {
		return fmt.Errorf("prompt must not be empty")
	}
	if len(document.Prompt) > 65536 {
		return fmt.Errorf("prompt must be at most 65536 bytes")
	}

	return nil
}

func containsContextMode(modes []domain.HeroContextMode, target domain.HeroContextMode) bool {
	for _, mode := range modes {
		if mode == target {
			return true
		}
	}
	return false
}
